package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

// cloudAccountPatterns are the contract's own constraints on a grant's cloud account.
var cloudAccountPatterns = map[cloud.CloudProvider]*regexp.Regexp{
	cloud.CloudAWS:   regexp.MustCompile(`^[0-9]{12}$`),
	cloud.CloudGCP:   regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`),
	cloud.CloudAzure: regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`),
}

// CloudRegionGrantResource manages a region grant: Restate's half of the access a customer grants in
// one cloud account, which builds any number of BYOC regions there.
type CloudRegionGrantResource struct {
	data *providerData
}

var (
	_ resource.Resource                   = (*CloudRegionGrantResource)(nil)
	_ resource.ResourceWithConfigure      = (*CloudRegionGrantResource)(nil)
	_ resource.ResourceWithImportState    = (*CloudRegionGrantResource)(nil)
	_ resource.ResourceWithValidateConfig = (*CloudRegionGrantResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*CloudRegionGrantResource)(nil)
)

// NewCloudRegionGrantResource returns the resource factory.
func NewCloudRegionGrantResource() resource.Resource { return &CloudRegionGrantResource{} }

func (r *CloudRegionGrantResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_region_grant"
}

type cloudRegionGrantModel struct {
	ID             types.String `tfsdk:"id"`
	AccountID      types.String `tfsdk:"account_id"`
	Owner          types.String `tfsdk:"owner"`
	OwnerID        types.String `tfsdk:"owner_id"`
	DisplayName    types.String `tfsdk:"display_name"`
	Cloud          types.String `tfsdk:"cloud"`
	CloudAccountID types.String `tfsdk:"cloud_account_id"`

	Status         types.String `tfsdk:"status"`
	StatusReason   types.String `tfsdk:"status_reason"`
	RestateIssuer  types.String `tfsdk:"restate_issuer"`
	GrantSubject   types.String `tfsdk:"grant_subject"`
	TokenAudience  types.String `tfsdk:"token_audience"`
	ResourcePrefix types.String `tfsdk:"resource_prefix"`
	RegionIDs      types.List   `tfsdk:"region_ids"`
	CreatedAt      types.String `tfsdk:"created_at"`
	ExpiresAt      types.String `tfsdk:"expires_at"`
}

func (r *CloudRegionGrantResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stable := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Managed BYOC region grant: access to one AWS account, GCP project or Azure " +
			"subscription that builds any number of BYOC regions there.\n\n" +
			"A grant has two halves. This resource is Restate's half. It answers the values the cloud half is " +
			"built from (`restate_issuer`, `grant_subject`, `token_audience` and `resource_prefix`), which go to " +
			"the grant module in [modules/byoc](https://github.com/restatedev/terraform-provider-restate/tree/main/modules/byoc), which can also create this resource and its confirmation itself. " +
			"`restate_cloud_region_grant_confirmation` then hands the module's output to Restate, which signs in " +
			"through it and marks the grant `READY`. Only then can `restate_cloud_byoc_region` build regions " +
			"with it.\n\n" +
			"A grant that is not confirmed within seven days expires. A refresh that finds the grant expired or " +
			"deleted removes it from the state, so the next apply creates a new one.\n\n" +
			"The API has no update operation, so changing any argument replaces the grant, and a grant that " +
			"regions still use cannot be deleted.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Grant ID assigned by Restate Cloud, for example `rgr_x5z2y4qjmt`.",
				PlanModifiers:       stable,
			},
			"account_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Account the grant was created through. For an `ORGANIZATION` grant this is " +
					"any account of the organization, and the grant's owner is `owner_id`.",
				PlanModifiers: stable,
			},
			"owner": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(string(cloud.RegionGrantOwnerAccount)),
				MarkdownDescription: "Who owns the grant. `ACCOUNT` (the default): the provider's account, whose " +
					"administrators and management keys manage it and build regions with it. `ORGANIZATION`: the " +
					"account's organization, whose administrators and organization API keys with role " +
					"`rst:role::OrganizationRegionGrantAccess` manage it and build regions with it in any of its " +
					"accounts. An account's management key cannot manage an organization's grant.",
				Validators: []validator.String{stringvalidator.OneOf(
					string(cloud.RegionGrantOwnerAccount), string(cloud.RegionGrantOwnerOrganization))},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"owner_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the account or organization that owns the grant.",
				PlanModifiers:       stable,
			},
			"display_name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Name shown for the grant. Restate chooses one when it is omitted. A live grant " +
					"for the same cloud account keeps its name, so asking for another one is refused.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 100)},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cloud": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Cloud the grant is in: `aws`, `gcp` or `azure`.",
				Validators:          []validator.String{stringvalidator.OneOf(cloudProviderNames()...)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"cloud_account_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The AWS account ID, GCP project ID or Azure subscription ID the grant is " +
					"in. Every region built with the grant runs there. Restate confirms only one grant for each " +
					"cloud account.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},

			"status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "`PENDING` until the grant is confirmed, then `READY`. A grant that expired " +
					"or was deleted is removed from the state on refresh.",
			},
			"status_reason": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "While the grant is `PENDING`: `AWAITING_CONFIRMATION`, `CONFIRMING` while Restate signs " +
					"in through the cloud grant, or `CONFIRMATION_FAILED` when the cloud kept refusing. Empty once " +
					"the grant is `READY`.",
			},
			"restate_issuer": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Restate's OIDC issuer, which signs the grant's token. The grant module's `restate_issuer`.",
				PlanModifiers:       stable,
			},
			"grant_subject": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Subject of the grant's token, `grant:<grant ID>`. The grant module's `grant_subject`.",
				PlanModifiers:       stable,
			},
			"token_audience": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Audience of the grant's token. The grant module's `token_audience`.",
				PlanModifiers:       stable,
			},
			"resource_prefix": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Prefix of every resource name a region of the grant has in the cloud " +
					"account. The grant module's `resource_prefix`.",
				PlanModifiers: stable,
			},
			"region_ids": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Regions built with the grant that are not deleted yet.",
				PlanModifiers:       []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the grant was created.",
				PlanModifiers:       stable,
			},
			"expires_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "While the grant is `PENDING`: when it expires unless it is confirmed. Empty once it is confirmed.",
			},
		},
	}
}

