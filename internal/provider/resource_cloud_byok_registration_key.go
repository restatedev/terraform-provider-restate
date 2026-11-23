package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

// CloudByokRegistrationKeyResource manages the one-use key that registers a BYOK region.
type CloudByokRegistrationKeyResource struct {
	data *providerData
}

var (
	_ resource.Resource               = (*CloudByokRegistrationKeyResource)(nil)
	_ resource.ResourceWithConfigure  = (*CloudByokRegistrationKeyResource)(nil)
	_ resource.ResourceWithModifyPlan = (*CloudByokRegistrationKeyResource)(nil)
)

// NewCloudByokRegistrationKeyResource returns the resource factory.
func NewCloudByokRegistrationKeyResource() resource.Resource {
	return &CloudByokRegistrationKeyResource{}
}

func (r *CloudByokRegistrationKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_byok_registration_key"
}

type cloudByokRegistrationKeyModel struct {
	ID                        types.String `tfsdk:"id"`
	AccountID                 types.String `tfsdk:"account_id"`
	Cloud                     types.String `tfsdk:"cloud"`
	ProviderRegion            types.String `tfsdk:"provider_region"`
	RegionDisplayName         types.String `tfsdk:"region_display_name"`
	DeregisterRegionOnDestroy types.Bool   `tfsdk:"deregister_region_on_destroy"`

	Key       types.String `tfsdk:"key"`
	RegionID  types.String `tfsdk:"region_id"`
	ExpiresAt types.String `tfsdk:"expires_at"`
	ImagePull types.Object `tfsdk:"image_pull"`
}

// imagePullAttrTypes is the shape of image_pull, which is null when the control plane returned no
// image pull credential.
var imagePullAttrTypes = map[string]attr.Type{
	"registry":          types.StringType,
	"username":          types.StringType,
	"password":          types.StringType,
	"repository_prefix": types.StringType,
}

func imagePullValue(pull *cloud.ByokImagePull) (types.Object, diag.Diagnostics) {
	if pull == nil {
		return types.ObjectNull(imagePullAttrTypes), nil
	}
	return types.ObjectValue(imagePullAttrTypes, map[string]attr.Value{
		"registry":          types.StringValue(pull.Registry),
		"username":          types.StringValue(pull.Username),
		"password":          types.StringValue(pull.Password),
		"repository_prefix": types.StringValue(pull.RepositoryPrefix),
	})
}

