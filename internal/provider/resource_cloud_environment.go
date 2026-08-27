package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

// Provisioning and teardown are server-side operations whose duration the provider does not control, so
// these defaults are generous. They are estimates, not measurements.
const (
	defaultEnvironmentCreateTimeout = 30 * time.Minute
	defaultEnvironmentUpdateTimeout = 15 * time.Minute
	defaultEnvironmentDeleteTimeout = 30 * time.Minute
)

// environmentNamePattern is the contract's own constraint on an environment name.
var environmentNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

// CloudEnvironmentResource manages a Restate Cloud environment.
type CloudEnvironmentResource struct {
	data *providerData
}

var (
	_ resource.Resource                = (*CloudEnvironmentResource)(nil)
	_ resource.ResourceWithConfigure   = (*CloudEnvironmentResource)(nil)
	_ resource.ResourceWithImportState = (*CloudEnvironmentResource)(nil)
)

// NewCloudEnvironmentResource returns the resource factory.
func NewCloudEnvironmentResource() resource.Resource { return &CloudEnvironmentResource{} }

func (r *CloudEnvironmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_environment"
}

type cloudEnvironmentModel struct {
	ID        types.String `tfsdk:"id"`
	AccountID types.String `tfsdk:"account_id"`
	Name      types.String `tfsdk:"name"`
	Region    types.String `tfsdk:"region"`
	TierID    types.String `tfsdk:"tier_id"`

	DeletionProtection types.Bool `tfsdk:"deletion_protection"`

	Status               types.String `tfsdk:"status"`
	RegionDisplayName    types.String `tfsdk:"region_display_name"`
	SigningPublicKey     types.String `tfsdk:"signing_public_key"`
	IngressBaseURL       types.String `tfsdk:"ingress_base_url"`
	AdminBaseURL         types.String `tfsdk:"admin_base_url"`
	InvocationMetricsURL types.String `tfsdk:"invocation_metrics_url"`
	TunnelBaseURL        types.String `tfsdk:"tunnel_base_url"`
	TunnelSRV            types.String `tfsdk:"tunnel_srv"`
	ProxyBaseURL         types.String `tfsdk:"proxy_base_url"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func (r *CloudEnvironmentResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Restate Cloud environment.\n\n" +
			"Creation is asynchronous: the API acknowledges the request and provisions in the background, so the " +
			"provider polls until the environment is `ACTIVE`. A failed provision leaves the environment in place " +
			"and reports its ID, so it can be inspected or destroyed.\n\n" +
			"The connection metadata this resource exports -- notably `admin_base_url` -- cannot configure a " +
			"provider in the same apply, because Terraform must configure a provider before any resource exists. " +
			"See the two-stage bootstrap guide.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Environment ID assigned by Restate Cloud, for example `env_1a2b3c`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"account_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Account the environment belongs to. It is recorded so a later plan can " +
					"detect a provider pointed at a different account, and it is the first half of the import ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Environment name. Lowercase letters, digits and hyphens, 2 to 63 characters, " +
					"not starting or ending with a hyphen. Renaming is applied in place.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(environmentNamePattern,
						"must be 2 to 63 characters of lowercase letters, digits and hyphens, and must not start or end with a hyphen"),
				},
			},
			"region": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Region to place the environment in, for example `us-east-2`. When omitted, " +
					"Restate Cloud picks the account's default and the provider records it, so later plans " +
					"converge.\n\n" +
					"Changing it replaces the environment: the API has no migration operation, so there is no way " +
					"to move an environment between regions. Use `restate_cloud_regions` to see what the account " +
					"may use.",
				PlanModifiers: []planmodifier.String{
					// RequiresReplaceIfConfigured, not RequiresReplace: with an unconfigured region the
					// value comes from state, and a plain RequiresReplace would see the null-to-known
					// transition as a change and replace a healthy environment.
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"tier_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: fmt.Sprintf(
					"Environment tier: one of %s. When omitted, Restate Cloud applies its default and the provider "+
						"records it.\n\n"+
						"`%s` is readable but cannot be requested: a custom tier is selected through an "+
						"operator-configured profile, which the API does not report, so a custom-tier environment "+
						"could not be refreshed without hidden drift.\n\n"+
						"A tier change on a Stripe-billed account may require completing a browser payment flow, "+
						"which Terraform cannot do. The apply fails with an actionable error rather than reporting "+
						"success; check `restate_cloud_account.billing_mode` first.",
					quotedTiers(), cloud.TierCustom),
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{stringvalidator.OneOf(requestableTierNames()...)},
			},
			"deletion_protection": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When true, `terraform destroy` refuses to delete this environment. Destroying " +
					"an environment is irreversible and takes its data with it, so guard production environments " +
					"with this and clear it deliberately when you mean to tear one down.",
			},

			"status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Provisioning status: `PENDING`, `ACTIVE`, `FAILED`, or `DELETED`. A " +
					"successful apply always leaves this `ACTIVE`; it is exported for diagnostics.",
			},
			"region_display_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Human-readable region label. Empty when the API reports none.",
			},
			"signing_public_key": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Public key services use to verify that a request came from this environment. " +
					"Pass it to your service deployments so they can authenticate Restate.",
			},
			"ingress_base_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Base URL for invoking services in this environment.",
			},
			"admin_base_url": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Base URL of this environment's Admin API. Use it as the `admin_url` of a " +
					"provider configured in a later stage; it cannot configure a provider in the same apply.",
			},
			"invocation_metrics_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "URL for this environment's invocation metrics. Empty when the API reports none.",
			},
			"tunnel_base_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Base URL of the private tunnel service for this environment.",
			},
			"tunnel_srv": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "SRV record the tunnel client resolves to reach this environment.",
			},
			"proxy_base_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Base URL of the proxy service for this environment.",
			},
		},
		Blocks: map[string]schema.Block{
			// Only the operations the resource actually honours are offered. A read timeout would be
			// advertised and then ignored: refresh is a single bounded request governed by the
			// provider's request_timeout, not a convergence loop.
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *CloudEnvironmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *CloudEnvironmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireCloudClient(r.data, "restate_cloud_environment", &resp.Diagnostics) {
		return
	}

	var plan cloudEnvironmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultEnvironmentCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	environments := r.data.Cloud.Environments()
	created, err := environments.Create(ctx, cloud.CreateEnvironmentInput{
		Name:   plan.Name.ValueString(),
		Region: plan.Region.ValueString(),
		Tier:   cloud.Tier(plan.TierID.ValueString()),
	})
	if err != nil {
		// An environment may exist that Terraform does not know about. Record the ID so the next apply can
		// destroy it.
		r.recordOrphan(ctx, created.ID, resp, err)
		return
	}

	// The create response is an acknowledgement, not proof of provisioning. Keep the ID in state if the
	// wait fails, so the environment is not orphaned.
	environment, err := environments.WaitUntilActive(ctx, created.ID)
	if err != nil {
		message := fmt.Sprintf("Environment %s was created but did not become active.\n\n%s\n\n"+
			"The environment ID has been recorded in state, so `terraform destroy` can remove it. "+
			"Check the Restate Cloud console before retrying.", created.ID, err.Error())
		if errors.Is(err, api.ErrPollTimeout) {
			message += fmt.Sprintf("\n\nIf provisioning is simply slow, raise the create timeout:\n\n"+
				"    timeouts {\n      create = \"%s\"\n    }", 2*timeout)
		}
		r.recordOrphanMessage(ctx, created.ID, resp, "Restate Cloud environment did not finish provisioning", message)
		return
	}

	state := plan
	r.applyEnvironment(&state, environment, applyWrite, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *CloudEnvironmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireCloudClient(r.data, "restate_cloud_environment", &resp.Diagnostics) {
		return
	}

	var state cloudEnvironmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	environment, err := r.data.Cloud.Environments().GetExisting(ctx, state.ID.ValueString())
	if err != nil {
		// An environment deleted out of band, by either of the API's two ways of saying so.
		if errors.Is(err, cloud.ErrEnvironmentGone) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not read the Restate Cloud environment", err.Error())
		return
	}

	if diags := r.checkAccount(state, environment); diags != nil {
		resp.Diagnostics.Append(diags...)
		return
	}

	r.applyEnvironment(&state, environment, applyRead, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *CloudEnvironmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireCloudClient(r.data, "restate_cloud_environment", &resp.Diagnostics) {
		return
	}

	var plan, state cloudEnvironmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, defaultEnvironmentUpdateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	environmentID := state.ID.ValueString()
	environments := r.data.Cloud.Environments()

	// Rename and tier change are separate operations with different semantics, applied separately and each
	// skipped when it would be a no-op.
	if plan.Name.ValueString() != state.Name.ValueString() {
		if err := environments.Rename(ctx, environmentID, plan.Name.ValueString()); err != nil {
			resp.Diagnostics.AddError("Could not rename the Restate Cloud environment", err.Error())
			return
		}
	}

	if !plan.TierID.IsUnknown() && plan.TierID.ValueString() != state.TierID.ValueString() {
		err := environments.SetTier(ctx, environmentID, cloud.Tier(plan.TierID.ValueString()))
		if cloud.IsInteractiveCheckoutRequired(err) {
			// The tier was not applied, and a rename in the same update may have been. Re-read so state reflects
			// reality rather than the plan.
			resp.Diagnostics.AddAttributeError(path.Root("tier_id"),
				"Restate Cloud tier change requires interactive payment", err.Error())
			r.refreshAfterPartialUpdate(ctx, &state, environmentID, resp)
			return
		}
		if err != nil {
			resp.Diagnostics.AddError("Could not change the Restate Cloud environment tier", err.Error())
			r.refreshAfterPartialUpdate(ctx, &state, environmentID, resp)
			return
		}
	}

	// Re-read: a tier change can alter connection metadata.
	environment, err := environments.Get(ctx, environmentID)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the Restate Cloud environment after updating it", err.Error())
		return
	}

	updated := plan
	r.applyEnvironment(&updated, environment, applyWrite, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &updated)...)
}

func (r *CloudEnvironmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireCloudClient(r.data, "restate_cloud_environment", &resp.Diagnostics) {
		return
	}

	var state cloudEnvironmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Restate Cloud environment is protected from deletion",
			fmt.Sprintf("Environment %s (%s) has `deletion_protection = true`, so it was not destroyed.\n\n"+
				"Destroying an environment is irreversible and takes its data with it. To proceed, set "+
				"`deletion_protection = false`, apply that change, and then destroy.",
				state.Name.ValueString(), state.ID.ValueString()))
		return
	}

	timeout, diags := state.Timeouts.Delete(ctx, defaultEnvironmentDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	environmentID := state.ID.ValueString()
	environments := r.data.Cloud.Environments()

	if err := environments.Delete(ctx, environmentID, cloud.DestroyOptions{}); err != nil {
		resp.Diagnostics.AddError("Could not destroy the Restate Cloud environment", err.Error())
		return
	}

	// Destruction is asynchronous, so returning here would let a dependent resource -- or a re-created
	// environment with the same name -- race the teardown.
	if err := environments.WaitUntilDeleted(ctx, environmentID); err != nil {
		resp.Diagnostics.AddError("Restate Cloud environment did not finish being destroyed",
			fmt.Sprintf("Environment %s was accepted for deletion but did not reach a deleted state.\n\n%s\n\n"+
				"The environment may still be being torn down. Re-run `terraform destroy` to continue waiting.",
				environmentID, err.Error()))
		return
	}
}

// ImportState accepts "<account ID>/<environment ID>".
//
// The account ID is part of the identity rather than taken from the provider, so importing into a
// provider configured for a different account is caught here.
func (r *CloudEnvironmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	accountID, environmentID, ok := splitImportID(req.ID, 2)
	if !ok {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected `<account ID>/<environment ID>`, for example `acc_1a2b3c/env_1a2b3c`, but got %q.", req.ID))
		return
	}

	if !requireCloudClient(r.data, "restate_cloud_environment", &resp.Diagnostics) {
		return
	}
	if configured := r.data.Cloud.AccountID(); accountID != configured {
		resp.Diagnostics.AddError("Import ID does not match the configured account",
			fmt.Sprintf("The import ID names account %s, but this provider is configured for account %s. "+
				"Use a provider alias configured for the right account, or correct the import ID.", accountID, configured))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), environmentID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), accountID)...)
	// Deletion protection has no server-side representation, so an import cannot discover it. The schema
	// default keeps the first plan after import empty; a protected environment sets it in configuration.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), false)...)
}

// applyEnvironment copies API state into the model. See reconcileString for why a write and a refresh
// treat the configured attributes differently.
func (r *CloudEnvironmentResource) applyEnvironment(model *cloudEnvironmentModel, environment cloud.Environment, mode applyMode, diags *diag.Diagnostics) {
	subject := fmt.Sprintf("Environment %s", environment.ID)

	model.ID = types.StringValue(environment.ID)
	model.AccountID = types.StringValue(r.data.Cloud.AccountID())
	model.Name = reconcileString(mode, model.Name, environment.Name, path.Root("name"), subject, diags)
	// Region and tier are Optional+Computed: unconfigured, the plan leaves them unknown and the API's choice
	// is adopted so later plans converge; configured, they must come back as asked.
	model.Region = reconcileString(mode, model.Region, environment.Region, path.Root("region"), subject, diags)
	model.TierID = reconcileString(mode, model.TierID, environment.Tier.String(), path.Root("tier_id"), subject, diags)

	model.Status = types.StringValue(string(environment.Status))
	model.RegionDisplayName = types.StringValue(environment.RegionDisplayName)
	model.SigningPublicKey = types.StringValue(environment.SigningPublicKey)
	model.IngressBaseURL = types.StringValue(environment.IngressBaseURL)
	model.AdminBaseURL = types.StringValue(environment.AdminBaseURL)
	model.InvocationMetricsURL = types.StringValue(environment.InvocationMetricsURL)
	model.TunnelBaseURL = types.StringValue(environment.TunnelBaseURL)
	model.TunnelSRV = types.StringValue(environment.TunnelSRV)
	model.ProxyBaseURL = types.StringValue(environment.ProxyBaseURL)

	// deletion_protection has no server-side representation, so whatever the model already holds -- from
	// plan or prior state -- is left alone.
}

// checkAccount catches a provider repointed at a different account. Refreshing an environment from
// account A against account B could adopt a same-named environment that is not the one in state.
func (r *CloudEnvironmentResource) checkAccount(state cloudEnvironmentModel, environment cloud.Environment) diag.Diagnostics {
	recorded := state.AccountID.ValueString()
	configured := r.data.Cloud.AccountID()
	if recorded == "" || recorded == configured {
		return nil
	}

	var diags diag.Diagnostics
	diags.AddError("Restate Cloud environment belongs to a different account",
		fmt.Sprintf("Environment %s was created in account %s, but this provider is configured for account %s.\n\n"+
			"Point the provider at the original account, or use a provider alias for it. Refreshing against the "+
			"wrong account could adopt a different environment that happens to share this ID.",
			environment.ID, recorded, configured))
	return diags
}

// recordOrphan saves an environment ID that exists server-side but whose creation failed, so Terraform
// can destroy it rather than leaving it for someone to find.
func (r *CloudEnvironmentResource) recordOrphan(ctx context.Context, environmentID string, resp *resource.CreateResponse, err error) {
	summary := "Could not create the Restate Cloud environment"
	if cloud.IsInteractiveCheckoutRequired(err) {
		summary = "Restate Cloud environment requires interactive payment"
	}
	r.recordOrphanMessage(ctx, environmentID, resp, summary, err.Error())
}

func (r *CloudEnvironmentResource) recordOrphanMessage(ctx context.Context, environmentID string, resp *resource.CreateResponse, summary, detail string) {
	if environmentID != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), environmentID)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), r.data.Cloud.AccountID())...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), false)...)
	}
	resp.Diagnostics.AddError(summary, detail)
}

// refreshAfterPartialUpdate re-reads an environment whose update failed part way through, so state
// records what was applied rather than what was planned.
func (r *CloudEnvironmentResource) refreshAfterPartialUpdate(ctx context.Context, state *cloudEnvironmentModel, environmentID string, resp *resource.UpdateResponse) {
	environment, err := r.data.Cloud.Environments().Get(ctx, environmentID)
	if err != nil {
		// The original failure is already reported; this only adds context.
		resp.Diagnostics.AddWarning("Could not re-read the Restate Cloud environment after a partial update",
			fmt.Sprintf("State for environment %s may not reflect what was applied. Run `terraform refresh`.\n\n%s",
				environmentID, err.Error()))
		return
	}
	r.applyEnvironment(state, environment, applyRead, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// splitImportID splits a two-part slash-separated import ID, requiring both parts non-empty.
func splitImportID(id string, parts int) (string, string, bool) {
	fields := strings.Split(id, "/")
	if len(fields) != parts {
		return "", "", false
	}
	for _, field := range fields {
		if strings.TrimSpace(field) == "" {
			return "", "", false
		}
	}
	if parts == 2 {
		return fields[0], fields[1], true
	}
	return "", "", false
}

func quotedTiers() string {
	names := make([]string, 0, len(cloud.RequestableTiers()))
	for _, tier := range cloud.RequestableTiers() {
		names = append(names, "`"+tier.String()+"`")
	}
	return strings.Join(names, ", ")
}

func requestableTierNames() []string {
	names := make([]string, 0, len(cloud.RequestableTiers()))
	for _, tier := range cloud.RequestableTiers() {
		names = append(names, tier.String())
	}
	return names
}
