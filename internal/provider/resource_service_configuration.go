package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin"
)

// ServiceConfigurationResource manages the mutable subset of a service's configuration.
//
// This is deliberately a partial-management resource, and that shapes its whole design. The Admin API
// has no operation that resets a service setting to the value discovery gave it, and the contract does
// not document what a JSON null means for these fields. Rather than guess:
//
//   - Only the fields the configuration sets are sent, never a null.
//   - Removing the resource, or an attribute from it, stops managing that setting and leaves whatever
//     value is in place. Nothing is reverted.
//   - Destroy is therefore a state-only operation, and says so.
//
// That is the honest behaviour given the contract, and it is documented on the resource so nobody
// discovers it by finding a setting still applied after a destroy.
type ServiceConfigurationResource struct {
	data *providerData
}

var (
	_ resource.Resource                   = (*ServiceConfigurationResource)(nil)
	_ resource.ResourceWithConfigure      = (*ServiceConfigurationResource)(nil)
	_ resource.ResourceWithImportState    = (*ServiceConfigurationResource)(nil)
	_ resource.ResourceWithValidateConfig = (*ServiceConfigurationResource)(nil)
)

// NewServiceConfigurationResource returns the resource factory.
func NewServiceConfigurationResource() resource.Resource { return &ServiceConfigurationResource{} }

func (r *ServiceConfigurationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_configuration"
}

type serviceConfigurationModel struct {
	ID          types.String `tfsdk:"id"`
	ServiceName types.String `tfsdk:"service_name"`

	Public                      types.Bool    `tfsdk:"public"`
	IdempotencyRetention        durationValue `tfsdk:"idempotency_retention"`
	JournalRetention            durationValue `tfsdk:"journal_retention"`
	WorkflowCompletionRetention durationValue `tfsdk:"workflow_completion_retention"`
	InactivityTimeout           durationValue `tfsdk:"inactivity_timeout"`
	AbortTimeout                durationValue `tfsdk:"abort_timeout"`

	ServiceType  types.String `tfsdk:"service_type"`
	Revision     types.Int64  `tfsdk:"revision"`
	DeploymentID types.String `tfsdk:"deployment_id"`
}

func (r *ServiceConfigurationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Overrides for the mutable subset of a discovered service's configuration.\n\n" +
			"### This resource manages settings, not the service\n\n" +
			"A service is created and destroyed by deployment discovery. This resource neither creates nor deletes " +
			"one: it applies configuration to a service that already exists, so it must depend on the " +
			"`restate_deployment` that exposes it.\n\n" +
			"### Removing it does not revert anything\n\n" +
			"The Admin API has no operation that resets a setting to the value discovery gave it, and the pinned " +
			"contract does not define what a JSON null means for these fields. The provider therefore sends only " +
			"the attributes you set, and never a null.\n\n" +
			"The consequence is explicit: **`terraform destroy`, or removing an attribute from this resource, " +
			"stops Terraform managing that setting and leaves the applied value in place.** Nothing is reverted. " +
			"To restore a discovered default, re-register the deployment with `overwrite = true`, or set the value " +
			"you want here explicitly.\n\n" +
			"### Discovery can overwrite these settings\n\n" +
			"Re-registering the deployment with `overwrite = true` replaces the recorded schema information, which " +
			"can reset these settings to what the SDK reports. The next `terraform plan` will then show a diff and " +
			"re-apply them; that is expected, not a bug.\n\n" +
			"Durations may be written in either the friendly form (`1 day`, `90 minutes`, `30s`) or ISO-8601 " +
			"(`P1D`, `PT1H30M`). The provider compares them by meaning, so the server echoing a value back in the " +
			"other form is not a diff.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Same as `service_name`: a service's name is its identity in the Admin API.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"service_name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Name of the discovered service to configure. Changing it means configuring a " +
					"different service, so it replaces this resource -- which, per the note above, leaves the old " +
					"service's applied settings in place.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},

			"public": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Whether the service is reachable through the environment's ingress. Set it to " +
					"`false` to make a service invocable only from other services.",
			},
			"idempotency_retention": schema.StringAttribute{
				Optional:            true,
				CustomType:          durationType{},
				MarkdownDescription: "How long the result of a completed idempotent invocation is retained.",
			},
			"journal_retention": schema.StringAttribute{
				Optional:   true,
				CustomType: durationType{},
				MarkdownDescription: "How long an invocation's journal is retained after it completes. Longer " +
					"retention costs storage but makes an invocation introspectable for longer.",
			},
			"workflow_completion_retention": schema.StringAttribute{
				Optional:   true,
				CustomType: durationType{},
				MarkdownDescription: fmt.Sprintf("How long a completed workflow's result is retained.\n\n"+
					"Valid only for a `%s` service. The provider checks the service's actual type and reports an "+
					"error rather than sending a setting the service cannot have.", admin.ServiceTypeWorkflow),
			},
			"inactivity_timeout": schema.StringAttribute{
				Optional:   true,
				CustomType: durationType{},
				MarkdownDescription: "How long Restate waits on a stalled invocation before asking it to suspend. " +
					"Paired with `abort_timeout`, which starts once this expires.",
			},
			"abort_timeout": schema.StringAttribute{
				Optional:   true,
				CustomType: durationType{},
				MarkdownDescription: "How long Restate waits after `inactivity_timeout` expires, and the invocation " +
					"has been asked to terminate gracefully, before aborting it.",
			},

			"service_type": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: fmt.Sprintf("The service's kind, from discovery: `%s`, `%s`, or `%s`.",
					admin.ServiceTypeService, admin.ServiceTypeVirtualObject, admin.ServiceTypeWorkflow),
			},
			"revision": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: "Current service revision. It changes when discovery finds a new schema, which " +
					"is a useful signal that these settings may have been overwritten.",
			},
			"deployment_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Deployment currently serving this service.",
			},
		},
	}
}