func (r *CloudRegionGrantResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *CloudRegionGrantResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config cloudRegionGrantModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateCloudAccount(config.Cloud, config.CloudAccountID, path.Root("cloud_account_id"), &resp.Diagnostics)
}

func (r *CloudRegionGrantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireRegionGrantClient(r.data, "restate_cloud_region_grant", &resp.Diagnostics) {
		return
	}

	var plan cloudRegionGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	provider := cloud.CloudProvider(plan.Cloud.ValueString())
	grant, err := r.data.Cloud.RegionGrants().Create(ctx, cloud.CreateRegionGrantInput{
		Owner:       cloud.RegionGrantOwner(plan.Owner.ValueString()),
		DisplayName: plan.DisplayName.ValueString(),
		Cloud:       provider,
		Account:     cloudAccountFor(provider, plan.CloudAccountID.ValueString()),
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not create the Restate Cloud region grant", err.Error())
		return
	}

	// Restate answers the owner's live grant for the cloud account when one exists, and this resource
	// now manages it: destroying the resource deletes that grant.
	if grant.Status != cloud.RegionGrantPending || grant.StatusReason != "" && grant.StatusReason != cloud.GrantAwaitingConfirmation || len(grant.RegionIDs) > 0 {
		resp.Diagnostics.AddWarning("Adopted an existing Restate Cloud region grant",
			fmt.Sprintf("The owner already had region grant %s (%s, regions %v) for %s account %s, and Restate answered it "+
				"instead of creating another one. This resource now manages it, and destroying the resource deletes it. "+
				"If another configuration manages the grant, remove this resource from the state and use the "+
				"restate_cloud_region_grant data source here instead.",
				grant.ID, grant.Status, grant.RegionIDs, grant.Cloud, grant.Account.ID()))
	}

	state := plan
	r.applyGrant(&state, grant, applyWrite, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ModifyPlan warns before a replacement. A grant has no update operation, so a change to any argument
// replaces it, and the regions built with it in this configuration are rebuilt with the new grant.
func (r *CloudRegionGrantResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan cloudRegionGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !plan.ID.IsUnknown() {
		return
	}
	var regions []string
	resp.Diagnostics.Append(state.RegionIDs.ElementsAs(ctx, &regions, false)...)
	if len(regions) == 0 {
		return
	}
	resp.Diagnostics.AddWarning("Replacing a region grant that regions use",
		fmt.Sprintf("Region grant %s cannot be changed in place, so this plan replaces it. Regions %v use it. A region "+
			"keeps its grant for its whole life, so a region this configuration manages is deleted and built again with "+
			"the new grant, along with everything in it, unless it has deletion_protection. Restate refuses to delete "+
			"the grant while any other region uses it.", state.ID.ValueString(), regions))
}

func (r *CloudRegionGrantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireRegionGrantClient(r.data, "restate_cloud_region_grant", &resp.Diagnostics) {
		return
	}

	var state cloudRegionGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	grant, err := r.data.Cloud.RegionGrants().GetLive(ctx, state.ID.ValueString())
	if err != nil {
		if errors.Is(err, cloud.ErrRegionGrantGone) {
			if grant.ID == "" && !accountMatches(r.data, state.AccountID, &resp.Diagnostics, "region grant", state.ID.ValueString()) {
				return
			}
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not read the Restate Cloud region grant", err.Error())
		return
	}

	r.applyGrant(&state, grant, applyRead, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update exists only to satisfy the interface: every argument requires replacement.
func (r *CloudRegionGrantResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Restate Cloud region grants cannot be updated in place",
		"The Cloud Management API has no update operation for region grants, so every change replaces the grant. "+
			"Reaching this point means the provider's plan modifiers are wrong; please report it as a bug.")
}

// Delete deletes a grant that the confirmation has not already deleted. That is a grant that was
// never confirmed, or one whose confirmation is managed somewhere else.
func (r *CloudRegionGrantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireRegionGrantClient(r.data, "restate_cloud_region_grant", &resp.Diagnostics) {
		return
	}

	var state cloudRegionGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	deleteRegionGrant(ctx, r.data.Cloud, state.ID.ValueString(), "Could not delete the Restate Cloud region grant", &resp.Diagnostics)
}

// ImportState accepts "<account ID>/<grant ID>". For an organization's grant the account is any account
// of the organization that the provider is configured for.
func (r *CloudRegionGrantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	fields, ok := splitImportID(req.ID, 2)
	if !ok {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected `<account ID>/<grant ID>`, for example `acc_1a2b3c/rgr_x5z2y4qjmt`, but got %q.", req.ID))
		return
	}
	accountID, grantID := fields[0], fields[1]

	if !requireRegionGrantClient(r.data, "restate_cloud_region_grant", &resp.Diagnostics) {
		return
	}
	if configured := r.data.Cloud.AccountID(); accountID != configured {
		resp.Diagnostics.AddError("Import ID does not match the configured account",
			fmt.Sprintf("The import ID names account %s, but this provider is configured for account %s. "+
				"Use a provider alias configured for the right account, or correct the import ID.", accountID, configured))
		return
	}

	grant, err := r.data.Cloud.RegionGrants().GetLive(ctx, grantID)
	if err != nil {
		resp.Diagnostics.AddError("Could not import the Restate Cloud region grant", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), grant.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), accountID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner"), string(grant.Owner))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cloud"), string(grant.Cloud))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cloud_account_id"), grant.Account.ID())...)
}

func (r *CloudRegionGrantResource) applyGrant(model *cloudRegionGrantModel, grant cloud.RegionGrant, mode applyMode, diags *diag.Diagnostics) {
	subject := fmt.Sprintf("Region grant %s", grant.ID)

	model.ID = types.StringValue(grant.ID)
	if model.AccountID.IsNull() || model.AccountID.IsUnknown() {
		model.AccountID = types.StringValue(r.data.Cloud.AccountID())
	}
	model.Owner = reconcileString(mode, model.Owner, string(grant.Owner), path.Root("owner"), subject, diags)
	model.OwnerID = types.StringValue(grant.OwnerID)
	model.DisplayName = reconcileString(mode, model.DisplayName, grant.DisplayName, path.Root("display_name"), subject, diags)
	model.Cloud = reconcileString(mode, model.Cloud, string(grant.Cloud), path.Root("cloud"), subject, diags)
	model.CloudAccountID = reconcileCloudAccount(mode, model.CloudAccountID, grant.Cloud, grant.Account.ID(), subject, diags)

	model.Status = types.StringValue(string(grant.Status))
	model.StatusReason = types.StringValue(grant.StatusReason)
	model.CreatedAt = types.StringValue(grant.CreatedAt)
	model.ExpiresAt = types.StringValue(grant.ExpiresAt)
	if grant.Inputs != nil {
		model.RestateIssuer = types.StringValue(grant.Inputs.RestateIssuer)
		model.GrantSubject = types.StringValue(grant.Inputs.GrantSubject)
		model.TokenAudience = types.StringValue(grant.Inputs.TokenAudience)
		model.ResourcePrefix = types.StringValue(grant.Inputs.ResourcePrefix)
	} else if mode == applyWrite {
		diags.AddError("Restate Cloud answered no grant inputs",
			fmt.Sprintf("%s was created, but the API did not answer the values the cloud grant is built from. "+
				"The grant has been recorded in state so it can be destroyed.", subject))
	}
	model.RegionIDs = stringList(grant.RegionIDs)
}

// deleteRegionGrant deletes a grant and explains the one refusal an operator can act on.
func deleteRegionGrant(ctx context.Context, client *cloud.Client, grantID, summary string, diags *diag.Diagnostics) {
	err := client.RegionGrants().Delete(ctx, grantID, struct{}{})
	if err == nil {
		return
	}
	detail := err.Error()
	if grant, getErr := client.RegionGrants().Get(ctx, grantID); getErr == nil && len(grant.RegionIDs) > 0 {
		detail = fmt.Sprintf("Regions still use region grant %s: %v. Delete them first, then destroy again. "+
			"The cloud side of the grant is still in place, which Restate needs to delete those regions.\n\n%s",
			grantID, grant.RegionIDs, err.Error())
	}
	diags.AddError(summary, detail)
}

// validateCloudAccount checks a cloud account ID against the contract's pattern for its cloud. It skips
// values that are not known yet.
func validateCloudAccount(provider, accountID types.String, at path.Path, diags *diag.Diagnostics) {
	if provider.IsNull() || provider.IsUnknown() || accountID.IsNull() || accountID.IsUnknown() {
		return
	}
	pattern, ok := cloudAccountPatterns[cloud.CloudProvider(provider.ValueString())]
	if !ok || pattern.MatchString(accountID.ValueString()) {
		return
	}
	kind := map[string]string{"aws": "an AWS account ID of 12 digits", "gcp": "a GCP project ID", "azure": "an Azure subscription ID (a UUID)"}
	diags.AddAttributeError(at, "Invalid cloud account ID",
		fmt.Sprintf("%q is not %s.", accountID.ValueString(), kind[provider.ValueString()]))
}

// reconcileCloudAccount compares Azure subscriptions without regard to case, as the API does.
func reconcileCloudAccount(mode applyMode, planned types.String, provider cloud.CloudProvider, reported, subject string, diags *diag.Diagnostics) types.String {
	if provider == cloud.CloudAzure && !planned.IsNull() && !planned.IsUnknown() && strings.EqualFold(planned.ValueString(), reported) {
		return planned
	}
	return reconcileString(mode, planned, reported, path.Root("cloud_account_id"), subject, diags)
}

func cloudAccountFor(provider cloud.CloudProvider, id string) cloud.CloudAccount {
	switch provider {
	case cloud.CloudAWS:
		return cloud.CloudAccount{AWSAccountID: id}
	case cloud.CloudGCP:
		return cloud.CloudAccount{GCPProjectID: id}
	default:
		return cloud.CloudAccount{AzureSubscriptionID: id}
	}
}

func cloudProviderNames() []string {
	names := make([]string, 0, len(cloud.CloudProviders()))
	for _, provider := range cloud.CloudProviders() {
		names = append(names, string(provider))
	}
	return names
}

func stringList(values []string) types.List {
	elements := make([]attr.Value, 0, len(values))
	for _, value := range values {
		elements = append(elements, types.StringValue(value))
	}
	return types.ListValueMust(types.StringType, elements)
}

// accountMatches guards a removal from the state on a 404. Restate answers 404 both for a grant that
// is gone and for one that another account owns, so a provider pointed at another account would
// otherwise forget a live grant, and a later destroy could remove its cloud side while Restate still
// uses it.
func accountMatches(data *providerData, recorded types.String, diags *diag.Diagnostics, kind, id string) bool {
	configured := data.Cloud.AccountID()
	if recorded.IsNull() || recorded.IsUnknown() || recorded.ValueString() == "" || recorded.ValueString() == configured {
		return true
	}
	diags.AddError(fmt.Sprintf("Restate Cloud %s not found through this account", kind),
		fmt.Sprintf("Restate Cloud did not find %s %s through account %s, but it was created through account %s. "+
			"Point the provider at that account, or another account of the same organization for an organization's "+
			"grant, rather than letting Terraform forget a grant that may still exist.", kind, id, configured, recorded.ValueString()))
	return false
}
