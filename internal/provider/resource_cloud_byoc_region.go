package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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

// Building a region creates a network, a Kubernetes cluster and storage in the customer's cloud. These
// are estimates with a margin, not measurements.
const (
	defaultByocRegionCreateTimeout = 90 * time.Minute
	defaultByocRegionDeleteTimeout = 60 * time.Minute
)

// byocRegionNamePattern is the contract's own constraint on a region name.
var byocRegionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

// CloudByocRegionResource manages a Managed BYOC region built with a region grant.
type CloudByocRegionResource struct {
	data *providerData
}

var (
	_ resource.Resource                = (*CloudByocRegionResource)(nil)
	_ resource.ResourceWithConfigure   = (*CloudByocRegionResource)(nil)
	_ resource.ResourceWithImportState = (*CloudByocRegionResource)(nil)
)

// NewCloudByocRegionResource returns the resource factory.
func NewCloudByocRegionResource() resource.Resource { return &CloudByocRegionResource{} }

func (r *CloudByocRegionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_byoc_region"
}

type cloudByocRegionModel struct {
	ID          types.String `tfsdk:"id"`
	AccountID   types.String `tfsdk:"account_id"`
	Name        types.String `tfsdk:"name"`
	DisplayName types.String `tfsdk:"display_name"`
	GrantID     types.String `tfsdk:"grant_id"`
	CloudRegion types.String `tfsdk:"cloud_region"`
	MachineType types.String `tfsdk:"machine_type"`

	DeletionProtection types.Bool `tfsdk:"deletion_protection"`

	Cloud          types.String `tfsdk:"cloud"`
	CloudAccountID types.String `tfsdk:"cloud_account_id"`
	Status         types.String `tfsdk:"status"`
	StatusReason   types.String `tfsdk:"status_reason"`
	CreatedAt      types.String `tfsdk:"created_at"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func (r *CloudByocRegionResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stable := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Managed BYOC region: a Restate region in your own cloud account, which Restate " +
			"builds and operates through a `READY` region grant. Once it is `ACTIVE`, create environments in it " +
			"with `restate_cloud_environment` and `region` set to its `id`.\n\n" +
			"The region is built in the grant's cloud and cloud account, which the provider reads from the grant. " +
			"Creation polls until the region is `ACTIVE`, and a region that fails to build stays in the state so " +
			"it can be destroyed. Destruction polls until Restate has removed what it built, and Restate refuses " +
			"while the region still has environments.\n\n" +
			"The API has no update operation, so changing any argument other than `deletion_protection` " +
			"replaces the region.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Region ID assigned by Restate Cloud, for example `rgn_a1b2c3d4e5`.",
				PlanModifiers:       stable,
			},
			"account_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Account the region belongs to.",
				PlanModifiers:       stable,
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Name of the region, unique within the account: 3 to 40 lower-case letters, " +
					"digits and hyphens, starting with a letter.",
				Validators: []validator.String{stringvalidator.RegexMatches(byocRegionNamePattern,
					"must be 3 to 40 lower-case letters, digits and hyphens, start with a letter and not end with a hyphen")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"display_name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Name shown for the region. Defaults to `name`.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 100)},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"grant_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The `READY` region grant to build the region with, of the account or of its " +
					"organization. A region keeps its grant for its whole life. Reference " +
					"`restate_cloud_region_grant_confirmation.<name>.grant_id` rather than the grant itself, so " +
					"the region waits for the confirmation and is destroyed before it.",
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"cloud_region": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The AWS region, GCP region or Azure location the region runs in, for example `eu-central-1`.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"machine_type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Node type of the cluster's baseline node pool on GCP, or of its system nodes " +
					"on Azure. Restate chooses one when it is omitted. AWS takes none.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"deletion_protection": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				MarkdownDescription: "When true, the default, Terraform refuses to delete this region, including " +
					"for a replacement. Deleting a region removes everything Restate built in your cloud account " +
					"for it, and the environments in it. Set it to false and apply before a destroy.",
			},

			"cloud": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Cloud the region runs in, which is the grant's.",
				PlanModifiers:       stable,
			},
			"cloud_account_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "AWS account, GCP project or Azure subscription the region runs in, which is the grant's.",
				PlanModifiers:       stable,
			},
			"status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "`PENDING` while the region is being built, then `ACTIVE`. `FAILED`, " +
					"`DELETING` and `DELETED` otherwise. A successful apply leaves it `ACTIVE`.",
			},
			"status_reason": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Why the region has its status, for example `CREATING_INFRASTRUCTURE` or " +
					"`NEEDS_RESTATE_ATTENTION`. Empty when Restate reports none. More values may be added.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the region was created.",
				PlanModifiers:       stable,
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *CloudByocRegionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *CloudByocRegionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireRegionGrantClient(r.data, "restate_cloud_byoc_region", &resp.Diagnostics) {
		return
	}

	var plan cloudByocRegionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultByocRegionCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	grantID := plan.GrantID.ValueString()
	grant, err := r.data.Cloud.RegionGrants().GetLive(ctx, grantID)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("grant_id"), "Could not read the region grant", err.Error())
		return
	}
	if grant.Status != cloud.RegionGrantReady {
		resp.Diagnostics.AddAttributeError(path.Root("grant_id"), "The region grant is not confirmed",
			fmt.Sprintf("Region grant %s is %s. Confirm it with `restate_cloud_region_grant_confirmation`, and "+
				"reference the confirmation's `grant_id` so the region waits for it.", grantID, grant.Status))
		return
	}

	regions := r.data.Cloud.ByocRegions()
	created, err := regions.Create(ctx, cloud.CreateRegionInput{
		Name:        plan.Name.ValueString(),
		DisplayName: plan.DisplayName.ValueString(),
		GrantID:     grantID,
		Cloud:       grant.Cloud,
		CloudRegion: plan.CloudRegion.ValueString(),
		Account:     grant.Account,
		MachineType: plan.MachineType.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not create the Restate Cloud BYOC region", err.Error())
		return
	}

	region, err := regions.WaitUntilActive(ctx, created.ID)
	if err != nil {
		// The region exists and is building in the customer's cloud. Keep it in the state, so a destroy
		// removes it rather than leaving it for someone to find.
		state := plan
		r.applyRegion(&state, created, applyWrite, &resp.Diagnostics)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)

		message := fmt.Sprintf("Region %s was created but did not become active.\n\n%s\n\n"+
			"The region has been recorded in state, so `terraform destroy` can remove it.", created.ID, err.Error())
		if errors.Is(err, api.ErrPollTimeout) {
			message += fmt.Sprintf("\n\nIf the build is simply slow, raise the create timeout:\n\n"+
				"    timeouts {\n      create = \"%s\"\n    }", 2*timeout)
		}
		resp.Diagnostics.AddError("Restate Cloud BYOC region did not finish building", message)
		return
	}

	state := plan
	r.applyRegion(&state, region, applyWrite, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *CloudByocRegionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireRegionGrantClient(r.data, "restate_cloud_byoc_region", &resp.Diagnostics) {
		return
	}

	var state cloudByocRegionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if recorded, configured := state.AccountID.ValueString(), r.data.Cloud.AccountID(); recorded != "" && recorded != configured {
		resp.Diagnostics.AddError("Restate Cloud BYOC region belongs to a different account",
			fmt.Sprintf("Region %s was created in account %s, but this provider is configured for account %s. "+
				"Point the provider at the original account, or use a provider alias for it.",
				state.ID.ValueString(), recorded, configured))
		return
	}

	region, err := r.data.Cloud.ByocRegions().GetExisting(ctx, state.ID.ValueString())
	if err != nil {
		if errors.Is(err, cloud.ErrRegionGone) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not read the Restate Cloud BYOC region", err.Error())
		return
	}

	r.applyRegion(&state, region, applyRead, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update applies deletion_protection, the only argument that does not replace the region. It lives
// only in the state.
func (r *CloudByocRegionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state cloudByocRegionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.DeletionProtection = plan.DeletionProtection
	state.Timeouts = plan.Timeouts
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *CloudByocRegionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireRegionGrantClient(r.data, "restate_cloud_byoc_region", &resp.Diagnostics) {
		return
	}

	var state cloudByocRegionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Restate Cloud BYOC region is protected from deletion",
			fmt.Sprintf("Region %s (%s) has `deletion_protection = true`, so it was not destroyed. To proceed, set "+
				"`deletion_protection = false`, apply that change, and then destroy.",
				state.Name.ValueString(), state.ID.ValueString()))
		return
	}

	timeout, diags := state.Timeouts.Delete(ctx, defaultByocRegionDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	regionID := state.ID.ValueString()
	if err := r.data.Cloud.ByocRegions().DeleteAndWait(ctx, regionID); err != nil {
		detail := err.Error()
		if errors.Is(err, api.ErrPollTimeout) {
			detail = fmt.Sprintf("Region %s is still being deleted. Run `terraform destroy` again to keep waiting; "+
				"the region grant must stay in place until it is gone.\n\n%s", regionID, err.Error())
		}
		resp.Diagnostics.AddError("Could not delete the Restate Cloud BYOC region", detail)
	}
}

// ImportState accepts "<account ID>/<region ID>".
func (r *CloudByocRegionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	fields, ok := splitImportID(req.ID, 2)
	if !ok {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected `<account ID>/<region ID>`, for example `acc_1a2b3c/rgn_a1b2c3d4e5`, but got %q.", req.ID))
		return
	}
	accountID, regionID := fields[0], fields[1]

	if !requireRegionGrantClient(r.data, "restate_cloud_byoc_region", &resp.Diagnostics) {
		return
	}
	if configured := r.data.Cloud.AccountID(); accountID != configured {
		resp.Diagnostics.AddError("Import ID does not match the configured account",
			fmt.Sprintf("The import ID names account %s, but this provider is configured for account %s. "+
				"Use a provider alias configured for the right account, or correct the import ID.", accountID, configured))
		return
	}

	region, err := r.data.Cloud.ByocRegions().GetExisting(ctx, regionID)
	if err != nil {
		resp.Diagnostics.AddError("Could not import the Restate Cloud BYOC region", err.Error())
		return
	}
	if region.GrantID == "" {
		resp.Diagnostics.AddError("The BYOC region was not built with a region grant",
			fmt.Sprintf("Region %s has a grant of its own, which the grant module in "+
				"modules/byoc manages together with the region.", regionID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), region.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), accountID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), region.Name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("grant_id"), region.GrantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cloud_region"), region.CloudRegion)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
}

func (r *CloudByocRegionResource) applyRegion(model *cloudByocRegionModel, region cloud.ByocRegion, mode applyMode, diags *diag.Diagnostics) {
	subject := fmt.Sprintf("Region %s", region.ID)

	model.ID = types.StringValue(region.ID)
	model.AccountID = types.StringValue(r.data.Cloud.AccountID())
	model.Name = reconcileString(mode, model.Name, region.Name, path.Root("name"), subject, diags)
	model.DisplayName = reconcileString(mode, model.DisplayName, region.DisplayName, path.Root("display_name"), subject, diags)
	model.GrantID = reconcileString(mode, model.GrantID, region.GrantID, path.Root("grant_id"), subject, diags)
	model.CloudRegion = reconcileString(mode, model.CloudRegion, region.CloudRegion, path.Root("cloud_region"), subject, diags)
	model.MachineType = reconcileString(mode, model.MachineType, region.MachineType, path.Root("machine_type"), subject, diags)

	model.Cloud = types.StringValue(string(region.Cloud))
	model.CloudAccountID = types.StringValue(region.Account.ID())
	model.Status = types.StringValue(string(region.Status))
	model.StatusReason = types.StringValue(region.StatusReason)
	model.CreatedAt = types.StringValue(region.CreatedAt)
}
