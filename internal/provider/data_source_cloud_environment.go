package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

// CloudEnvironmentDataSource looks up an existing environment the provider does not manage.
type CloudEnvironmentDataSource struct {
	data *providerData
}

var (
	_ datasource.DataSource              = (*CloudEnvironmentDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*CloudEnvironmentDataSource)(nil)
)

// NewCloudEnvironmentDataSource returns the data source factory.
func NewCloudEnvironmentDataSource() datasource.DataSource { return &CloudEnvironmentDataSource{} }

func (d *CloudEnvironmentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_environment"
}

type cloudEnvironmentDataSourceModel struct {
	ID        types.String `tfsdk:"id"`
	AccountID types.String `tfsdk:"account_id"`
	Name      types.String `tfsdk:"name"`
	Region    types.String `tfsdk:"region"`
	TierID    types.String `tfsdk:"tier_id"`

	Status               types.String `tfsdk:"status"`
	RegionDisplayName    types.String `tfsdk:"region_display_name"`
	SigningPublicKey     types.String `tfsdk:"signing_public_key"`
	IngressBaseURL       types.String `tfsdk:"ingress_base_url"`
	AdminBaseURL         types.String `tfsdk:"admin_base_url"`
	InvocationMetricsURL types.String `tfsdk:"invocation_metrics_url"`
	TunnelBaseURL        types.String `tfsdk:"tunnel_base_url"`
	TunnelSRV            types.String `tfsdk:"tunnel_srv"`
	ProxyBaseURL         types.String `tfsdk:"proxy_base_url"`
	APIKeyIDs            types.List   `tfsdk:"api_key_ids"`
}

func (d *CloudEnvironmentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An existing Restate Cloud environment, looked up by ID.\n\n" +
			"Use it to read the connection metadata of an environment this configuration does not manage -- " +
			"typically the second stage of a two-stage bootstrap, where the environment was created by a " +
			"different state.\n\n" +
			"It exposes no secrets. An environment API key's bearer secret is available only when the key is " +
			"created, so this data source lists key IDs and nothing more.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment ID to look up, for example `env_1a2b3c`.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"account_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Account the environment was found in: the account the provider is configured for.",
			},
			"name":   schema.StringAttribute{Computed: true, MarkdownDescription: "Environment name."},
			"region": schema.StringAttribute{Computed: true, MarkdownDescription: "Region the environment runs in."},
			"tier_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: fmt.Sprintf("Environment tier. Unlike the resource, this can report `%s`, "+
					"since reading a custom-tier environment carries no drift risk.", cloud.TierCustom),
			},
			"status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Provisioning status: `PENDING`, `ACTIVE`, `FAILED`, or `DELETED`. Check this " +
					"before depending on the connection metadata: a `PENDING` environment may not have all of it yet.",
			},
			"region_display_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Human-readable region label. Empty when the API reports none."},
			"signing_public_key": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Public key services use to verify that a request came from this environment.",
			},
			"ingress_base_url":       schema.StringAttribute{Computed: true, MarkdownDescription: "Base URL for invoking services in this environment."},
			"admin_base_url":         schema.StringAttribute{Computed: true, MarkdownDescription: "Base URL of this environment's Admin API."},
			"invocation_metrics_url": schema.StringAttribute{Computed: true, MarkdownDescription: "URL for this environment's invocation metrics. Empty when the API reports none."},
			"tunnel_base_url":        schema.StringAttribute{Computed: true, MarkdownDescription: "Base URL of the private tunnel service for this environment."},
			"tunnel_srv":             schema.StringAttribute{Computed: true, MarkdownDescription: "SRV record the tunnel client resolves to reach this environment."},
			"proxy_base_url":         schema.StringAttribute{Computed: true, MarkdownDescription: "Base URL of the proxy service for this environment."},
			"api_key_ids": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				MarkdownDescription: "IDs of the API keys that exist for this environment, including any created " +
					"outside Terraform. Secrets are not included and cannot be read back.",
			},
		},
	}
}

func (d *CloudEnvironmentDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *CloudEnvironmentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloudClient(d.data, "restate_cloud_environment", &resp.Diagnostics) {
		return
	}

	var config cloudEnvironmentDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	environmentID := config.ID.ValueString()
	environment, err := d.data.Cloud.Environments().GetExisting(ctx, environmentID)
	if err != nil {
		// A data source that cannot find its target is an error, not an empty result: something in
		// the configuration depends on it.
		if errors.Is(err, cloud.ErrEnvironmentGone) {
			resp.Diagnostics.AddError("Restate Cloud environment not found",
				fmt.Sprintf("No environment %s exists in account %s.\n\n%s",
					environmentID, d.data.Cloud.AccountID(), err.Error()))
			return
		}
		resp.Diagnostics.AddError("Could not read the Restate Cloud environment", err.Error())
		return
	}

	keyIDs := make([]attr.Value, 0, len(environment.APIKeyIDs))
	for _, keyID := range environment.APIKeyIDs {
		keyIDs = append(keyIDs, types.StringValue(keyID))
	}
	keyIDList, diags := types.ListValue(types.StringType, keyIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state := cloudEnvironmentDataSourceModel{
		ID:                   types.StringValue(environment.ID),
		AccountID:            types.StringValue(d.data.Cloud.AccountID()),
		Name:                 types.StringValue(environment.Name),
		Region:               types.StringValue(environment.Region),
		TierID:               types.StringValue(environment.Tier.String()),
		Status:               types.StringValue(string(environment.Status)),
		RegionDisplayName:    types.StringValue(environment.RegionDisplayName),
		SigningPublicKey:     types.StringValue(environment.SigningPublicKey),
		IngressBaseURL:       types.StringValue(environment.IngressBaseURL),
		AdminBaseURL:         types.StringValue(environment.AdminBaseURL),
		InvocationMetricsURL: types.StringValue(environment.InvocationMetricsURL),
		TunnelBaseURL:        types.StringValue(environment.TunnelBaseURL),
		TunnelSRV:            types.StringValue(environment.TunnelSRV),
		ProxyBaseURL:         types.StringValue(environment.ProxyBaseURL),
		APIKeyIDs:            keyIDList,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
