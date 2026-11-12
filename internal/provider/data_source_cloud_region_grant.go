package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// CloudRegionGrantDataSource looks up a region grant that another configuration manages, typically an
// organization's grant that an account builds regions with.
type CloudRegionGrantDataSource struct {
	data *providerData
}

var (
	_ datasource.DataSource              = (*CloudRegionGrantDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*CloudRegionGrantDataSource)(nil)
)

// NewCloudRegionGrantDataSource returns the data source factory.
func NewCloudRegionGrantDataSource() datasource.DataSource { return &CloudRegionGrantDataSource{} }

func (d *CloudRegionGrantDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_region_grant"
}

type cloudRegionGrantDataSourceModel struct {
	ID             types.String `tfsdk:"id"`
	Owner          types.String `tfsdk:"owner"`
	OwnerID        types.String `tfsdk:"owner_id"`
	DisplayName    types.String `tfsdk:"display_name"`
	Cloud          types.String `tfsdk:"cloud"`
	CloudAccountID types.String `tfsdk:"cloud_account_id"`
	Status         types.String `tfsdk:"status"`
	StatusReason   types.String `tfsdk:"status_reason"`
	StatusMessage  types.String `tfsdk:"status_message"`
	ResourcePrefix types.String `tfsdk:"resource_prefix"`
	RegionIDs      types.List   `tfsdk:"region_ids"`
	CreatedAt      types.String `tfsdk:"created_at"`
	ConfirmedAt    types.String `tfsdk:"confirmed_at"`
}

func (d *CloudRegionGrantDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computed := func(description string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: description}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "An existing region grant of the account or of its organization, looked up by ID.\n\n" +
			"Use it where one configuration manages an organization's grant and others build regions with it: " +
			"check `status` before creating a `restate_cloud_byoc_region`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Grant ID to look up, for example `rgr_x5z2y4qjmt`.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"owner":            computed("`ACCOUNT` or `ORGANIZATION`."),
			"owner_id":         computed("ID of the account or organization that owns the grant."),
			"display_name":     computed("Name shown for the grant."),
			"cloud":            computed("Cloud the grant is in: `aws`, `gcp` or `azure`."),
			"cloud_account_id": computed("AWS account ID, GCP project ID or Azure subscription ID the grant is in."),
			"status":           computed("`PENDING`, `READY`, `EXPIRED` or `DELETED`. Regions can be built only with a `READY` grant."),
			"status_reason":    computed("While the grant is `PENDING`: `AWAITING_CONFIRMATION`, `CONFIRMING` or `CONFIRMATION_FAILED`."),
			"status_message":   computed("With `CONFIRMATION_FAILED`: the cloud's answer to Restate's sign-in."),
			"resource_prefix":  computed("Prefix of every resource name a region of the grant has in the cloud account. Empty once the grant has ended."),
			"region_ids": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Regions built with the grant that are not deleted yet.",
			},
			"created_at":   computed("When the grant was created."),
			"confirmed_at": computed("When Restate confirmed the grant. Empty while it is `PENDING`."),
		},
	}
}

func (d *CloudRegionGrantDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *CloudRegionGrantDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireRegionGrantClient(d.data, "restate_cloud_region_grant", &resp.Diagnostics) {
		return
	}

	var config cloudRegionGrantDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	grant, err := d.data.Cloud.RegionGrants().Get(ctx, config.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not read the Restate Cloud region grant", err.Error())
		return
	}

	state := cloudRegionGrantDataSourceModel{
		ID:             types.StringValue(grant.ID),
		Owner:          types.StringValue(string(grant.Owner)),
		OwnerID:        types.StringValue(grant.OwnerID),
		DisplayName:    types.StringValue(grant.DisplayName),
		Cloud:          types.StringValue(string(grant.Cloud)),
		CloudAccountID: types.StringValue(grant.Account.ID()),
		Status:         types.StringValue(string(grant.Status)),
		StatusReason:   types.StringValue(grant.StatusReason),
		StatusMessage:  types.StringValue(grant.StatusMessage),
		ResourcePrefix: types.StringValue(""),
		RegionIDs:      stringList(grant.RegionIDs),
		CreatedAt:      types.StringValue(grant.CreatedAt),
		ConfirmedAt:    types.StringValue(grant.ConfirmedAt),
	}
	if grant.Inputs != nil {
		state.ResourcePrefix = types.StringValue(grant.Inputs.ResourcePrefix)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
