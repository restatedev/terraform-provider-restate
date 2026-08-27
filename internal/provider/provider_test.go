package provider_test

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/restatedev/terraform-provider-restate/internal/provider"
	"github.com/restatedev/terraform-provider-restate/internal/testutil"
)

// protocolProviders is the provider factory the protocol tests run against.
func protocolProviders() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"restate": providerserver.NewProtocol6WithError(provider.New("test", "abc1234")()),
	}
}

// providerEnvironmentVariables are every variable the provider reads.
var providerEnvironmentVariables = []string{
	"RESTATE_CLOUD_API_URL",
	"RESTATE_CLOUD_ACCOUNT_ID",
	"RESTATE_CLOUD_MANAGEMENT_API_KEY",
	"RESTATE_ADMIN_URL",
	"RESTATE_AUTH_TOKEN",
}

// clearProviderEnvironment unsets every variable the provider reads, so a developer's own shell
// cannot make a test pass or fail.
func clearProviderEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range providerEnvironmentVariables {
		t.Setenv(name, "")
	}
}

// cloudServer starts a Cloud Management API stub that answers the credential-validation probe.
func cloudServer(t *testing.T) *testutil.APIServer {
	t.Helper()
	server := testutil.NewAPIServer(t)
	server.On("DescribeAccount", testutil.JSON(`{"environments":[],"billingMode":"account"}`))
	server.On("GetAccountLimits", testutil.JSON(`{"freeEnvironments":{"usage":0,"limit":3}}`))
	server.On("ListRegions", testutil.JSON(`{"regions":[{"regionName":"us-east-1","regionDisplayName":"US East (N. Virginia)"}]}`))
	return server
}

// adminServer starts an Admin API stub that answers the version probe.
func adminServer(t *testing.T) *testutil.APIServer {
	t.Helper()
	server := testutil.NewAPIServer(t)
	// The Admin API is REST-shaped, so routes are matched on the whole path: /deployments and
	// /deployments/<id> are different operations and must stay distinguishable.
	server.UseFullPath = true
	server.On("/version", testutil.JSON(`{
	  "version":"1.7.6","min_admin_api_version":2,"max_admin_api_version":4,
	  "ingress_endpoint":"https://env_1.env.us.restate.cloud","features":{"vqueues":false}
	}`))
	return server
}

func TestProviderSchemaIsValid(t *testing.T) {
	// The framework validates the schema itself, which catches a missing description, an invalid
	// attribute combination, or a reserved name before any plan runs.
	resp := &fwprovider.SchemaResponse{}
	provider.New("test", "")().Schema(context.Background(), fwprovider.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("provider schema is invalid: %v", resp.Diagnostics)
	}
}

// A Cloud-only configuration is valid: it manages environments and keys and never touches an
// environment's Admin API. Configuring must therefore succeed and must not probe the Admin API.
func TestConfigureAcceptsACloudOnlyConfiguration(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)
	admin := adminServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":      cloud.URL,
				"account_id":         "acc_1a2b3c",
				"management_api_key": "key_management",
			}) + forceCloudConfigure(),
		}},
	})

	if count := cloud.CallCount("DescribeAccount"); count == 0 {
		t.Error("the Cloud credential validation probe was not sent")
	}
	if count := admin.CallCount("/version"); count != 0 {
		t.Errorf("the Admin API was probed %d times by a Cloud-only configuration", count)
	}
}

// An Admin-only configuration is equally valid: it configures an existing environment without any
// Cloud Management credentials.
func TestConfigureAcceptsAnAdminOnlyConfiguration(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)
	admin := adminServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"admin_url":           admin.URL,
				"environment_api_key": "key_environment",
			}) + forceAdminConfigure(),
		}},
	})

	if count := admin.CallCount("/version"); count == 0 {
		t.Error("the Admin API version probe was not sent")
	}
	if count := cloud.CallCount("DescribeAccount"); count != 0 {
		t.Errorf("the Cloud Management API was probed %d times by an Admin-only configuration", count)
	}
}

func TestConfigureAcceptsACombinedConfiguration(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)
	admin := adminServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":       cloud.URL,
				"account_id":          "acc_1a2b3c",
				"management_api_key":  "key_management",
				"admin_url":           admin.URL,
				"environment_api_key": "key_environment",
			}) + forceCloudConfigure() + forceAdminConfigure(),
		}},
	})

	if cloud.CallCount("DescribeAccount") == 0 || admin.CallCount("/version") == 0 {
		t.Error("a combined configuration did not probe both APIs")
	}
}

