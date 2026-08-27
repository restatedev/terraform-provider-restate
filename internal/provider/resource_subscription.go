package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin"
)

// SubscriptionResource manages a Kafka subscription.
type SubscriptionResource struct {
	data *providerData
}

var (
	_ resource.Resource                = (*SubscriptionResource)(nil)
	_ resource.ResourceWithConfigure   = (*SubscriptionResource)(nil)
	_ resource.ResourceWithImportState = (*SubscriptionResource)(nil)
)

// NewSubscriptionResource returns the resource factory.
func NewSubscriptionResource() resource.Resource { return &SubscriptionResource{} }

func (r *SubscriptionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription"
}

type subscriptionModel struct {
	ID      types.String `tfsdk:"id"`
	Source  types.String `tfsdk:"source"`
	Sink    types.String `tfsdk:"sink"`
	Options types.Map    `tfsdk:"options"`
}

func (r *SubscriptionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A subscription that drives a Restate handler from a Kafka topic.\n\n" +
			"The Admin API has no update operation for subscriptions, so **every change replaces** the " +
			"subscription. Replacement briefly stops consumption; the new subscription resumes from whatever " +
			"offset its `options` specify.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Subscription ID assigned by Restate, for example `sub_1a2b3c`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"source": schema.StringAttribute{
				Required: true,
				MarkdownDescription: fmt.Sprintf("Kafka topic to consume, as `%s://<cluster>/<topic>`, where "+
					"`<cluster>` is a `restate_kafka_cluster` name.\n\n"+
					"Changing it replaces the subscription. A scheme this provider's pinned contract does not "+
					"document is sent unchanged with a warning, rather than rejected, so a newer Restate server's "+
					"scheme is not blocked by the provider.", admin.KafkaSourceScheme),
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"sink": schema.StringAttribute{
				Required: true,
				MarkdownDescription: fmt.Sprintf("Handler to invoke, as `%s://<service>/<handler>`.\n\n"+
					"Changing it replaces the subscription.", admin.ServiceSinkScheme),
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"options": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Consumer options, such as `auto.offset.reset`. Passed through to the Kafka " +
					"consumer as strings.\n\n" +
					"Changing them replaces the subscription.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
		},
	}
}

func (r *SubscriptionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *SubscriptionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireAdminClient(r.data, "restate_subscription", &resp.Diagnostics) {
		return
	}

	var plan subscriptionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	source, sink := plan.Source.ValueString(), plan.Sink.ValueString()

	// Warn about a URI shape this snapshot does not document, but send it anyway: the server is the
	// authority on what it accepts, and a closed enum here would make the provider the reason a supported
	// configuration cannot be expressed.
	warnings, err := admin.ValidateSubscriptionURIs(source, sink)
	if err != nil {
		resp.Diagnostics.AddError("Invalid subscription source or sink", err.Error())
		return
	}
	appendURIWarnings(&resp.Diagnostics, warnings)

	options, diags := stringMap(ctx, plan.Options)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	subscription, err := r.data.Admin.Subscriptions().Create(ctx, admin.CreateSubscription{
		Source:  source,
		Sink:    sink,
		Options: options,
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not create the subscription",
			fmt.Sprintf("%s\n\nCheck that the Kafka cluster named in `source` exists and that the service and "+
				"handler named in `sink` have been discovered from a registered deployment.", err.Error()))
		return
	}

	// Keep the planned source, sink and options -- see reconcileString. The API is consulted only for the
	// identifier it assigned, and for a discrepancy worth reporting.
	state := plan
	state.ID = types.StringValue(subscription.ID)
	r.verifyCreated(&resp.Diagnostics, plan, subscription)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// verifyCreated reports a subscription the API created with different endpoints than were asked for,
// which would otherwise surface as an unexplained replacement on the next plan.
func (r *SubscriptionResource) verifyCreated(diags *diag.Diagnostics, plan subscriptionModel, created admin.Subscription) {
	for _, field := range []struct {
		name      string
		requested string
		reported  string
	}{
		{"source", plan.Source.ValueString(), created.Source},
		{"sink", plan.Sink.ValueString(), created.Sink},
	} {
		if field.reported == "" || field.reported == field.requested {
			continue
		}
		diags.AddAttributeError(path.Root(field.name), "The subscription was not created as requested",
			fmt.Sprintf("Subscription %s was asked for %s %q, but the API reports %q.",
				created.ID, field.name, field.requested, field.reported))
	}
}

func (r *SubscriptionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireAdminClient(r.data, "restate_subscription", &resp.Diagnostics) {
		return
	}

	var state subscriptionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	subscription, err := r.data.Admin.Subscriptions().Get(ctx, state.ID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not read the subscription", err.Error())
		return
	}

	resp.Diagnostics.Append(r.applySubscription(ctx, &state, subscription)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update exists only to satisfy the interface: every configurable attribute requires replacement, so
// Terraform never calls this.
func (r *SubscriptionResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Subscriptions cannot be updated in place",
		"The Restate Admin API has no update operation for subscriptions, so every change replaces the "+
			"subscription. Reaching this point means the provider's plan modifiers are wrong; please report it "+
			"as a bug.")
}

func (r *SubscriptionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireAdminClient(r.data, "restate_subscription", &resp.Diagnostics) {
		return
	}

	var state subscriptionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.data.Admin.Subscriptions().Delete(ctx, state.ID.ValueString(), struct{}{}); err != nil {
		resp.Diagnostics.AddError("Could not delete the subscription", err.Error())
	}
}

// ImportState takes the subscription ID.
func (r *SubscriptionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Expected a subscription ID, for example `sub_1a2b3c`.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// applySubscription reconciles state with the API's report, for the refresh path only; see Create.
func (r *SubscriptionResource) applySubscription(ctx context.Context, model *subscriptionModel, subscription admin.Subscription) diag.Diagnostics {
	var diags diag.Diagnostics

	model.ID = types.StringValue(subscription.ID)
	model.Source = types.StringValue(subscription.Source)
	model.Sink = types.StringValue(subscription.Sink)

	// Options need care in both directions.
	//
	// A subscription configured with none must keep a null map rather than gaining an empty one, or it
	// would never converge. And when options were configured but the API reports none, the configured
	// value is kept: every attribute forces replacement, so treating that as drift would replace the
	// subscription, get the same empty response back, and replace it again forever.
	if len(subscription.Options) == 0 {
		if model.Options.IsUnknown() {
			model.Options = types.MapNull(types.StringType)
		}
		return diags
	}
	model.Options = stringMapValue(ctx, subscription.Options, &diags)
	return diags
}

func appendURIWarnings(diags *diag.Diagnostics, warnings []admin.URIWarning) {
	for _, warning := range warnings {
		diags.AddAttributeWarning(path.Root(warning.Field),
			fmt.Sprintf("Unrecognised subscription %s", warning.Field), warning.Message)
	}
}
