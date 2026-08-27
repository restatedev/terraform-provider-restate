package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

// CloudAccountDataSource exposes the read-only account state the provider can see.
type CloudAccountDataSource struct {
	data *providerData
}

var (
	_ datasource.DataSource              = (*CloudAccountDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*CloudAccountDataSource)(nil)
)

// NewCloudAccountDataSource returns the data source factory.
func NewCloudAccountDataSource() datasource.DataSource { return &CloudAccountDataSource{} }

func (d *CloudAccountDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_account"
}

type cloudAccountModel struct {
	ID                 types.String `tfsdk:"id"`
	BillingMode        types.String `tfsdk:"billing_mode"`
	SubscriptionStatus types.String `tfsdk:"subscription_status"`
	PeriodStart        types.Int64  `tfsdk:"subscription_current_period_start"`
	PeriodEnd          types.Int64  `tfsdk:"subscription_current_period_end"`
	Environments       types.List   `tfsdk:"environments"`
	FreeEnvironments   types.Object `tfsdk:"free_environment_limit"`
}

var environmentSummaryAttributes = map[string]attr.Type{
	"id":     types.StringType,
	"name":   types.StringType,
	"region": types.StringType,
	"tier":   types.StringType,
}

var limitAttributes = map[string]attr.Type{
	"usage": types.Float64Type,
	"limit": types.Float64Type,
}

func (d *CloudAccountDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Read-only state of the Restate Cloud account the provider is configured for.\n\n" +
			"The provider does not manage accounts, billing, or subscriptions: those operations require an " +
			"interactive user identity. This data source exists so a configuration can read the account's billing " +
			"mode -- which decides whether a tier change can complete without a browser -- and enumerate the " +
			"environments the account already has.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The account ID the provider is configured with.",
			},
			"billing_mode": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: fmt.Sprintf(
					"How the account pays: `%s` or `%s`. Empty when the API reports none.\n\n"+
						"A `%s`-billed account may be redirected to an interactive Stripe checkout when an "+
						"environment's tier changes, which Terraform cannot complete. Check this before planning a "+
						"tier change.", cloud.BillingModeStripe, cloud.BillingModeAccount, cloud.BillingModeStripe),
			},
			"subscription_status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: fmt.Sprintf("Subscription state: `%s`, `%s`, or `%s`. Empty when the account "+
					"has no subscription.", cloud.SubscriptionAwaitingPayment, cloud.SubscriptionActive, cloud.SubscriptionInactive),
			},
			"subscription_current_period_start": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Start of the current billing period, as Unix seconds. Null when the account has no subscription.",
			},
			"subscription_current_period_end": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "End of the current billing period, as Unix seconds. Null when the account has no subscription.",
			},
			"environments": schema.ListNestedAttribute{
				Computed: true,
				MarkdownDescription: "Environments in the account. This is a summary: it carries no provisioning " +
					"status and no connection metadata, because the account API does not report them. Use the " +
					"`restate_cloud_environment` data source for those.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":     schema.StringAttribute{Computed: true, MarkdownDescription: "Environment ID."},
						"name":   schema.StringAttribute{Computed: true, MarkdownDescription: "Environment name."},
						"region": schema.StringAttribute{Computed: true, MarkdownDescription: "Region the environment runs in."},
						"tier":   schema.StringAttribute{Computed: true, MarkdownDescription: "Environment tier."},
					},
				},
			},
			"free_environment_limit": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The account's free-environment quota and current usage.",
				Attributes: map[string]schema.Attribute{
					"usage": schema.Float64Attribute{Computed: true, MarkdownDescription: "Free environments in use."},
					"limit": schema.Float64Attribute{Computed: true, MarkdownDescription: "Free environments permitted."},
				},
			},
		},
	}
}

func (d *CloudAccountDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *CloudAccountDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireCloudClient(d.data, "restate_cloud_account", &resp.Diagnostics) {
		return
	}

	account, err := d.data.Cloud.DescribeAccount(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the Restate Cloud account", err.Error())
		return
	}

	// Limits are a separate operation, and an account may not be permitted to read them. A failure
	// there must not fail the whole data source, so it degrades to a null quota with a warning.
	freeLimit := types.ObjectNull(limitAttributes)
	if limits, err := d.data.Cloud.GetAccountLimits(ctx); err != nil {
		resp.Diagnostics.AddWarning("Could not read the Restate Cloud account limits",
			fmt.Sprintf("The account was read successfully, but its limits were not, so `free_environment_limit` is null.\n\n%s", err.Error()))
	} else {
		object, diags := types.ObjectValue(limitAttributes, map[string]attr.Value{
			"usage": types.Float64Value(limits.FreeEnvironments.Usage),
			"limit": types.Float64Value(limits.FreeEnvironments.Limit),
		})
		resp.Diagnostics.Append(diags...)
		freeLimit = object
	}

	environments := make([]attr.Value, 0, len(account.Environments))
	for _, environment := range account.Environments {
		object, diags := types.ObjectValue(environmentSummaryAttributes, map[string]attr.Value{
			"id":     types.StringValue(environment.ID),
			"name":   types.StringValue(environment.Name),
			"region": types.StringValue(environment.Region),
			"tier":   types.StringValue(environment.Tier.String()),
		})
		resp.Diagnostics.Append(diags...)
		environments = append(environments, object)
	}
	environmentList, diags := types.ListValue(types.ObjectType{AttrTypes: environmentSummaryAttributes}, environments)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state := cloudAccountModel{
		ID:               types.StringValue(account.ID),
		BillingMode:      types.StringValue(string(account.BillingMode)),
		Environments:     environmentList,
		FreeEnvironments: freeLimit,
	}
	if account.Subscription != nil {
		state.SubscriptionStatus = types.StringValue(string(account.Subscription.Status))
		state.PeriodStart = types.Int64Value(account.Subscription.CurrentPeriodStart)
		state.PeriodEnd = types.Int64Value(account.Subscription.CurrentPeriodEnd)
	} else {
		// An account with no subscription has no period, and null says that more honestly than zero.
		state.SubscriptionStatus = types.StringValue("")
		state.PeriodStart = types.Int64Null()
		state.PeriodEnd = types.Int64Null()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
