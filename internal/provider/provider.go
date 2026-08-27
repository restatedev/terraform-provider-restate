// Package provider implements the Terraform provider. It owns schema definition, state conversion,
// and resource lifecycle logic, and reaches the two APIs only through the facades in pkg/api.
package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

// Environment variables the provider reads when the corresponding attribute is not configured.
const (
	envCloudAPIURL       = "RESTATE_CLOUD_API_URL"
	envCloudAccountID    = "RESTATE_CLOUD_ACCOUNT_ID"
	envManagementAPIKey  = "RESTATE_CLOUD_MANAGEMENT_API_KEY"
	envAdminURL          = "RESTATE_ADMIN_URL"
	envEnvironmentAPIKey = "RESTATE_AUTH_TOKEN"
)

// DefaultRequestTimeout bounds one API request including its retries. Deployment registration
// performs discovery against the user's own endpoint, so resources that need longer set their own
// timeout rather than raising this default for everything.
const DefaultRequestTimeout = 60 * time.Second

// RestateProvider is the provider implementation.
type RestateProvider struct {
	// version is stamped at build time and appears in the user agent.
	version string
	// commit is the build's source revision, when known.
	commit string
}

var _ provider.Provider = (*RestateProvider)(nil)

// New returns a provider factory for the given build version.
func New(version, commit string) func() provider.Provider {
	return func() provider.Provider {
		return &RestateProvider{version: version, commit: commit}
	}
}

func (p *RestateProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "restate"
	resp.Version = p.version
}

// providerModel mirrors the provider schema.
type providerModel struct {
	CloudAPIURL       types.String `tfsdk:"cloud_api_url"`
	AccountID         types.String `tfsdk:"account_id"`
	ManagementAPIKey  types.String `tfsdk:"management_api_key"`
	AdminURL          types.String `tfsdk:"admin_url"`
	EnvironmentAPIKey types.String `tfsdk:"environment_api_key"`
	RequestTimeout    types.String `tfsdk:"request_timeout"`
	AllowInsecureHTTP types.Bool   `tfsdk:"allow_insecure_http"`
}

func (p *RestateProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages Restate Cloud environments through the Cloud Management API, and configures " +
			"environments through the per-environment Admin API.\n\n" +
			"The two APIs use separate credentials, and the provider keeps them separate: a management key is never " +
			"sent to an environment, and an environment key is never sent to the Cloud Management API. Cloud-only, " +
			"Admin-only, and combined configurations are all valid.\n\n" +
			"Provisioning an environment and configuring it are two stages. Terraform must be able to configure a " +
			"provider before any resource exists, so an `admin_url` produced by `restate_cloud_environment` cannot " +
			"configure a provider in the same apply. See the two-stage bootstrap guide.",
		Attributes: map[string]schema.Attribute{
			"cloud_api_url": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: fmt.Sprintf(
					"Base URL of the Restate Cloud Management API. Defaults to the `%s` environment variable, "+
						"or `%s`. The `/cloud` path suffix is part of the endpoint and is preserved. This is not "+
						"inferred from an environment's region: an environment lives in a region, the management API "+
						"that provisions it does not.",
					envCloudAPIURL, cloud.DefaultBaseURL),
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"account_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: fmt.Sprintf(
					"Restate Cloud account every Cloud Management operation is scoped to. Defaults to the `%s` "+
						"environment variable. Use provider aliases to manage more than one account in a single "+
						"configuration.", envCloudAccountID),
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"management_api_key": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: fmt.Sprintf(
					"Account-scoped API key with role `%s`. Defaults to the `%s` environment variable. It is sent "+
						"only to `cloud_api_url`, is never written to resource state, and must belong to "+
						"`account_id`. It is a different credential from an environment-scoped API key and cannot be "+
						"created by this provider; mint it outside Terraform.",
					cloud.RoleManagementAccess, envManagementAPIKey),
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"admin_url": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: fmt.Sprintf(
					"Complete base URL of one environment's Admin API, for example "+
						"`https://<environment>.env.<region>.restate.cloud:9070`. Defaults to the `%s` environment "+
						"variable. There is no computed default: the provider does not construct this from an "+
						"environment ID, because that would depend on today's hostname format. Read it from "+
						"`restate_cloud_environment.<name>.admin_base_url` in a previous stage.", envAdminURL),
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"environment_api_key": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: fmt.Sprintf(
					"Environment-scoped API key for `admin_url`, with role `%s` or `%s`. Defaults to the `%s` "+
						"environment variable. It is sent only to `admin_url` and is never written to resource state.",
					cloud.RoleAdminAccess, cloud.RoleFullAccess, envEnvironmentAPIKey),
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"request_timeout": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: fmt.Sprintf(
					"Timeout for a single API request including its retries, as a Go duration such as `30s` or "+
						"`2m`. Defaults to `%s`. Resource-level timeouts bound whole operations such as environment "+
						"provisioning and deployment discovery, and are set per resource.", DefaultRequestTimeout),
			},
			"allow_insecure_http": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Permit a plaintext `http://` URL for a host that is not loopback. This sends " +
					"the API key unencrypted and exists only for testing against a local or self-hosted Restate. " +
					"Never enable it against a real endpoint.",
			},
		},
	}
}