func (r *ServiceConfigurationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

// ValidateConfig rejects a configuration that manages nothing, which would otherwise apply cleanly and
// do nothing at all.
func (r *ServiceConfigurationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config serviceConfigurationModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.warnAboutUnparseableDurations(config, &resp.Diagnostics)

	if !r.managesAnything(config) {
		resp.Diagnostics.AddError("This restate_service_configuration manages nothing",
			"At least one of `public`, `idempotency_retention`, `journal_retention`, "+
				"`workflow_completion_retention`, `inactivity_timeout` or `abort_timeout` must be set.\n\n"+
				"This resource only sends the attributes you set -- it never sends a null -- so a resource with "+
				"none of them set would apply successfully and change nothing.")
	}
}

// managesAnything reports whether the configuration sets any manageable attribute.
//
// It counts an unknown value as set, unlike modifyRequest, which must not send one. The questions are
// different: validation asks whether the user wrote anything, and at validate time an attribute
// derived from another resource is unknown rather than null. Reusing modifyRequest here would reject a
// perfectly valid configuration.
// warnAboutUnparseableDurations reports a duration this provider cannot reduce to a length of time.
//
// It warns rather than rejects, for the same reason subscription URI schemes do: the server is the
// authority on what it accepts, and a calendar duration such as `P1M` is legitimate but has no fixed
// length. The consequence is worth saying out loud, though -- comparison falls back to exact string
// matching, so the server echoing the value back in another spelling shows as a permanent diff.
func (r *ServiceConfigurationResource) warnAboutUnparseableDurations(config serviceConfigurationModel, diags *diag.Diagnostics) {
	for _, field := range []struct {
		name  string
		value durationValue
	}{
		{"idempotency_retention", config.IdempotencyRetention},
		{"journal_retention", config.JournalRetention},
		{"workflow_completion_retention", config.WorkflowCompletionRetention},
		{"inactivity_timeout", config.InactivityTimeout},
		{"abort_timeout", config.AbortTimeout},
	} {
		if field.value.IsNull() || field.value.IsUnknown() {
			continue
		}
		_, err := ParseDuration(field.value.ValueString())
		if err == nil {
			continue
		}
		diags.AddAttributeWarning(path.Root(field.name),
			fmt.Sprintf("`%s` is not a duration this provider can compare", field.name),
			fmt.Sprintf("%s\n\n"+
				"The value is sent to Restate unchanged -- the server is the authority on what it accepts -- but "+
				"this provider cannot reduce it to a length of time, so it compares the configured and reported "+
				"values as exact strings. If Restate echoes it back in a different spelling, every plan will show "+
				"a diff.\n\n"+
				"Use a fixed-length form such as `30 days` or `P30D` to avoid that.", err.Error()))
	}
}

func (r *ServiceConfigurationResource) managesAnything(config serviceConfigurationModel) bool {
	if !config.Public.IsNull() {
		return true
	}
	for _, value := range []durationValue{
		config.IdempotencyRetention,
		config.JournalRetention,
		config.WorkflowCompletionRetention,
		config.InactivityTimeout,
		config.AbortTimeout,
	} {
		if !value.IsNull() {
			return true
		}
	}
	return false
}

func (r *ServiceConfigurationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireAdminClient(r.data, "restate_service_configuration", &resp.Diagnostics) {
		return
	}

	var plan serviceConfigurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.apply(ctx, plan, &resp.Diagnostics, func(state *serviceConfigurationModel) {
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	})
}

func (r *ServiceConfigurationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireAdminClient(r.data, "restate_service_configuration", &resp.Diagnostics) {
		return
	}

	var state serviceConfigurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	service, err := r.data.Admin.Services().Get(ctx, state.ServiceName.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			// The service is gone, so the deployment exposing it is gone. Drop the resource; the next plan
			// recreates it once the deployment is back.
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not read the Restate service", err.Error())
		return
	}

	r.applyService(&state, service)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ServiceConfigurationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireAdminClient(r.data, "restate_service_configuration", &resp.Diagnostics) {
		return
	}

	var plan, state serviceConfigurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// An attribute dropped from the configuration cannot be reverted.
	r.warnAboutAbandonedSettings(&resp.Diagnostics, plan, state)

	r.apply(ctx, plan, &resp.Diagnostics, func(updated *serviceConfigurationModel) {
		resp.Diagnostics.Append(resp.State.Set(ctx, updated)...)
	})
}

// Delete removes the resource from state without changing the service.
//
// This is the partial-management contract made explicit: there is nothing safe to send, and silently
// doing nothing would be misleading, so it warns.
func (r *ServiceConfigurationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceConfigurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddWarning("Service configuration was left applied on the Restate service",
		fmt.Sprintf("Terraform has stopped managing the configuration of service %q, but the settings it applied "+
			"are still in effect.\n\n"+
			"The Restate Admin API has no operation that resets a service setting to the value discovery gave it, "+
			"so there is nothing this provider can send to undo the change. To restore the SDK-discovered "+
			"defaults, re-register the deployment with `overwrite = true`.",
			state.ServiceName.ValueString()))
}

// ImportState takes the service name.
func (r *ServiceConfigurationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Expected a service name, for example `OrderService`.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("service_name"), req.ID)...)

	// Import records the identity and nothing else. Adopting the service's current settings would be wrong:
	// they include values that came from discovery rather than from anyone's configuration.
	resp.Diagnostics.AddWarning("An imported service configuration manages nothing until you configure it",
		fmt.Sprintf("Service %q has been imported, but no settings have been adopted: this resource manages only "+
			"the attributes your configuration sets, and the service's current values include whatever its SDK "+
			"reported at discovery.\n\n"+
			"Add the attributes you want to manage and apply. Anything you leave out stays unmanaged and is not "+
			"reverted. Read the service's current values with the `restate_service` data source if you want to "+
			"start from them.", req.ID))
}