// An entirely empty configuration is valid: a configuration may exist only to declare provider
// aliases. Configuration must therefore succeed, and the data source that needs a client it does not
// have is what reports the problem -- naming the attributes to set and the alias mechanism.
func TestConfigureAcceptsAnEmptyConfigurationAndDefersToTheResource(t *testing.T) {
	clearProviderEnvironment(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(nil) + forceCloudConfigure(),
			ExpectError: regexp.MustCompile(
				`(?s)restate_cloud_regions requires Restate Cloud Management API configuration.*` +
					`RESTATE_CLOUD_ACCOUNT_ID.*provider alias`),
		}},
	})
}

// The Admin API counterpart of the same deferral.
func TestAnAdminDataSourceReportsAMissingAdminClient(t *testing.T) {
	clearProviderEnvironment(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(nil) + forceAdminConfigure(),
			ExpectError: regexp.MustCompile(
				`(?s)restate_server_info requires Restate Admin API configuration.*two-stage bootstrap`),
		}},
	})
}

// A half-configured credential set is a mistake, not a credential-less configuration. Failing at
// configure time beats failing later with a confusing "not configured" message.
func TestConfigureRejectsAPartialCredentialSet(t *testing.T) {
	tests := []struct {
		name        string
		attributes  map[string]string
		expectError *regexp.Regexp
	}{
		{
			name:        "management key without an account",
			attributes:  map[string]string{"management_api_key": "key_management"},
			expectError: regexp.MustCompile(`(?s)Incomplete Restate Cloud configuration.*without an account ID`),
		},
		{
			name:        "account without a management key",
			attributes:  map[string]string{"account_id": "acc_1a2b3c"},
			expectError: regexp.MustCompile(`(?s)Incomplete Restate Cloud configuration.*without a management API key`),
		},
		{
			name:        "environment key without an admin URL",
			attributes:  map[string]string{"environment_api_key": "key_environment"},
			expectError: regexp.MustCompile(`(?s)Incomplete Restate Admin API configuration.*without an Admin API URL`),
		},
		{
			name:        "admin URL without an environment key",
			attributes:  map[string]string{"admin_url": "https://env.restate.cloud:9070"},
			expectError: regexp.MustCompile(`(?s)Incomplete Restate Admin API configuration.*without an environment API key`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearProviderEnvironment(t)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{{
					Config:      providerBlock(tt.attributes) + forceConfigure(),
					ExpectError: tt.expectError,
				}},
			})
		})
	}
}

func TestConfigureReadsEnvironmentVariables(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)
	admin := adminServer(t)

	t.Setenv("RESTATE_CLOUD_API_URL", cloud.URL)
	t.Setenv("RESTATE_CLOUD_ACCOUNT_ID", "acc_from_env")
	t.Setenv("RESTATE_CLOUD_MANAGEMENT_API_KEY", "key_from_env")
	t.Setenv("RESTATE_ADMIN_URL", admin.URL)
	t.Setenv("RESTATE_AUTH_TOKEN", "key_environment_from_env")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			// allow_insecure_http has no environment variable, so it stays in the block. Everything
			// else must come from the environment.
			Config: providerBlock(nil) + forceCloudConfigure() + forceAdminConfigure(),
		}},
	})

	if cloud.CallCount("DescribeAccount") == 0 {
		t.Fatal("the Cloud API was not configured from environment variables")
	}
	if path := cloud.LastRequest().Path; path != "/acc_from_env/DescribeAccount" {
		t.Errorf("path = %q, want the account from the environment", path)
	}
	if auth := cloud.LastRequest().Authorization; auth != "Bearer key_from_env" {
		t.Errorf("Authorization = %q, want the key from the environment", auth)
	}
	if admin.CallCount("/version") == 0 {
		t.Error("the Admin API was not configured from environment variables")
	}
}

// Explicit configuration wins over the environment, so a per-provider-alias override is possible in
// a shell that has the variables set.
func TestExplicitConfigurationOverridesTheEnvironment(t *testing.T) {
	clearProviderEnvironment(t)
	configured := cloudServer(t)

	t.Setenv("RESTATE_CLOUD_ACCOUNT_ID", "acc_from_env")
	t.Setenv("RESTATE_CLOUD_MANAGEMENT_API_KEY", "key_from_env")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":      configured.URL,
				"account_id":         "acc_configured",
				"management_api_key": "key_configured",
			}) + forceCloudConfigure(),
		}},
	})

	request := configured.LastRequest()
	if request.Path != "/acc_configured/DescribeAccount" {
		t.Errorf("path = %q, want the explicitly configured account", request.Path)
	}
	if request.Authorization != "Bearer key_configured" {
		t.Errorf("Authorization = %q, want the explicitly configured key", request.Authorization)
	}
}

