package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api/admin"
)

// ServerInfoDataSource exposes GET /version.
type ServerInfoDataSource struct {
	data *providerData
}

var (
	_ datasource.DataSource              = (*ServerInfoDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*ServerInfoDataSource)(nil)
)

// NewServerInfoDataSource returns the data source factory.
func NewServerInfoDataSource() datasource.DataSource { return &ServerInfoDataSource{} }

func (d *ServerInfoDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server_info"
}

type serverInfoModel struct {
	Version            types.String `tfsdk:"version"`
	MinAdminAPIVersion types.Int64  `tfsdk:"min_admin_api_version"`
	MaxAdminAPIVersion types.Int64  `tfsdk:"max_admin_api_version"`
	IngressEndpoint    types.String `tfsdk:"ingress_endpoint"`
	Features           types.Map    `tfsdk:"features"`
	NegotiatedVersion  types.Int64  `tfsdk:"negotiated_admin_api_version"`
}

func (d *ServerInfoDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Version and capability information for the Restate server behind the provider's " +
			"`admin_url`.\n\n" +
			"This is a read-only diagnostic. It is useful for asserting a minimum server version in a " +
			"configuration, or for reading the advertised ingress endpoint.",
		Attributes: map[string]schema.Attribute{
			"version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Restate server version, for example `1.7.6`.",
			},
			"min_admin_api_version": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Lowest Admin API version the server accepts.",
			},
			"max_admin_api_version": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Highest Admin API version the server accepts.",
			},
			"negotiated_admin_api_version": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: fmt.Sprintf(
					"Highest Admin API version both the server and this provider support. This provider's API "+
						"client was generated against version %d (Restate %s) and supports versions %d to %d.",
					admin.PinnedContractAdminAPIVersion, admin.PinnedContractServerVersion,
					admin.MinSupportedAdminAPIVersion, admin.MaxSupportedAdminAPIVersion),
			},
			"ingress_endpoint": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Ingress endpoint the server advertises for invoking services. Empty when the " +
					"server reports none.",
			},
			"features": schema.MapAttribute{
				Computed:            true,
				ElementType:         types.BoolType,
				MarkdownDescription: "The server's experimental features and whether each is enabled.",
			},
		},
	}
}

func (d *ServerInfoDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *ServerInfoDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireAdminClient(d.data, "restate_server_info", &resp.Diagnostics) {
		return
	}

	info, err := d.data.Admin.ServerInfo(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the Restate server version", err.Error())
		return
	}

	features, diags := types.MapValueFrom(ctx, types.BoolType, orEmptyBoolMap(info.Features))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state := serverInfoModel{
		Version:            types.StringValue(info.Version),
		MinAdminAPIVersion: types.Int64Value(int64(info.MinAdminAPIVersion)),
		MaxAdminAPIVersion: types.Int64Value(int64(info.MaxAdminAPIVersion)),
		IngressEndpoint:    types.StringValue(info.IngressEndpoint),
		Features:           features,
		NegotiatedVersion:  types.Int64Value(int64(admin.CheckCompatibility(info).NegotiatedVersion)),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// orEmptyBoolMap turns a nil map into an empty one, so the attribute is an empty map rather than
// null. A server that reports no features has reported an empty set, which is different from the
// provider not knowing.
func orEmptyBoolMap(m map[string]bool) map[string]bool {
	if m == nil {
		return map[string]bool{}
	}
	return m
}
