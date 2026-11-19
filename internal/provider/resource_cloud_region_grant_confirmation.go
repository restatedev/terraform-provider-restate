package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
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
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

// Cloud IAM changes take seconds to spread, and documents no upper bound. A confirmation keeps trying
// for this long before it reports the cloud's last refusal.
const defaultGrantConfirmationCreateTimeout = 5 * time.Minute

// CloudRegionGrantConfirmationResource hands the cloud half of a region grant to Restate. Restate signs
// in through it, and the grant becomes READY.
type CloudRegionGrantConfirmationResource struct {
	data *providerData
}

var (
	_ resource.Resource                = (*CloudRegionGrantConfirmationResource)(nil)
	_ resource.ResourceWithConfigure   = (*CloudRegionGrantConfirmationResource)(nil)
	_ resource.ResourceWithImportState = (*CloudRegionGrantConfirmationResource)(nil)
)

// NewCloudRegionGrantConfirmationResource returns the resource factory.
func NewCloudRegionGrantConfirmationResource() resource.Resource {
	return &CloudRegionGrantConfirmationResource{}
}

func (r *CloudRegionGrantConfirmationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_region_grant_confirmation"
}

type cloudRegionGrantConfirmationModel struct {
	ID          types.String   `tfsdk:"id"`
	AccountID   types.String   `tfsdk:"account_id"`
	GrantID     types.String   `tfsdk:"grant_id"`
	Grant       types.Map      `tfsdk:"grant"`
	Status      types.String   `tfsdk:"status"`
	ConfirmedAt types.String   `tfsdk:"confirmed_at"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

func (r *CloudRegionGrantConfirmationResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Confirms a `restate_cloud_region_grant` with the output of the grant module in " +
			"[modules/byoc](https://github.com/restatedev/terraform-provider-restate/tree/main/modules/byoc). Restate " +
			"signs in through the cloud grant and checks that it acts in the grant's cloud account, and the grant " +
			"becomes `READY`.\n\n" +
			"Restate signs in through the cloud grant in the background, and keeps trying for 20 minutes while " +
			"the cloud spreads a new identity. The apply waits until the grant is `READY`, and fails with the " +
			"cloud's answer when Restate gives up (`CONFIRMATION_FAILED`). When the create timeout passes first, " +
			"the apply fails with the cloud's last answer, and the next apply keeps waiting on the same " +
			"sign-in.\n\n" +
			"**Destroying this resource deletes the region grant at Restate.** Terraform destroys it before the " +
			"grant module, so Restate stops using the cloud grant before it is removed. Restate refuses while a " +
			"region still uses the grant, which stops the destroy and keeps the cloud grant in place, including " +
			"for regions that other configurations built with an organization's grant.\n\n" +
			"A change to `grant` confirms the grant again in place. Restate accepts the output it confirmed and " +
			"refuses any other, because regions built with the grant depend on it; to move to another cloud " +
			"grant, replace the `restate_cloud_region_grant`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The grant ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"account_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Account the grant was confirmed through.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"grant_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The `restate_cloud_region_grant` to confirm.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"grant": schema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
				MarkdownDescription: "The grant module's `grant` output, unchanged: `roleArn` and " +
					"`permissionsBoundary` on AWS; `workloadIdentityProvider`, `serviceAccount` and " +
					"`identityProject` on GCP; `tenantId` and `clientId` on Azure.",
				Validators: []validator.Map{mapvalidator.SizeAtLeast(1)},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The grant's status, `READY` after a successful apply.",
			},
			"confirmed_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When Restate confirmed the grant.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true}),
		},
	}
}

func (r *CloudRegionGrantConfirmationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *CloudRegionGrantConfirmationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan cloudRegionGrantConfirmationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Create(ctx, defaultGrantConfirmationCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if r.confirm(ctx, &plan, timeout, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

// Update confirms the grant again with a changed output. Restate answers the grant when the output is
// the one it confirmed, which is the case after an import, and refuses any other.
func (r *CloudRegionGrantConfirmationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan cloudRegionGrantConfirmationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Update(ctx, defaultGrantConfirmationCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if r.confirm(ctx, &plan, timeout, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *CloudRegionGrantConfirmationResource) confirm(ctx context.Context, plan *cloudRegionGrantConfirmationModel, timeout time.Duration, diags *diag.Diagnostics) bool {
	if diags.HasError() || !requireRegionGrantClient(r.data, "restate_cloud_region_grant_confirmation", diags) {
		return false
	}
	output := map[string]string{}
	diags.Append(plan.Grant.ElementsAs(ctx, &output, false)...)
	if diags.HasError() {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	grantID := plan.GrantID.ValueString()
	grant, err := r.data.Cloud.RegionGrants().WaitUntilConfirmed(ctx, grantID, output)
	if err != nil {
		detail := err.Error()
		switch {
		case api.IsGone(err):
			detail = fmt.Sprintf("Region grant %s has expired or been deleted, so it cannot be confirmed. Replace it "+
				"with `terraform apply -replace=<the restate_cloud_region_grant address>`, which creates a new grant "+
				"and a cloud grant for it.\n\n%s", grantID, err.Error())
		case api.IsConflict(err):
			detail = fmt.Sprintf("Restate refused to confirm region grant %s. Either another grant for the same cloud "+
				"account is already confirmed, or this grant was confirmed with another output, which Restate does "+
				"not allow to change. To move to another cloud grant, replace the restate_cloud_region_grant.\n\n%s",
				grantID, err.Error())
		case errors.Is(err, api.ErrPollTimeout):
			detail = fmt.Sprintf("Restate could not sign in through the cloud grant for region grant %s within %s. "+
				"The grant stays PENDING, and the next apply keeps waiting. Check the cloud's message below; if the "+
				"cloud grant is new, it may still be spreading, and a longer timeout helps.\n\n%s",
				grantID, timeout, err.Error())
		}
		diags.AddError("Could not confirm the Restate Cloud region grant", detail)
		return false
	}

	plan.ID = types.StringValue(grant.ID)
	plan.AccountID = types.StringValue(r.data.Cloud.AccountID())
	plan.Status = types.StringValue(string(grant.Status))
	plan.ConfirmedAt = types.StringValue(grant.ConfirmedAt)
	return true
}

// Read removes the confirmation when the grant has ended, or is PENDING again, so the next apply
// confirms a grant that needs it.
func (r *CloudRegionGrantConfirmationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireRegionGrantClient(r.data, "restate_cloud_region_grant_confirmation", &resp.Diagnostics) {
		return
	}

	var state cloudRegionGrantConfirmationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	grant, err := r.data.Cloud.RegionGrants().GetLive(ctx, state.GrantID.ValueString())
	if err != nil {
		if errors.Is(err, cloud.ErrRegionGrantGone) {
			if grant.ID == "" && !accountMatches(r.data, state.AccountID, &resp.Diagnostics, "region grant", state.GrantID.ValueString()) {
				return
			}
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not read the Restate Cloud region grant", err.Error())
		return
	}
	if grant.Status != cloud.RegionGrantReady {
		resp.State.RemoveResource(ctx)
		return
	}

	state.Status = types.StringValue(string(grant.Status))
	state.ConfirmedAt = types.StringValue(grant.ConfirmedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *CloudRegionGrantConfirmationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireRegionGrantClient(r.data, "restate_cloud_region_grant_confirmation", &resp.Diagnostics) {
		return
	}

	var state cloudRegionGrantConfirmationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Terraform stops the destroy at this error, before it reaches the grant module, so the cloud side
	// of the grant stays in place for as long as Restate still needs it.
	deleteRegionGrant(ctx, r.data.Cloud, state.GrantID.ValueString(),
		"Restate kept the region grant, so its cloud side was kept too", &resp.Diagnostics)
}

// ImportState accepts a grant ID. The grant module's output cannot be read back, so the first apply
// after an import confirms the grant again with the configured output, which Restate accepts when it
// is the output it confirmed.
func (r *CloudRegionGrantConfirmationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("grant_id"), req.ID)...)
	if r.data != nil && r.data.Cloud != nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), r.data.Cloud.AccountID())...)
	}
}