// The Cloud Management API has no version endpoint, so a read-only account describe is the only
// available proof that the credential is valid and pointed at the right account. A 403 has to name
// both possibilities.
func TestConfigureReportsARejectedCloudCredential(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := testutil.NewAPIServer(t)
	cloud.On("DescribeAccount", testutil.Error(403, "principal is not authorized"))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":      cloud.URL,
				"account_id":         "acc_wrong",
				"management_api_key": "key_management",
			}) + forceCloudConfigure(),
			ExpectError: regexp.MustCompile(`(?s)Could not authenticate.*acc_wrong.*ManagementApiAccess`),
		}},
	})
}

func TestConfigureReportsAnUnauthorizedCloudCredential(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := testutil.NewAPIServer(t)
	cloud.On("DescribeAccount", testutil.Error(401, "token expired"))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":      cloud.URL,
				"account_id":         "acc_1",
				"management_api_key": "key_bad",
			}) + forceCloudConfigure(),
			ExpectError: regexp.MustCompile(`(?s)Could not authenticate.*revoked`),
		}},
	})
}

// A server whose Admin API range does not overlap the provider's must fail at configure time, not on
// the first decode of a field that no longer exists.
func TestConfigureRejectsAnIncompatibleAdminAPIVersion(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminServer(t)
	admin.Replace("/version", testutil.JSON(`{"version":"1.0.0","min_admin_api_version":0,"max_admin_api_version":1,"features":{}}`))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"admin_url":           admin.URL,
				"environment_api_key": "key_environment",
			}) + forceAdminConfigure(),
			ExpectError: regexp.MustCompile(`(?s)Incompatible Restate Admin API version.*1\.0\.0.*at least version 2`),
		}},
	})
}

// Pointing admin_url at something that is not a Restate Admin API is a common mistake, and the
// diagnostic must say what to check rather than reporting a bare decode failure.
func TestConfigureReportsAnUnreachableAdminAPI(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminServer(t)
	admin.Replace("/version", testutil.Response{Status: 200, Body: `<html>not restate</html>`})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"admin_url":           admin.URL,
				"environment_api_key": "key_environment",
			}) + forceAdminConfigure(),
			ExpectError: regexp.MustCompile(`(?s)Could not reach the Restate Admin API.*Admin API port`),
		}},
	})
}

// A bearer token over plaintext to a remote host is a credential leak, and the message must name the
// override rather than leaving the user to find it.
func TestConfigureRejectsPlaintextToARemoteHost(t *testing.T) {
	clearProviderEnvironment(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: `provider "restate" {
  cloud_api_url      = "http://api.us.restate.cloud/cloud"
  account_id         = "acc_1"
  management_api_key = "key_management"
}
` + forceCloudConfigure(),
			ExpectError: regexp.MustCompile(`(?s)Invalid Restate Cloud configuration.*allow_insecure_http`),
		}},
	})
}

func TestConfigureRejectsAnInvalidRequestTimeout(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
	}{
		{"not a duration", "soon"},
		{"bare number", "30"},
		{"zero", "0s"},
		{"negative", "-5s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearProviderEnvironment(t)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{{
					Config:      providerBlock(map[string]string{"request_timeout": tt.value}) + forceConfigure(),
					ExpectError: regexp.MustCompile(`Invalid request timeout`),
				}},
			})
		})
	}
}

func TestConfigureAcceptsAValidRequestTimeout(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":      cloud.URL,
				"account_id":         "acc_1",
				"management_api_key": "key_management",
				"request_timeout":    "90s",
			}) + forceCloudConfigure(),
		}},
	})
}

// Terraform cannot configure a provider from a value produced by a resource in the same apply. The
// diagnostic must explain the two-stage requirement rather than sending an unknown value as a
// credential.
func TestConfigureRejectsUnknownConfiguration(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			// terraform_data produces a value that is unknown until apply, which is exactly the
			// shape of the mistake this guards against.
			Config: fmt.Sprintf(`
resource "terraform_data" "later" {
  input = %q
}

provider "restate" {
  allow_insecure_http = true
  cloud_api_url       = %q
  account_id          = "acc_1"
  management_api_key  = terraform_data.later.output
}
`, "key_produced_at_apply", cloud.URL) + forceCloudConfigure(),
			ExpectError: regexp.MustCompile(`(?s)Provider configuration is not known yet.*management_api_key.*two-stage bootstrap`),
		}},
	})
}

