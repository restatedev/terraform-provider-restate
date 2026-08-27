package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
)

func TestServerInfoDataSource(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"admin_url":           admin.URL,
				"environment_api_key": "key_environment",
			}) + forceAdminConfigure(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.restate_server_info.probe", "version", "1.7.6"),
				resource.TestCheckResourceAttr("data.restate_server_info.probe", "min_admin_api_version", "2"),
				resource.TestCheckResourceAttr("data.restate_server_info.probe", "max_admin_api_version", "4"),
				// The provider's client was generated against version 4, and the server's maximum is
				// 4, so 4 is the highest both support.
				resource.TestCheckResourceAttr("data.restate_server_info.probe", "negotiated_admin_api_version", "4"),
				resource.TestCheckResourceAttr("data.restate_server_info.probe", "ingress_endpoint", "https://env_1.env.us.restate.cloud"),
				resource.TestCheckResourceAttr("data.restate_server_info.probe", "features.vqueues", "false"),
			),
		}},
	})
}

// A server whose maximum is below the pinned contract's version negotiates down, which is a warning
// rather than a failure, and the data source must report the negotiated version rather than the
// contract's.
func TestServerInfoReportsANegotiatedDowngrade(t *testing.T) {
	clearProviderEnvironment(t)
	admin := testutil.NewAPIServer(t)
	admin.Replace("version", testutil.JSON(`{"version":"1.5.0","min_admin_api_version":2,"max_admin_api_version":3,"features":{}}`))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"admin_url":           admin.URL,
				"environment_api_key": "key_environment",
			}) + forceAdminConfigure(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.restate_server_info.probe", "negotiated_admin_api_version", "3"),
			),
		}},
	})
}

// A server reporting no features has reported an empty set, which is different from the provider not
// knowing, so the attribute must be an empty map rather than null.
func TestServerInfoReportsAnEmptyFeatureMapRatherThanNull(t *testing.T) {
	clearProviderEnvironment(t)
	admin := testutil.NewAPIServer(t)
	admin.Replace("version", testutil.JSON(`{"version":"1.7.6","min_admin_api_version":2,"max_admin_api_version":4,"features":{}}`))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"admin_url":           admin.URL,
				"environment_api_key": "key_environment",
			}) + forceAdminConfigure(),
			Check: resource.TestCheckResourceAttr("data.restate_server_info.probe", "features.%", "0"),
		}},
	})
}

func TestCloudRegionsDataSource(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)
	cloud.Replace("ListRegions", testutil.JSON(`{"regions":[
	  {"regionName":"us-east-1","regionDisplayName":"US East (N. Virginia)"},
	  {"regionName":"eu-central-1"}
	]}`))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":      cloud.URL,
				"account_id":         "acc_1a2b3c",
				"management_api_key": "key_management",
			}) + forceCloudConfigure(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.restate_cloud_regions.probe", "names.#", "2"),
				resource.TestCheckResourceAttr("data.restate_cloud_regions.probe", "names.0", "us-east-1"),
				resource.TestCheckResourceAttr("data.restate_cloud_regions.probe", "names.1", "eu-central-1"),
				resource.TestCheckResourceAttr("data.restate_cloud_regions.probe", "regions.0.display_name", "US East (N. Virginia)"),
				// The display name is optional in the contract; an absent one is empty, not null.
				resource.TestCheckResourceAttr("data.restate_cloud_regions.probe", "regions.1.display_name", ""),
			),
		}},
	})
}

func TestCloudRegionsDataSourceOnAnAccountWithNoRegions(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)
	cloud.Replace("ListRegions", testutil.JSON(`{"regions":[]}`))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":      cloud.URL,
				"account_id":         "acc_1a2b3c",
				"management_api_key": "key_management",
			}) + forceCloudConfigure(),
			// An empty list, not a null one: the API answered and reported nothing.
			Check: resource.TestCheckResourceAttr("data.restate_cloud_regions.probe", "names.#", "0"),
		}},
	})
}

func TestCloudRegionsDataSourceReportsAReadFailure(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)
	cloud.Replace("ListRegions", testutil.Error(500, "internal error"))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":      cloud.URL,
				"account_id":         "acc_1a2b3c",
				"management_api_key": "key_management",
			}) + forceCloudConfigure(),
			ExpectError: regexp.MustCompile(`(?s)Could not list Restate Cloud regions.*internal error`),
		}},
	})
}