func (r *CloudByokRegistrationKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stable := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "The one-use key that registers a BYOK region: a Restate region that runs in a " +
			"Kubernetes cluster you build yourself. The BYOC Helm chart's region installation operator presents " +
			"the key to Restate Cloud, which creates the region under the id the key reserved. Give `key` to the " +
			"chart as its registration Secret and `region_id` as `region.id`.\n\n" +
			"A key works once and expires four hours after it is created, so create it after the cluster exists " +
			"and install the chart soon after. A key that expired or was used is not renewed: replace the " +
			"resource (`terraform apply -replace=...`) to mint a new one, which reserves a new region id. " +
			"Every key that no region has used counts against an account limit until it expires.\n\n" +
			"The response that creates the key also carries a pull credential for Restate's registry proxy, " +
			"which `image_pull` exposes. The chart's direct image source mode stores it in a Secret that the " +
			"chart's pods pull with, and the region's installer replaces its content once the region is active.\n\n" +
			"The API has no operation that reads a key back, so a refresh keeps the state as it is, and the key " +
			"cannot be imported. Changing any argument other than `deregister_region_on_destroy` replaces the " +
			"key. The key and the pull credential stay in the Terraform state, as every secret this provider " +
			"stores does, and are useless there once a region has registered with the key or it has expired.\n\n" +
			"Destroying the resource does not revoke an unused key, because the API cannot, and it leaves the " +
			"registered region in place unless `deregister_region_on_destroy` is set.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The id of the region the key reserves, the same as `region_id`.",
				PlanModifiers:       stable,
			},
			"account_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Account the key and its region belong to.",
				PlanModifiers:       stable,
			},
			"cloud": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Cloud the region's cluster runs on. Only `gcp` is supported.",
				Validators:          []validator.String{stringvalidator.OneOf(string(cloud.CloudGCP))},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"provider_region": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The cloud's own name for where the cluster runs, for example `us-central1`. " +
					"The region must report the same name when it registers, and the chart takes it as " +
					"`region.providerRegion`.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 64)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"region_display_name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The name the Restate Cloud console shows for the region. Restate chooses one when it is omitted.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 128)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"deregister_region_on_destroy": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When true, destroying this resource also removes the region that registered " +
					"with the key (`DeleteByokRegion`), which revokes the region's access keys and removes its " +
					"Restate-side access to the cluster. Restate refuses while the region hosts environments, and " +
					"the destroy then fails until they are deleted. Defaults to false, which leaves the region " +
					"registered. A replacement of the key destroys the old resource, so with this set it also " +
					"removes the region the old key registered. A key that no region has used reserved no region, " +
					"and there is nothing to remove.",
			},

			"key": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				MarkdownDescription: "The registration key, for the BYOC chart's registration Secret under the key " +
					"`key`. Restate shows it only in the response that creates it.",
				PlanModifiers: stable,
			},
			"region_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The id the key gives the region it registers, for example `rgn_a1b2c3d4e5`. " +
					"The chart takes it as `region.id` and derives the region's addresses from it. The region " +
					"exists at Restate only after the chart has registered.",
				PlanModifiers: stable,
			},
			"expires_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the key stops working if no region has used it.",
				PlanModifiers:       stable,
			},
			"image_pull": schema.SingleNestedAttribute{
				Computed: true,
				MarkdownDescription: "The credential for Restate's registry proxy that the region's images are pulled " +
					"with. The BYOC chart's `imageSource.mode = \"direct\"` pulls every image from the proxy, and the " +
					"credential goes into a `kubernetes.io/dockerconfigjson` Secret in the chart's namespace. " +
					"It is also what copies the images into your own registry first, in mirror mode. Restate shows it " +
					"only in the response that creates the key, and the credential stops working when the key " +
					"expires unused. After a region registers with the key, the credential keeps working until " +
					"the region activates and for 24 hours after that, while the region's own pull key replaces " +
					"it. `null` when the control plane returned no credential, which is the case for a stage that serves no images.",
				PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
				Attributes: map[string]schema.Attribute{
					"registry": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "The registry proxy host, such as `registry.us.restate.cloud`.",
					},
					"username": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "The user name for the registry. Any value works; Docker and crane insist on one.",
					},
					"password": schema.StringAttribute{
						Computed:            true,
						Sensitive:           true,
						MarkdownDescription: "A Restate API key for the registry proxy, which pulls the images of the region this key reserves and nothing else.",
					},
					"repository_prefix": schema.StringAttribute{
						Computed: true,
						MarkdownDescription: "What a repository name is appended to: the registry host and the stage's prefix. " +
							"The region's private images live under it.",
					},
				},
			},
		},
	}
}

