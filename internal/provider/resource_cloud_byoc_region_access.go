package provider

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

// A write waits for a region that is still being built, for the infrastructure behind the region's
// load balancer, and for AWS to create the endpoint service. These are estimates with a margin, not
// measurements.
const (
	defaultByocRegionAccessWriteTimeout  = 20 * time.Minute
	defaultByocRegionAccessDeleteTimeout = 10 * time.Minute
)

// The contract's limits on an access setting.
const (
	maxEntrySourceCIDRs  = 10
	maxAllowedPrincipals = 20
)

// allowedPrincipalPattern is the contract's own constraint on an allowed principal.
var allowedPrincipalPattern = regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:(root|role/.+|user/.+)$`)

// CloudByocRegionAccessResource manages who may reach the entry point of an AWS Managed BYOC region.
type CloudByocRegionAccessResource struct {
	data *providerData
}

var (
	_ resource.Resource                = (*CloudByocRegionAccessResource)(nil)
	_ resource.ResourceWithConfigure   = (*CloudByocRegionAccessResource)(nil)
	_ resource.ResourceWithImportState = (*CloudByocRegionAccessResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*CloudByocRegionAccessResource)(nil)
)

// NewCloudByocRegionAccessResource returns the resource factory.
func NewCloudByocRegionAccessResource() resource.Resource { return &CloudByocRegionAccessResource{} }

func (r *CloudByocRegionAccessResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_byoc_region_access"
}

type cloudByocRegionAccessModel struct {
	ID               types.String `tfsdk:"id"`
	AccountID        types.String `tfsdk:"account_id"`
	RegionID         types.String `tfsdk:"region_id"`
	EntrySourceCIDRs types.Set    `tfsdk:"entry_source_cidrs"`
	PrivateLink      types.Object `tfsdk:"private_link"`

	EndpointServiceName  types.String `tfsdk:"endpoint_service_name"`
	EndpointServiceState types.String `tfsdk:"endpoint_service_state"`
	AvailabilityZoneIDs  types.Set    `tfsdk:"availability_zone_ids"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

type privateLinkModel struct {
	Enabled            types.Bool `tfsdk:"enabled"`
	AllowedPrincipals  types.Set  `tfsdk:"allowed_principals"`
	AcceptanceRequired types.Bool `tfsdk:"acceptance_required"`
}

var privateLinkAttributeTypes = map[string]attr.Type{
	"enabled":             types.BoolType,
	"allowed_principals":  types.SetType{ElemType: types.StringType},
	"acceptance_required": types.BoolType,
}