// apply sends the modify request and writes the result through the supplied setter, so create and update
// share one implementation.
func (r *ServiceConfigurationResource) apply(ctx context.Context, plan serviceConfigurationModel, diags *diag.Diagnostics, set func(*serviceConfigurationModel)) {
	serviceName := plan.ServiceName.ValueString()

	// Read first, to check the service exists and to learn its type: sending
	// workflow_completion_retention to a non-workflow service earns a far less specific server error.
	service, err := r.data.Admin.Services().Get(ctx, serviceName)
	if err != nil {
		if api.IsNotFound(err) {
			diags.AddAttributeError(path.Root("service_name"), "Restate service not found",
				fmt.Sprintf("No service named %q has been discovered in this environment.\n\n"+
					"A service exists only once a deployment exposing it is registered. Add a `depends_on` for the "+
					"`restate_deployment` that exposes this service so Terraform applies them in order.",
					serviceName))
			return
		}
		diags.AddError("Could not read the Restate service before configuring it", err.Error())
		return
	}

	if !plan.WorkflowCompletionRetention.IsNull() && !service.Type.SupportsWorkflowCompletionRetention() {
		diags.AddAttributeError(path.Root("workflow_completion_retention"),
			"workflow_completion_retention applies only to a workflow service",
			fmt.Sprintf("Service %q is a %s, not a %s, so it has no workflow completion to retain. Remove this "+
				"attribute.", serviceName, service.Type, admin.ServiceTypeWorkflow))
		return
	}

	updated, err := r.data.Admin.Services().Update(ctx, serviceName, r.modifyRequest(plan))
	if err != nil {
		diags.AddError("Could not configure the Restate service", err.Error())
		return
	}

	// Keep the planned values and verify the API agrees; see reconcileString. Durations compare
	// semantically, so the API echoing `P7D` back for a configured `7 days` is agreement.
	state := plan
	r.applyComputed(&state, updated)
	r.verifyApplied(diags, serviceName, plan, updated)
	set(&state)
}