func (r *CloudByokRegistrationKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

// ModifyPlan warns before a replacement that would remove a registered region. A replacement mints a
// new key with a new region id, and destroys the old resource first.
func (r *CloudByokRegistrationKeyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan cloudByokRegistrationKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	// The computed attributes keep their state while the plan is made, so a replacement is recognised by
	// an argument that replaces the key having changed.
	replaces := !plan.Cloud.Equal(state.Cloud) || !plan.ProviderRegion.Equal(state.ProviderRegion) ||
		!plan.RegionDisplayName.Equal(state.RegionDisplayName)
	if resp.Diagnostics.HasError() || !replaces || !state.DeregisterRegionOnDestroy.ValueBool() {
		return
	}
	resp.Diagnostics.AddWarning("Replacing a registration key removes its region",
		fmt.Sprintf("This plan replaces the registration key for region %s, and the resource has "+
			"`deregister_region_on_destroy = true`, so applying it also removes that region if the chart registered "+
			"it, and revokes its access keys. Restate refuses while the region hosts environments. The new key "+
			"reserves a different region id.", state.RegionID.ValueString()))
}

func (r *CloudByokRegistrationKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireCloudClient(r.data, "restate_cloud_byok_registration_key", &resp.Diagnostics) {
		return
	}

	var plan cloudByokRegistrationKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := r.data.Cloud.ByokRegistrations().Create(ctx, cloud.CreateByokRegistrationKeyInput{
		Cloud:          cloud.CloudProvider(plan.Cloud.ValueString()),
		ProviderRegion: plan.ProviderRegion.ValueString(),
		DisplayName:    plan.RegionDisplayName.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not create the Restate Cloud BYOK registration key", createKeyDetail(err))
		return
	}

	state := plan
	state.ID = types.StringValue(key.RegionID)
	state.AccountID = types.StringValue(r.data.Cloud.AccountID())
	state.Key = types.StringValue(key.Key)
	state.RegionID = types.StringValue(key.RegionID)
	state.ExpiresAt = types.StringValue(key.ExpiresAt)
	imagePull, diags := imagePullValue(key.ImagePull)
	resp.Diagnostics.Append(diags...)
	state.ImagePull = imagePull
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// createKeyDetail names the two refusals an operator can act on.
func createKeyDetail(err error) string {
	switch {
	case api.IsForbidden(err):
		return "The account is not authorized to register BYOC regions, or the management API key's role cannot " +
			"register them. Ask Restate to enable BYOC for the account.\n\n" + err.Error()
	case api.IsTooManyRequests(err):
		return "The account already holds the most registration keys that no region has used. A key expires four " +
			"hours after it was created, so wait for one to expire, or install the chart with a key you already hold.\n\n" +
			err.Error()
	default:
		return err.Error()
	}
}

// Read keeps the state: the API has no operation that describes a key or the region it reserves
// before the chart registers.
func (r *CloudByokRegistrationKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state cloudByokRegistrationKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update applies deregister_region_on_destroy, the only argument that does not replace the key. It
// lives only in the state.
func (r *CloudByokRegistrationKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state cloudByokRegistrationKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.DeregisterRegionOnDestroy = plan.DeregisterRegionOnDestroy
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *CloudByokRegistrationKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state cloudByokRegistrationKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !state.DeregisterRegionOnDestroy.ValueBool() {
		return
	}
	if !requireCloudClient(r.data, "restate_cloud_byok_registration_key", &resp.Diagnostics) {
		return
	}

	// The API answers 404 for a region of another account as for one that does not exist, so without
	// this check a provider pointed at the wrong account would report the region removed.
	if recorded, configured := state.AccountID.ValueString(), r.data.Cloud.AccountID(); recorded != "" && recorded != configured {
		resp.Diagnostics.AddError("Restate Cloud BYOK registration key belongs to a different account",
			fmt.Sprintf("Region %s was reserved through account %s, but this provider is configured for account %s. "+
				"Point the provider at the original account, or use a provider alias for it.",
				state.RegionID.ValueString(), recorded, configured))
		return
	}

	regionID := state.RegionID.ValueString()
	if err := r.data.Cloud.ByokRegistrations().DeleteRegion(ctx, regionID); err != nil {
		detail := err.Error()
		if api.IsConflict(err) {
			detail = fmt.Sprintf("Region %s still hosts environments, so Restate did not remove it. Delete the "+
				"environments in it, then destroy again.\n\n%s", regionID, err.Error())
		}
		resp.Diagnostics.AddAttributeError(path.Root("region_id"), "Could not remove the BYOK region", detail)
	}
}