// providerData is what resources and data sources receive. Either client may be nil: a Cloud-only or
// Admin-only configuration is valid, and the resource that needs a missing client is the one that
// reports it.
type providerData struct {
	Cloud *cloud.Client
	Admin *admin.Client

	// AdminCompatibility is the result of the version check performed at configure time, so a
	// resource can mention it without repeating the round trip.
	AdminCompatibility *admin.Compatibility

	// RequestTimeout is the configured per-request timeout, for resources that build their own
	// bounded contexts.
	RequestTimeout time.Duration
}

func (p *RestateProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A resource operation cannot run before provider configuration is known. Bail out rather than
	// building a client from an unknown value, which would send a literal placeholder as a
	// credential.
	if unknown := unknownAttributes(config); len(unknown) > 0 {
		resp.Diagnostics.AddError(
			"Provider configuration is not known yet",
			fmt.Sprintf("The provider attributes %s depend on values that are not known until apply. "+
				"Terraform cannot configure a provider from them.\n\n"+
				"This usually means a provider attribute references a resource created in the same configuration. "+
				"Provisioning an environment and configuring it must be separate stages: pass the environment's "+
				"admin URL and API key through variables or a secret store, not through a reference in the same "+
				"apply. See the two-stage bootstrap guide.", strings.Join(unknown, ", ")))
		return
	}

	timeout, diags := resolveTimeout(config.RequestTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	allowInsecure := config.AllowInsecureHTTP.ValueBool()
	userAgent := p.userAgent(req.TerraformVersion)
	data := &providerData{RequestTimeout: timeout}

	data.Cloud = p.configureCloud(ctx, config, userAgent, timeout, allowInsecure, resp)
	data.Admin, data.AdminCompatibility = p.configureAdmin(ctx, config, userAgent, timeout, allowInsecure, resp)
	if resp.Diagnostics.HasError() {
		return
	}

	// Neither credential set configured is not an error in itself: a configuration may exist only to
	// declare provider aliases. The resources report it when they need a client they do not have.
	resp.DataSourceData = data
	resp.ResourceData = data
	resp.EphemeralResourceData = data
}

func (p *RestateProvider) configureCloud(
	ctx context.Context,
	config providerModel,
	userAgent string,
	timeout time.Duration,
	allowInsecure bool,
	resp *provider.ConfigureResponse,
) *cloud.Client {
	baseURL := firstNonEmpty(config.CloudAPIURL.ValueString(), os.Getenv(envCloudAPIURL), cloud.DefaultBaseURL)
	accountID := firstNonEmpty(config.AccountID.ValueString(), os.Getenv(envCloudAccountID))
	apiKey := firstNonEmpty(config.ManagementAPIKey.ValueString(), os.Getenv(envManagementAPIKey))

	// A partial credential set is a mistake, not a Cloud-less configuration. Failing here beats
	// failing on the first environment resource with a confusing "not configured" message.
	switch {
	case accountID == "" && apiKey == "":
		return nil
	case accountID == "":
		resp.Diagnostics.AddAttributeError(path.Root("account_id"),
			"Incomplete Restate Cloud configuration",
			fmt.Sprintf("A management API key was configured without an account ID. Set `account_id` or the `%s` "+
				"environment variable, or remove `management_api_key` if this provider instance should not manage "+
				"Cloud resources.", envCloudAccountID))
		return nil
	case apiKey == "":
		resp.Diagnostics.AddAttributeError(path.Root("management_api_key"),
			"Incomplete Restate Cloud configuration",
			fmt.Sprintf("An account ID was configured without a management API key. Set `management_api_key` or "+
				"the `%s` environment variable, or remove `account_id` if this provider instance should not manage "+
				"Cloud resources.", envManagementAPIKey))
		return nil
	}

	client, err := cloud.New(cloud.Config{
		BaseURL:           baseURL,
		AccountID:         accountID,
		ManagementAPIKey:  apiKey,
		UserAgent:         userAgent,
		Timeout:           timeout,
		Retry:             api.DefaultRetryPolicy(),
		Poll:              api.DefaultPollConfig(),
		AllowInsecureHTTP: allowInsecure,
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid Restate Cloud configuration", err.Error())
		return nil
	}

	// The Cloud Management API has no version endpoint, so a read-only account describe is the only
	// available proof that the credential works and is pointed at the right account.
	if err := client.ValidateCredentials(ctx); err != nil {
		resp.Diagnostics.AddError("Could not authenticate to the Restate Cloud Management API", err.Error())
		return nil
	}
	return client
}

func (p *RestateProvider) configureAdmin(
	ctx context.Context,
	config providerModel,
	userAgent string,
	timeout time.Duration,
	allowInsecure bool,
	resp *provider.ConfigureResponse,
) (*admin.Client, *admin.Compatibility) {
	baseURL := firstNonEmpty(config.AdminURL.ValueString(), os.Getenv(envAdminURL))
	apiKey := firstNonEmpty(config.EnvironmentAPIKey.ValueString(), os.Getenv(envEnvironmentAPIKey))

	switch {
	case baseURL == "" && apiKey == "":
		return nil, nil
	case baseURL == "":
		resp.Diagnostics.AddAttributeError(path.Root("admin_url"),
			"Incomplete Restate Admin API configuration",
			fmt.Sprintf("An environment API key was configured without an Admin API URL. Set `admin_url` or the "+
				"`%s` environment variable, or remove `environment_api_key` if this provider instance should not "+
				"manage environment resources.", envAdminURL))
		return nil, nil
	case apiKey == "":
		resp.Diagnostics.AddAttributeError(path.Root("environment_api_key"),
			"Incomplete Restate Admin API configuration",
			fmt.Sprintf("An Admin API URL was configured without an environment API key. Set "+
				"`environment_api_key` or the `%s` environment variable, or remove `admin_url` if this provider "+
				"instance should not manage environment resources.", envEnvironmentAPIKey))
		return nil, nil
	}

	client, err := admin.New(admin.Config{
		BaseURL:           baseURL,
		APIKey:            apiKey,
		UserAgent:         userAgent,
		Timeout:           timeout,
		Retry:             api.DefaultRetryPolicy(),
		AllowInsecureHTTP: allowInsecure,
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid Restate Admin API configuration", err.Error())
		return nil, nil
	}

	compatibility, err := client.CheckServerCompatibility(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not reach the Restate Admin API",
			fmt.Sprintf("Reading the server version from %s failed. Check that `admin_url` points at an "+
				"environment's Admin API port and that `environment_api_key` is valid for it.\n\n%s",
				client.BaseURL(), err.Error()))
		return nil, nil
	}
	if !compatibility.Compatible {
		resp.Diagnostics.AddError("Incompatible Restate Admin API version", compatibility.Problem)
		return nil, nil
	}
	if compatibility.Warning != "" {
		resp.Diagnostics.AddWarning("Restate Admin API version is older than this provider's contract", compatibility.Warning)
	}

	return client, &compatibility
}

func (p *RestateProvider) userAgent(terraformVersion string) string {
	version := p.version
	if version == "" {
		version = "dev"
	}
	agent := fmt.Sprintf("terraform-provider-restate/%s", version)
	if p.commit != "" {
		agent += fmt.Sprintf(" (%s)", p.commit)
	}
	if terraformVersion != "" {
		agent += fmt.Sprintf(" terraform/%s", terraformVersion)
	}
	return agent
}

// unknownAttributes lists the configured-but-unknown attributes, which is the signal that a provider
// attribute references something created in the same apply.
func unknownAttributes(config providerModel) []string {
	candidates := []struct {
		name  string
		known bool
	}{
		{"cloud_api_url", !config.CloudAPIURL.IsUnknown()},
		{"account_id", !config.AccountID.IsUnknown()},
		{"management_api_key", !config.ManagementAPIKey.IsUnknown()},
		{"admin_url", !config.AdminURL.IsUnknown()},
		{"environment_api_key", !config.EnvironmentAPIKey.IsUnknown()},
		{"request_timeout", !config.RequestTimeout.IsUnknown()},
		{"allow_insecure_http", !config.AllowInsecureHTTP.IsUnknown()},
	}

	var unknown []string
	for _, candidate := range candidates {
		if !candidate.known {
			unknown = append(unknown, "`"+candidate.name+"`")
		}
	}
	return unknown
}

func resolveTimeout(configured types.String) (time.Duration, diag.Diagnostics) {
	var diags diag.Diagnostics

	raw := strings.TrimSpace(configured.ValueString())
	if raw == "" {
		return DefaultRequestTimeout, diags
	}

	timeout, err := time.ParseDuration(raw)
	if err != nil {
		diags.AddAttributeError(path.Root("request_timeout"), "Invalid request timeout",
			fmt.Sprintf("%q is not a valid Go duration. Use a value such as `30s`, `90s`, or `2m`.", raw))
		return DefaultRequestTimeout, diags
	}
	if timeout <= 0 {
		diags.AddAttributeError(path.Root("request_timeout"), "Invalid request timeout",
			fmt.Sprintf("%q must be greater than zero.", raw))
		return DefaultRequestTimeout, diags
	}
	return timeout, diags
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (p *RestateProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{}
}

func (p *RestateProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewCloudAccountDataSource,
		NewCloudRegionsDataSource,
		NewServerInfoDataSource,
	}
}