func (r *CloudByocRegionAccessResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Who may reach the entry point of an AWS Managed BYOC region built with a region grant: " +
			"the IPv4 networks that may connect to its load balancer directly, and an AWS PrivateLink endpoint " +
			"service for private connections from your own VPCs. The region must be `ACTIVE`.\n\n" +
			"The resource sets the region's whole access setting, so an argument left out returns to its default: " +
			"every address is admitted, and there is no endpoint service. Manage one `restate_cloud_byoc_region_access` " +
			"for each region. Two of them for the same region overwrite each other. Destroying the resource " +
			"returns the region to those defaults.\n\n" +
			"Restricting `entry_source_cidrs` also keeps out Restate's control plane. While a list is set, " +
			"Restate collects no usage from the region's environments, and the Cloud Console's environment " +
			"views work only from an allowed network. Restate's own AWS account is always allowed to connect to " +
			"an enabled endpoint service, and no argument changes that.\n\n" +
			"Once you enable `private_link`, the apply waits until AWS has created the endpoint service, so " +
			"`endpoint_service_name` is known to resources that use it in the same apply, such as an " +
			"`aws_vpc_endpoint`. The wait is bounded by the `create` or `update` timeout. If it runs out during a " +
			"create, the setting stays in effect at Restate and nothing is recorded in the state, so the next " +
			"apply sends the same setting again and keeps waiting.\n\n" +
			"This resource needs a Restate Cloud control plane that offers `SetRegionAccess`. Where it does " +
			"not, the apply fails with a message that says so.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The region ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"account_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Account the region belongs to.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"region_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The AWS Managed BYOC region to set the access of, for example " +
					"`restate_cloud_byoc_region.<name>.id`. Referencing it makes the resource wait until the " +
					"region is `ACTIVE`, and makes a destroy reset the access before the region is deleted. " +
					"Changing it replaces the resource.",
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"entry_source_cidrs": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: fmt.Sprintf("The IPv4 networks that may reach the region's load balancer "+
					"directly, such as `203.0.113.0/24`. Each must be a network address with no host bits set and no "+
					"leading zeros, and at most %d are allowed.\n\n"+
					"Leave it out to admit every address. An empty set admits no client directly, so the region "+
					"is reached only through `private_link`. Traffic through an endpoint carries no consumer "+
					"address, so this list does not apply to it.", maxEntrySourceCIDRs),
				Validators: []validator.Set{
					setvalidator.SizeAtMost(maxEntrySourceCIDRs),
					setvalidator.ValueStringsAre(ipv4NetworkValidator{}),
				},
			},
			"private_link": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "A VPC endpoint service on the region's load balancer, so that the listed " +
					"principals can connect through interface endpoints. Leave it out for a region with no " +
					"endpoint service.",
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Required: true,
						MarkdownDescription: "Whether the region has an endpoint service. With `false`, the region has " +
							"none, and the other arguments are kept only in the configuration.",
					},
					"allowed_principals": schema.SetAttribute{
						Optional:    true,
						ElementType: types.StringType,
						MarkdownDescription: fmt.Sprintf("The IAM principals that may connect an interface endpoint to the "+
							"service: `arn:aws:iam::<12-digit account>:root`, `...:role/<name>` or `...:user/<name>`, "+
							"at most %d. Restate's own AWS account is always allowed and cannot be listed.", maxAllowedPrincipals),
						Validators: []validator.Set{
							setvalidator.SizeAtMost(maxAllowedPrincipals),
							setvalidator.ValueStringsAre(stringvalidator.RegexMatches(allowedPrincipalPattern,
								"must be an IAM principal ARN: arn:aws:iam::<12-digit account>:(root|role/<name>|user/<name>)")),
						},
					},
					"acceptance_required": schema.BoolAttribute{
						Optional:            true,
						Computed:            true,
						Default:             booldefault.StaticBool(false),
						MarkdownDescription: "Whether each connection request waits until Restate accepts it. Defaults to `false`.",
					},
				},
			},
			"endpoint_service_name": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The name to give an interface endpoint, `com.amazonaws.vpce.<region>.vpce-svc-<id>`. " +
					"Null while `private_link` is off. Use it as `service_name` of an `aws_vpc_endpoint`.",
			},
			"endpoint_service_state": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The state AWS reports for the endpoint service, such as `Available`. Null while `private_link` is off.",
			},
			"availability_zone_ids": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				MarkdownDescription: "The zones the endpoint service covers, as zone IDs such as `euc1-az1`. Zone names " +
					"differ between AWS accounts, so match the subnets of an interface endpoint by zone ID. " +
					"Null while `private_link` is off.",
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *CloudByocRegionAccessResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