// verifyApplied reports a field the API accepted but did not apply, which would otherwise surface on
// the next plan as drift the provider could not explain.
func (r *ServiceConfigurationResource) verifyApplied(diags *diag.Diagnostics, serviceName string, plan serviceConfigurationModel, applied admin.Service) {
	if !plan.Public.IsNull() && applied.Public != nil && plan.Public.ValueBool() != *applied.Public {
		diags.AddAttributeError(path.Root("public"), "The Restate service did not apply the requested configuration",
			fmt.Sprintf("Service %q was asked to set `public` to %v, but the API reports %v.",
				serviceName, plan.Public.ValueBool(), *applied.Public))
	}

	for _, field := range []struct {
		name      string
		requested durationValue
		reported  string
	}{
		{"idempotency_retention", plan.IdempotencyRetention, applied.IdempotencyRetention},
		{"journal_retention", plan.JournalRetention, applied.JournalRetention},
		{"workflow_completion_retention", plan.WorkflowCompletionRetention, applied.WorkflowCompletionRetention},
		{"inactivity_timeout", plan.InactivityTimeout, applied.InactivityTimeout},
		{"abort_timeout", plan.AbortTimeout, applied.AbortTimeout},
	} {
		if field.requested.IsNull() || field.requested.IsUnknown() {
			continue
		}
		// An unreported value is not evidence of a discrepancy.
		if field.reported == "" || sameDuration(field.requested.ValueString(), field.reported) {
			continue
		}
		diags.AddAttributeError(path.Root(field.name), "The Restate service did not apply the requested configuration",
			fmt.Sprintf("Service %q was asked to set `%s` to %q, but the API reports %q, which is a different length of time.",
				serviceName, field.name, field.requested.ValueString(), field.reported))
	}
}

// sameDuration reports whether two spellings denote the same length of time, falling back to exact
// comparison for a form the parser does not understand.
func sameDuration(left, right string) bool {
	parsedLeft, leftErr := ParseDuration(left)
	parsedRight, rightErr := ParseDuration(right)
	if leftErr != nil || rightErr != nil {
		return left == right
	}
	return parsedLeft == parsedRight
}

// applyComputed copies only the server-derived fields, leaving the managed attributes as planned.
func (r *ServiceConfigurationResource) applyComputed(model *serviceConfigurationModel, service admin.Service) {
	model.ID = types.StringValue(service.Name)
	// service_name is Required and requires replacement, so it can only ever equal what was planned;
	// it is set from the response rather than reconciled because an import has no plan to keep.
	model.ServiceName = types.StringValue(service.Name)
	model.ServiceType = types.StringValue(string(service.Type))
	model.Revision = types.Int64Value(int64(service.Revision))
	model.DeploymentID = types.StringValue(service.DeploymentID)
}