// A provider alias must be able to address a different account, which is how one configuration
// manages several accounts or environments.
func TestAliasedProvidersAddressDifferentAccounts(t *testing.T) {
	if raceDetectorEnabled {
		// terraform-plugin-testing serves every provider configuration from one in-process server, so
		// two aliased providers make Terraform call ConfigureProvider concurrently on it. The
		// framework stores each call's configure data on that shared server, which the race detector
		// correctly flags -- in the framework's own struct, with no provider code on either stack.
		//
		// Real Terraform runs a separate plugin instance per configuration, so this cannot happen in
		// practice. Skipping under -race keeps the test's value without reporting an upstream race as
		// a provider defect.
		t.Skip("terraform-plugin-testing shares one in-process provider server across aliases, which races in the framework under -race")
	}

	clearProviderEnvironment(t)
	first := cloudServer(t)
	second := cloudServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
provider "restate" {
  allow_insecure_http = true
  cloud_api_url       = %q
  account_id          = "acc_first"
  management_api_key  = "key_first"
}

provider "restate" {
  alias               = "second"
  allow_insecure_http = true
  cloud_api_url       = %q
  account_id          = "acc_second"
  management_api_key  = "key_second"
}
`, first.URL, second.URL) + `
data "restate_cloud_regions" "first" {}

data "restate_cloud_regions" "second" {
  provider = restate.second
}
`,
		}},
	})

	if path := first.LastRequest().Path; path != "/acc_first/DescribeAccount" {
		t.Errorf("default provider addressed %q", path)
	}
	if path := second.LastRequest().Path; path != "/acc_second/DescribeAccount" {
		t.Errorf("aliased provider addressed %q", path)
	}
	if auth := second.LastRequest().Authorization; auth != "Bearer key_second" {
		t.Errorf("aliased provider used %q, want its own credential", auth)
	}
}

// A management key must never reach an environment, and an environment key must never reach the
// Cloud Management API. This is the property the whole two-client design exists to guarantee.
func TestCredentialsAreNotSentToTheWrongAPI(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)
	admin := adminServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":       cloud.URL,
				"account_id":          "acc_1",
				"management_api_key":  "key_management_only",
				"admin_url":           admin.URL,
				"environment_api_key": "key_environment_only",
			}) + forceCloudConfigure() + forceAdminConfigure(),
		}},
	})

	for _, request := range cloud.Requests() {
		if request.Authorization != "Bearer key_management_only" {
			t.Errorf("the Cloud API received %q, want only the management key", request.Authorization)
		}
	}
	for _, request := range admin.Requests() {
		if request.Authorization != "Bearer key_environment_only" {
			t.Errorf("the Admin API received %q, want only the environment key", request.Authorization)
		}
	}
}

// The user agent identifies the provider and Terraform versions, which is how a request in a
// server-side log is traced back to a specific build.
func TestRequestsCarryAStableUserAgent(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":      cloud.URL,
				"account_id":         "acc_1",
				"management_api_key": "key_management",
			}) + forceCloudConfigure(),
		}},
	})

	agent := cloud.LastRequest().UserAgent
	for _, want := range []string{"terraform-provider-restate/test", "abc1234", "terraform/"} {
		if !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(agent) {
			t.Errorf("User-Agent %q is missing %q", agent, want)
		}
	}
}

// providerBlock renders a provider block. allow_insecure_http is always set, because every test
// points at an httptest server on loopback with a generated port; the tests that specifically
// exercise the HTTPS requirement write their own block.
func providerBlock(attributes map[string]string) string {
	block := "provider \"restate\" {\n  allow_insecure_http = true\n"
	for _, name := range []string{
		"cloud_api_url", "account_id", "management_api_key",
		"admin_url", "environment_api_key", "request_timeout",
	} {
		if value, ok := attributes[name]; ok {
			block += fmt.Sprintf("  %s = %q\n", name, value)
		}
	}
	return block + "}\n"
}

// forceCloudConfigure adds a Cloud data source to the configuration.
//
// Terraform only configures a provider that something in the configuration actually uses, so a
// configuration containing nothing but a provider block never reaches Configure and would make a
// configuration test vacuously pass.
func forceCloudConfigure() string {
	return "\ndata \"restate_cloud_regions\" \"probe\" {}\n"
}

// forceAdminConfigure is the Admin API counterpart of forceCloudConfigure.
func forceAdminConfigure() string {
	return "\ndata \"restate_server_info\" \"probe\" {}\n"
}

// forceConfigure uses neither API's data source, for the cases where configuration itself must fail
// before any read happens. It references the provider through a data source that will report a
// missing client, so the expected error has to be the configuration error rather than that one.
func forceConfigure() string {
	return forceCloudConfigure()
}