// ModifyPlan keeps the endpoint service's name and zones in the plan while PrivateLink stays enabled,
// because an unknown name would show every `aws_vpc_endpoint` that uses it as replaced. The service
// survives a change of principals, and the state of AWS's service can move in between, so that stays
// unknown on every change.
func (r *CloudByocRegionAccessResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || req.Plan.Raw.Equal(req.State.Raw) {
		return
	}
	var plan, state cloudByocRegionAccessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	switch enabled := privateLinkEnabled(plan.PrivateLink); {
	case enabled.IsUnknown():
	case !enabled.ValueBool():
		plan.EndpointServiceName = types.StringNull()
		plan.EndpointServiceState = types.StringNull()
		plan.AvailabilityZoneIDs = types.SetNull(types.StringType)
	default:
		plan.EndpointServiceState = types.StringUnknown()
		if privateLinkEnabled(state.PrivateLink).ValueBool() && !state.EndpointServiceName.IsNull() {
			plan.EndpointServiceName = state.EndpointServiceName
			plan.AvailabilityZoneIDs = state.AvailabilityZoneIDs
		}
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *CloudByocRegionAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireRegionGrantClient(r.data, "restate_cloud_byoc_region_access", &resp.Diagnostics) {
		return
	}
	var plan cloudByocRegionAccessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Create(ctx, defaultByocRegionAccessWriteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Nothing is recorded when the write fails. A state recorded with an error makes Terraform replace
	// the resource, which resets the access, and with it deletes the endpoint service, before it sends
	// the setting again.
	applied, _, err := r.write(ctx, plan, timeout, true, &resp.Diagnostics)
	if err != nil {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &applied)...)
}

// Update sends the whole setting again. A wait that runs out still records what Restate stored, so
// state describes the region rather than the plan.
func (r *CloudByocRegionAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireRegionGrantClient(r.data, "restate_cloud_byoc_region_access", &resp.Diagnostics) {
		return
	}
	var plan cloudByocRegionAccessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Update(ctx, defaultByocRegionAccessWriteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	applied, stored, err := r.write(ctx, plan, timeout, false, &resp.Diagnostics)
	if err != nil {
		if stored {
			r.recordWhatRestateHolds(ctx, plan, &resp.State, &resp.Diagnostics)
		}
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &applied)...)
}

// write sends the plan's setting, waits for the endpoint service when PrivateLink is enabled, and
// answers the state that describes the region. It reports its own failure as a diagnostic and returns
// the error that caused it, and whether Restate had stored the setting by then.
func (r *CloudByocRegionAccessResource) write(ctx context.Context, plan cloudByocRegionAccessModel, timeout time.Duration, creating bool, diags *diag.Diagnostics) (applied cloudByocRegionAccessModel, stored bool, err error) {
	access, accessDiags := accessFromModel(ctx, plan)
	diags.Append(accessDiags...)
	if diags.HasError() {
		return plan, false, errors.New("the configuration could not be read")
	}
	regionID := plan.RegionID.ValueString()
	privateLink := access.PrivateLink != nil && access.PrivateLink.Enabled
	if access.EntrySourceCIDRs != nil && len(access.EntrySourceCIDRs) == 0 && !privateLink {
		diags.AddWarning("No client can reach the region",
			fmt.Sprintf("Region %s admits no address directly, and has no PrivateLink endpoint service, so no client can "+
				"connect to it. Set `entry_source_cidrs` or enable `private_link` unless that is the intent.", regionID))
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	regions := r.data.Cloud.ByocRegions()
	if _, err := regions.SetAccessWhenReady(ctx, regionID, access); err != nil {
		summary, detail := describeAccessError(err, regionID)
		diags.AddError(summary, detail)
		return plan, false, err
	}

	var region cloud.ByocRegion
	if privateLink {
		region, err = regions.WaitForEndpointService(ctx, regionID)
	} else {
		region, err = regions.GetExisting(ctx, regionID)
	}
	if err != nil {
		if errors.Is(err, api.ErrPollTimeout) {
			next := "Apply again to keep waiting, or raise the update timeout."
			if creating {
				next = "Nothing is recorded in the state. Apply again to send the same setting and keep waiting, or raise the create timeout."
			}
			diags.AddError("Restate Cloud BYOC region access was set but the endpoint service is not ready",
				fmt.Sprintf("Region %s accepted the setting, which stays in effect, but AWS had not created the endpoint "+
					"service within %s: %s\n\n%s", regionID, timeout, err.Error(), next))
		} else {
			diags.AddError("Could not read the Restate Cloud BYOC region after setting its access", err.Error())
		}
		return plan, true, err
	}

	applied = plan
	r.applyAccess(&applied, region)
	if !plan.EntrySourceCIDRs.Equal(applied.EntrySourceCIDRs) || !plan.PrivateLink.Equal(applied.PrivateLink) {
		diags.AddError("The Restate API did not apply the requested access",
			fmt.Sprintf("Region %s was asked to take the access setting in the configuration, but it reports another one: "+
				"entry_source_cidrs %s, private_link %s. Apply again; if it persists, report it to Restate with the "+
				"region ID.", regionID, applied.EntrySourceCIDRs, applied.PrivateLink))
		return plan, true, errors.New("the stored access differs from the requested one")
	}
	return applied, true, nil
}

// recordWhatRestateHolds refreshes the state after an update that failed once the setting was stored,
// so the next plan compares against the region rather than against an earlier setting.
func (r *CloudByocRegionAccessResource) recordWhatRestateHolds(ctx context.Context, prior cloudByocRegionAccessModel, state *tfsdk.State, diags *diag.Diagnostics) {
	region, err := r.data.Cloud.ByocRegions().GetExisting(ctx, prior.RegionID.ValueString())
	if err != nil {
		return
	}
	refreshed := prior
	r.applyAccess(&refreshed, region)
	diags.Append(state.Set(ctx, &refreshed)...)
}

func (r *CloudByocRegionAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireRegionGrantClient(r.data, "restate_cloud_byoc_region_access", &resp.Diagnostics) {
		return
	}
	var state cloudByocRegionAccessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if recorded, configured := state.AccountID.ValueString(), r.data.Cloud.AccountID(); recorded != "" && recorded != configured {
		resp.Diagnostics.AddError("Restate Cloud BYOC region access belongs to a different account",
			fmt.Sprintf("The access of region %s was set through account %s, but this provider is configured for account %s. "+
				"Point the provider at the original account, or use a provider alias for it.",
				state.RegionID.ValueString(), recorded, configured))
		return
	}

	region, err := r.data.Cloud.ByocRegions().GetExisting(ctx, state.RegionID.ValueString())
	if err != nil {
		if errors.Is(err, cloud.ErrRegionGone) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not read the Restate Cloud BYOC region", err.Error())
		return
	}

	r.applyAccess(&state, region)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete returns the region to its defaults. A region that is gone has nothing to reset.
func (r *CloudByocRegionAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireRegionGrantClient(r.data, "restate_cloud_byoc_region_access", &resp.Diagnostics) {
		return
	}
	var state cloudByocRegionAccessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := state.Timeouts.Delete(ctx, defaultByocRegionAccessDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	regionID := state.RegionID.ValueString()
	if err := r.data.Cloud.ByocRegions().ResetAccess(ctx, regionID); err != nil {
		summary, detail := describeAccessError(err, regionID)
		resp.Diagnostics.AddError("Could not reset the access of the Restate Cloud BYOC region",
			summary+".\n\n"+detail)
	}
}

// ImportState accepts "<account ID>/<region ID>". The refresh that follows reads the setting.
func (r *CloudByocRegionAccessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	fields, ok := splitImportID(req.ID, 2)
	if !ok {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected `<account ID>/<region ID>`, for example `acc_1a2b3c/rgn_a1b2c3d4e5`, but got %q.", req.ID))
		return
	}
	accountID, regionID := fields[0], fields[1]
	if !requireRegionGrantClient(r.data, "restate_cloud_byoc_region_access", &resp.Diagnostics) {
		return
	}
	if configured := r.data.Cloud.AccountID(); accountID != configured {
		resp.Diagnostics.AddError("Import ID does not match the configured account",
			fmt.Sprintf("The import ID names account %s, but this provider is configured for account %s. "+
				"Use a provider alias configured for the right account, or correct the import ID.", accountID, configured))
		return
	}
	if _, err := r.data.Cloud.ByocRegions().GetExisting(ctx, regionID); err != nil {
		resp.Diagnostics.AddError("Could not import the access of the Restate Cloud BYOC region", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), regionID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), accountID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region_id"), regionID)...)
}

// applyAccess describes the region in the model. The configurable arguments keep the null-or-empty
// shape the model already has wherever the API cannot tell the two apart, so a region that reports no
// setting does not turn a configured empty principal list into drift.
func (r *CloudByocRegionAccessResource) applyAccess(model *cloudByocRegionAccessModel, region cloud.ByocRegion) {
	model.ID = types.StringValue(region.ID)
	model.AccountID = types.StringValue(r.data.Cloud.AccountID())
	model.RegionID = types.StringValue(region.ID)

	var access cloud.RegionAccess
	if region.Access != nil {
		access = *region.Access
	}

	if access.EntrySourceCIDRs == nil {
		model.EntrySourceCIDRs = types.SetNull(types.StringType)
	} else {
		model.EntrySourceCIDRs = stringSet(access.EntrySourceCIDRs)
	}

	model.EndpointServiceName = types.StringNull()
	model.EndpointServiceState = types.StringNull()
	model.AvailabilityZoneIDs = types.SetNull(types.StringType)

	link := access.PrivateLink
	if link == nil || !link.Enabled {
		// A disabled link and an absent one are the same setting. A private_link the configuration
		// declares as disabled stays, so it does not produce a change on every plan.
		if !privateLinkEnabled(model.PrivateLink).IsNull() &&
			!privateLinkEnabled(model.PrivateLink).ValueBool() {
			return
		}
		model.PrivateLink = types.ObjectNull(privateLinkAttributeTypes)
		return
	}

	principals := types.SetNull(types.StringType)
	if len(link.AllowedPrincipals) > 0 {
		principals = stringSet(link.AllowedPrincipals)
	} else if prior := priorAllowedPrincipals(model.PrivateLink); !prior.IsNull() && !prior.IsUnknown() {
		principals = types.SetValueMust(types.StringType, nil)
	}
	model.PrivateLink = types.ObjectValueMust(privateLinkAttributeTypes, map[string]attr.Value{
		"enabled":             types.BoolValue(true),
		"allowed_principals":  principals,
		"acceptance_required": types.BoolValue(link.AcceptanceRequired),
	})
	if service := link.EndpointService; service != nil && service.ServiceName != "" {
		model.EndpointServiceName = types.StringValue(service.ServiceName)
		model.EndpointServiceState = optionalString(service.State)
		model.AvailabilityZoneIDs = stringSet(service.AvailabilityZoneIDs)
	}
}

func priorAllowedPrincipals(link types.Object) types.Set {
	if link.IsNull() || link.IsUnknown() {
		return types.SetNull(types.StringType)
	}
	principals, ok := link.Attributes()["allowed_principals"].(types.Set)
	if !ok {
		return types.SetNull(types.StringType)
	}
	return principals
}

// privateLinkEnabled is the `enabled` value of private_link: null when it is absent, unknown while the
// configuration does not know it yet.
func privateLinkEnabled(link types.Object) types.Bool {
	if link.IsNull() {
		return types.BoolNull()
	}
	if link.IsUnknown() {
		return types.BoolUnknown()
	}
	enabled, ok := link.Attributes()["enabled"].(types.Bool)
	if !ok {
		return types.BoolNull()
	}
	return enabled
}

// accessFromModel builds the whole setting to send. A null entry source set stays nil, which admits
// every address, and an empty one stays empty and non-nil, which admits none.
func accessFromModel(ctx context.Context, model cloudByocRegionAccessModel) (cloud.RegionAccess, diag.Diagnostics) {
	var diags diag.Diagnostics
	var access cloud.RegionAccess

	if !model.EntrySourceCIDRs.IsNull() {
		cidrs := []string{}
		diags.Append(model.EntrySourceCIDRs.ElementsAs(ctx, &cidrs, false)...)
		slices.Sort(cidrs)
		access.EntrySourceCIDRs = cidrs
	}

	if !model.PrivateLink.IsNull() {
		var block privateLinkModel
		diags.Append(model.PrivateLink.As(ctx, &block, basetypes.ObjectAsOptions{})...)
		link := &cloud.RegionPrivateLink{
			Enabled:            block.Enabled.ValueBool(),
			AcceptanceRequired: block.AcceptanceRequired.ValueBool(),
		}
		if !block.AllowedPrincipals.IsNull() {
			diags.Append(block.AllowedPrincipals.ElementsAs(ctx, &link.AllowedPrincipals, false)...)
			slices.Sort(link.AllowedPrincipals)
		}
		access.PrivateLink = link
	}
	return access, diags
}

func stringSet(values []string) types.Set {
	elements := make([]attr.Value, len(values))
	for i, value := range values {
		elements[i] = types.StringValue(value)
	}
	return types.SetValueMust(types.StringType, elements)
}

// describeAccessError says what a refusal of SetRegionAccess means and what to do about it, with the
// API's own message after it.
func describeAccessError(err error, regionID string) (summary, detail string) {
	summary = "Could not set the access of the Restate Cloud BYOC region"
	switch {
	case api.IsBadRequest(err):
		detail = fmt.Sprintf("Restate refused the access setting for region %s. Access is available for AWS regions "+
			"built with a region grant, and the setting must follow the limits of the arguments.\n\n%s", regionID, err.Error())
	case api.IsForbidden(err):
		detail = fmt.Sprintf("The configured key may not set the access of region %s. The key needs a role that manages "+
			"regions. A region built with an organization's region grant needs a key that may use that grant.\n\n%s",
			regionID, err.Error())
	case api.IsNotFound(err):
		detail = fmt.Sprintf("Restate did not find region %s through this account. Check the region ID and the "+
			"account, and that the Restate Cloud control plane is a release that offers `SetRegionAccess`.\n\n%s",
			regionID, err.Error())
	case api.IsConflict(err):
		detail = fmt.Sprintf("Region %s cannot take an access setting in its current state. It may have been set up "+
			"with an installer instead of a region grant, or it is being deleted or has failed to build.\n\n%s", regionID, err.Error())
	case api.IsNotImplemented(err):
		detail = fmt.Sprintf("This Restate Cloud stage does not offer region access yet, so region %s keeps its "+
			"entry point open to every address. Use a stage that does, or leave this resource out.\n\n%s", regionID, err.Error())
	case errors.Is(err, api.ErrPollTimeout):
		detail = fmt.Sprintf("Restate did not accept the access setting for region %s in time. The region may still be "+
			"building, or its infrastructure cannot take the setting yet. Raise the timeout, or apply again.\n\n%s", regionID, err.Error())
	default:
		detail = err.Error()
	}
	return summary, detail
}

// ipv4NetworkValidator accepts an IPv4 network in CIDR notation with no host bits set and no leading
// zeros, which is what the API accepts.
type ipv4NetworkValidator struct{}

func (ipv4NetworkValidator) Description(context.Context) string {
	return "must be an IPv4 network address in CIDR notation, such as 203.0.113.0/24"
}

func (v ipv4NetworkValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (ipv4NetworkValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	prefix, err := netip.ParsePrefix(value)
	switch {
	case err != nil || !prefix.Addr().Is4():
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid IPv4 network",
			fmt.Sprintf("%q is not an IPv4 network in CIDR notation, such as 203.0.113.0/24, written without leading zeros.", value))
	case prefix != prefix.Masked():
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid IPv4 network",
			fmt.Sprintf("%q has host bits set. Use the network address, which is %q.", value, prefix.Masked().String()))
	}
}