// modifyRequest builds a modify request from the configuration, sending only what is set. An unset
// attribute is omitted rather than nulled, because the contract does not define what a null means.
func (r *ServiceConfigurationResource) modifyRequest(plan serviceConfigurationModel) admin.ModifyService {
	request := admin.ModifyService{}

	if !plan.Public.IsNull() && !plan.Public.IsUnknown() {
		value := plan.Public.ValueBool()
		request.Public = &value
	}
	for _, field := range []struct {
		value  durationValue
		target **string
	}{
		{plan.IdempotencyRetention, &request.IdempotencyRetention},
		{plan.JournalRetention, &request.JournalRetention},
		{plan.WorkflowCompletionRetention, &request.WorkflowCompletionRetention},
		{plan.InactivityTimeout, &request.InactivityTimeout},
		{plan.AbortTimeout, &request.AbortTimeout},
	} {
		if field.value.IsNull() || field.value.IsUnknown() {
			continue
		}
		value := field.value.ValueString()
		*field.target = &value
	}
	return request
}

// applyService copies the API's report into the model, for the refresh path.
//
// Managed attributes are overwritten so an out-of-band change shows as drift. Unmanaged ones are left
// null rather than set to the API's value, which is what keeps them out of the plan.
func (r *ServiceConfigurationResource) applyService(model *serviceConfigurationModel, service admin.Service) {
	r.applyComputed(model, service)

	if !model.Public.IsNull() {
		model.Public = optionalBool(service.Public)
	}
	if !model.IdempotencyRetention.IsNull() {
		model.IdempotencyRetention = durationValueOf(service.IdempotencyRetention)
	}
	if !model.JournalRetention.IsNull() {
		model.JournalRetention = durationValueOf(service.JournalRetention)
	}
	if !model.WorkflowCompletionRetention.IsNull() {
		model.WorkflowCompletionRetention = durationValueOf(service.WorkflowCompletionRetention)
	}
	if !model.InactivityTimeout.IsNull() {
		model.InactivityTimeout = durationValueOf(service.InactivityTimeout)
	}
	if !model.AbortTimeout.IsNull() {
		model.AbortTimeout = durationValueOf(service.AbortTimeout)
	}
}

// warnAboutAbandonedSettings reports an attribute removed from the configuration, because that stops
// management without reverting the applied value.
func (r *ServiceConfigurationResource) warnAboutAbandonedSettings(diags *diag.Diagnostics, plan, state serviceConfigurationModel) {
	abandoned := []struct {
		name             string
		nowUnset, wasSet bool
	}{
		{"public", plan.Public.IsNull(), !state.Public.IsNull()},
		{"idempotency_retention", plan.IdempotencyRetention.IsNull(), !state.IdempotencyRetention.IsNull()},
		{"journal_retention", plan.JournalRetention.IsNull(), !state.JournalRetention.IsNull()},
		{"workflow_completion_retention", plan.WorkflowCompletionRetention.IsNull(), !state.WorkflowCompletionRetention.IsNull()},
		{"inactivity_timeout", plan.InactivityTimeout.IsNull(), !state.InactivityTimeout.IsNull()},
		{"abort_timeout", plan.AbortTimeout.IsNull(), !state.AbortTimeout.IsNull()},
	}
	for _, candidate := range abandoned {
		if !candidate.nowUnset || !candidate.wasSet {
			continue
		}
		diags.AddAttributeWarning(path.Root(candidate.name),
			fmt.Sprintf("`%s` is no longer managed, and its applied value was left in place", candidate.name),
			fmt.Sprintf("`%s` was removed from the configuration. Terraform has stopped managing it, but the "+
				"value it previously applied is still in effect: the Restate Admin API has no operation that "+
				"resets a setting to its discovered value.\n\n"+
				"To change it, set it to the value you want. To restore the SDK-discovered default, re-register "+
				"the deployment with `overwrite = true`.", candidate.name))
	}
}