func TestCloudAccountDataSource(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)
	cloud.Replace("DescribeAccount", testutil.JSON(`{
	  "environments":[
	    {"environmentId":"env_1","region":"us-east-1","name":"prod","tierId":"BUSINESS"},
	    {"environmentId":"env_2","region":"eu-central-1","name":"staging","tierId":"FREE"}
	  ],
	  "billingMode":"account",
	  "subscription":{"status":"ACTIVE","currentPeriodStart":1756252800,"currentPeriodEnd":1758931200}
	}`))
	cloud.Replace("GetAccountLimits", testutil.JSON(`{"freeEnvironments":{"usage":1,"limit":3}}`))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":      cloud.URL,
				"account_id":         "acc_1a2b3c",
				"management_api_key": "key_management",
			}) + `
data "restate_cloud_account" "probe" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.restate_cloud_account.probe", "id", "acc_1a2b3c"),
				resource.TestCheckResourceAttr("data.restate_cloud_account.probe", "billing_mode", "account"),
				resource.TestCheckResourceAttr("data.restate_cloud_account.probe", "subscription_status", "ACTIVE"),
				resource.TestCheckResourceAttr("data.restate_cloud_account.probe", "subscription_current_period_start", "1756252800"),
				resource.TestCheckResourceAttr("data.restate_cloud_account.probe", "environments.#", "2"),
				resource.TestCheckResourceAttr("data.restate_cloud_account.probe", "environments.0.id", "env_1"),
				resource.TestCheckResourceAttr("data.restate_cloud_account.probe", "environments.0.tier", "BUSINESS"),
				resource.TestCheckResourceAttr("data.restate_cloud_account.probe", "free_environment_limit.usage", "1"),
				resource.TestCheckResourceAttr("data.restate_cloud_account.probe", "free_environment_limit.limit", "3"),
			),
		}},
	})
}

// An account with no subscription must report null periods rather than zero, which would read as a
// real billing period starting at the Unix epoch.
func TestCloudAccountDataSourceOnAnAccountWithNoSubscription(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":      cloud.URL,
				"account_id":         "acc_1a2b3c",
				"management_api_key": "key_management",
			}) + `
data "restate_cloud_account" "probe" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.restate_cloud_account.probe", "subscription_status", ""),
				resource.TestCheckNoResourceAttr("data.restate_cloud_account.probe", "subscription_current_period_start"),
				resource.TestCheckResourceAttr("data.restate_cloud_account.probe", "environments.#", "0"),
			),
		}},
	})
}

// Limits are a separate operation and an account may not be permitted to read them. That must
// degrade to a null quota with a warning rather than failing the whole read.
func TestCloudAccountDataSourceDegradesWhenLimitsAreUnreadable(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)
	cloud.Replace("GetAccountLimits", testutil.Error(403, "not authorized to read limits"))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{
				"cloud_api_url":      cloud.URL,
				"account_id":         "acc_1a2b3c",
				"management_api_key": "key_management",
			}) + `
data "restate_cloud_account" "probe" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.restate_cloud_account.probe", "billing_mode", "account"),
				resource.TestCheckNoResourceAttr("data.restate_cloud_account.probe", "free_environment_limit.usage"),
			),
		}},
	})
}

// A data source must be able to read through an explicitly aliased provider, which is how a
// configuration addresses a specific account or environment.
//
// This uses a single, alias-only provider on purpose. terraform-plugin-testing runs the provider
// in-process and serves every provider configuration from one server instance, so data sources
// declared against different aliases all see the most recently configured client. That is a harness
// limitation rather than provider behaviour -- real Terraform runs a separate plugin instance per
// configuration -- and TestAliasedProvidersAddressDifferentAccounts covers the part that is
// observable here: each alias configures its own endpoint and credential.
func TestDataSourceReadsThroughAnAliasedProvider(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudServer(t)
	cloud.Replace("ListRegions", testutil.JSON(`{"regions":[{"regionName":"ap-southeast-1"}]}`))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
provider "restate" {
  alias               = "singapore"
  allow_insecure_http = true
  cloud_api_url       = %q
  account_id          = "acc_singapore"
  management_api_key  = "key_singapore"
}

data "restate_cloud_regions" "aliased" {
  provider = restate.singapore
}
`, cloud.URL),
			Check: resource.TestCheckResourceAttr("data.restate_cloud_regions.aliased", "names.0", "ap-southeast-1"),
		}},
	})

	requests := cloud.RequestsFor("ListRegions")
	if len(requests) == 0 {
		t.Fatal("the aliased provider did not read regions")
	}
	for _, request := range requests {
		if request.Authorization != "Bearer key_singapore" {
			t.Errorf("the aliased provider sent %q, want its own credential", request.Authorization)
		}
		if request.Path != "/acc_singapore/ListRegions" {
			t.Errorf("the aliased provider addressed %q, want its own account", request.Path)
		}
	}
}
