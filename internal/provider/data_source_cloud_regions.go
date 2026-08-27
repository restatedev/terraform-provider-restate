package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// CloudRegionsDataSource lists the regions an account may place environments in.
type CloudRegionsDataSource struct {
	data *providerData
}

var (
	_ datasource.DataSource              = (*CloudRegionsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*CloudRegionsDataSource)(nil)
)

// NewCloudRegionsDataSource returns the data source factory.
func NewCloudRegionsDataSource() datasource.DataSource { return &CloudRegionsDataSource{} }

func (d *CloudRegionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_regions"
}

type cloudRegionsModel struct {
	Names   types.List `tfsdk:"names"`
	Regions types.List `tfsdk:"regions"`
}

var regionAttributes = map[string]attr.Type{
	"name":         types.StringType,
	"display_name": types.StringType,
}

func (d *CloudRegionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Regions the configured Restate Cloud account may place environments in.\n\n" +
			"Use it to validate a region before an apply, or to pick one without hard-coding it. An " +
			"environment's region cannot be changed after creation, so getting it wrong means replacing the " +
			"environment.",
		Attributes: map[string]schema.Attribute{
			"names": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Region names, in the order the API returned them. Convenient for `contains()` checks.",
			},
			"regions": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The regions, with their human-readable labels.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Region name, as used in `restate_cloud_environment.region`.",
						},
						"display_name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Human-readable region label. Empty when the API reports none.",
						},
					},
				},
			},
		},
	}
}

func (d *CloudRegionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *CloudRegionsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloudClient(d.data, "restate_cloud_regions", &resp.Diagnostics) {
		return
	}

	regions, err := d.data.Cloud.ListRegions(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not list Restate Cloud regions", err.Error())
		return
	}

	names := make([]attr.Value, 0, len(regions))
	objects := make([]attr.Value, 0, len(regions))
	for _, region := range regions {
		names = append(names, types.StringValue(region.Name))
		object, diags := types.ObjectValue(regionAttributes, map[string]attr.Value{
			"name":         types.StringValue(region.Name),
			"display_name": types.StringValue(region.DisplayName),
		})
		resp.Diagnostics.Append(diags...)
		objects = append(objects, object)
	}

	nameList, diags := types.ListValue(types.StringType, names)
	resp.Diagnostics.Append(diags...)
	regionList, diags := types.ListValue(types.ObjectType{AttrTypes: regionAttributes}, objects)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &cloudRegionsModel{Names: nameList, Regions: regionList})...)
}
