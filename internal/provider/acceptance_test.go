package provider_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// The tests in this file talk to real APIs. They are gated so they can never run implicitly:
// resource.Test skips unless TF_ACC is set, and each test additionally skips unless the credentials
// it needs are present.
//
// They create and destroy real infrastructure. Point them only at a designated test account and a
// designated test environment.
//
// Read-only tests come first in this file and are cheap; the destructive ones are further down and
// are individually gated.

// acceptanceRunID gives every object a run-specific, prefixed name so a leaked object is obviously
// from a test and two concurrent runs cannot collide.
//
// It is derived from the clock rather than randomly so a leftover object can be traced back to when
// it was created.
var acceptanceRunID = fmt.Sprintf("tfacc-%d", time.Now().UTC().Unix())

// acceptanceName builds a name that satisfies the environment name pattern.
func acceptanceName(suffix string) string {
	name := fmt.Sprintf("%s-%s", acceptanceRunID, suffix)
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

// requireCloudCredentials skips unless the Cloud Management API credentials are configured.
func requireCloudCredentials(t *testing.T) {
	t.Helper()
	for _, name := range []string{"RESTATE_CLOUD_ACCOUNT_ID", "RESTATE_CLOUD_MANAGEMENT_API_KEY"} {
		if os.Getenv(name) == "" {
			t.Skipf("%s is not set; skipping the Cloud Management API acceptance test", name)
		}
	}
}

// requireAdminCredentials skips unless the Admin API credentials are configured.
func requireAdminCredentials(t *testing.T) {
	t.Helper()
	for _, name := range []string{"RESTATE_ADMIN_URL", "RESTATE_AUTH_TOKEN"} {
		if os.Getenv(name) == "" {
			t.Skipf("%s is not set; skipping the Admin API acceptance test", name)
		}
	}
}

// requireDestructiveOptIn skips unless the caller has explicitly said this account or environment is
// disposable. Creating an environment costs money and deleting a deployment can break in-flight
// invocations, so "the credentials are present" is not enough.
func requireDestructiveOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("RESTATE_ACC_ALLOW_DESTRUCTIVE") != "1" {
		t.Skip("RESTATE_ACC_ALLOW_DESTRUCTIVE is not 1; skipping the destructive acceptance test. " +
			"Set it only for an account and environment you are willing to have modified.")
	}
}

// acceptanceProvider is an empty provider block: everything comes from the environment, so a
// credential is never written into a test's configuration.
const acceptanceProvider = `
provider "restate" {}
`

// TestAccCloudAccountReadOnly checks that a management key can perform the read-only, account-scoped
// operations the provider relies on for credential validation.
//
// This is the first acceptance test to run against a new account or credential: it proves
// authentication and account matching without creating anything.
func TestAccCloudAccountReadOnly(t *testing.T) {
	requireCloudCredentials(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: acceptanceProvider + `
data "restate_cloud_account" "current" {}

data "restate_cloud_regions" "available" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.restate_cloud_account.current", "id", os.Getenv("RESTATE_CLOUD_ACCOUNT_ID")),
				// An account must be able to place an environment somewhere, so an empty region list
				// means something is wrong with the account or the credential's scope.
				resource.TestCheckResourceAttrWith("data.restate_cloud_regions.available", "names.#", func(value string) error {
					if value == "0" {
						return fmt.Errorf("the account reports no available regions")
					}
					return nil
				}),
			),
		}},
	})
}

// TestAccServerInfoReadOnly checks the Admin API credential and the version negotiation against a
// real server. It creates nothing.
func TestAccServerInfoReadOnly(t *testing.T) {
	requireAdminCredentials(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: acceptanceProvider + `
data "restate_server_info" "environment" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrWith("data.restate_server_info.environment", "version", func(value string) error {
					if value == "" {
						return fmt.Errorf("the server reported no version")
					}
					return nil
				}),
				// If this is zero, the compatibility gate did not find a mutually supported version,
				// and provider configuration should already have failed.
				resource.TestCheckResourceAttrWith("data.restate_server_info.environment", "negotiated_admin_api_version", func(value string) error {
					if value == "0" {
						return fmt.Errorf("no mutually supported Admin API version was negotiated")
					}
					return nil
				}),
			),
		}},
	})
}

// TestAccCloudEnvironmentLifecycle is the release gate for environment support: it must pass before
// the provider's first usable release.
//
// It creates a free-tier environment, renames it, refreshes it, imports it, and destroys it.
func TestAccCloudEnvironmentLifecycle(t *testing.T) {
	requireCloudCredentials(t)
	requireDestructiveOptIn(t)

	name := acceptanceName("env")
	renamed := acceptanceName("env2")

	config := func(environmentName string) string {
		return acceptanceProvider + fmt.Sprintf(`
resource "restate_cloud_environment" "acc" {
  name    = %q
  tier_id = "FREE"

  timeouts {
    create = "30m"
    delete = "30m"
  }
}
`, environmentName)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: config(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_environment.acc", "name", name),
					// Provisioning must have completed, not merely been accepted.
					resource.TestCheckResourceAttr("restate_cloud_environment.acc", "status", "ACTIVE"),
					// The connection metadata is what the two-stage bootstrap depends on.
					resource.TestCheckResourceAttrWith("restate_cloud_environment.acc", "admin_base_url", requireNonEmpty("admin_base_url")),
					resource.TestCheckResourceAttrWith("restate_cloud_environment.acc", "ingress_base_url", requireNonEmpty("ingress_base_url")),
					resource.TestCheckResourceAttrWith("restate_cloud_environment.acc", "signing_public_key", requireNonEmpty("signing_public_key")),
					// An unconfigured region must be persisted from the API so later plans converge.
					resource.TestCheckResourceAttrWith("restate_cloud_environment.acc", "region", requireNonEmpty("region")),
				),
			},
			{
				// A repeated plan must be empty against the live API, not just against a fake.
				Config: config(name),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: config(renamed),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_environment.acc", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("restate_cloud_environment.acc", "name", renamed),
			},
			{
				ResourceName:            "restate_cloud_environment.acc",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					instance, ok := state.RootModule().Resources["restate_cloud_environment.acc"]
					if !ok {
						return "", fmt.Errorf("restate_cloud_environment.acc is not in state")
					}
					return fmt.Sprintf("%s/%s", os.Getenv("RESTATE_CLOUD_ACCOUNT_ID"), instance.Primary.ID), nil
				},
			},
		},
	})
}

// TestAccCloudAPIKeyLifecycle checks that a key's secret arrives from create, survives a refresh that
// cannot return it, and is revoked on destroy.
func TestAccCloudAPIKeyLifecycle(t *testing.T) {
	requireCloudCredentials(t)
	requireDestructiveOptIn(t)

	name := acceptanceName("key")

	config := acceptanceProvider + fmt.Sprintf(`
resource "restate_cloud_environment" "acc" {
  name    = %q
  tier_id = "FREE"
}

resource "restate_cloud_api_key" "acc" {
  environment_id = restate_cloud_environment.acc.id
  role_id        = "rst:role::AdminAccess"
  description    = "terraform acceptance test %s"
}
`, name, acceptanceRunID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_api_key.acc", "role_id", "rst:role::AdminAccess"),
					resource.TestCheckResourceAttr("restate_cloud_api_key.acc", "state", "ACTIVE"),
					// The secret is available only from create.
					resource.TestCheckResourceAttrWith("restate_cloud_api_key.acc", "api_key", requireNonEmpty("api_key")),
				),
			},
			{
				// The refresh cannot return the secret, so state must still hold it and the plan must
				// be empty.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttrWith("restate_cloud_api_key.acc", "api_key", requireNonEmpty("api_key")),
			},
		},
	})
}

// TestAccDeploymentLifecycle registers a deployment against a real environment.
//
// It needs an endpoint the environment can reach, which the test cannot provide, so it also requires
// RESTATE_ACC_DEPLOYMENT_URI. Without it the test skips rather than failing on a discovery error that
// says nothing about the provider.
func TestAccDeploymentLifecycle(t *testing.T) {
	requireAdminCredentials(t)
	requireDestructiveOptIn(t)

	uri := os.Getenv("RESTATE_ACC_DEPLOYMENT_URI")
	if uri == "" {
		t.Skip("RESTATE_ACC_DEPLOYMENT_URI is not set; skipping. Set it to a service endpoint the target " +
			"Restate environment can reach and discover.")
	}

	config := acceptanceProvider + fmt.Sprintf(`
resource "restate_deployment" "acc" {
  http = {
    uri = %q

    metadata = {
      "terraform-acceptance-run" = %q
    }
  }

  # The test must be able to clean up after itself.
  force_destroy = true

  timeouts {
    create = "10m"
  }
}
`, uri, acceptanceRunID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_deployment.acc", "endpoint_type", "http"),
					resource.TestCheckResourceAttr("restate_deployment.acc", "http.uri", uri),
					// Registration performs discovery, so a deployment exposing no services means
					// discovery found nothing and the endpoint is not what the test assumed.
					resource.TestCheckResourceAttrWith("restate_deployment.acc", "services.#", func(value string) error {
						if value == "0" {
							return fmt.Errorf("discovery found no services at %s", uri)
						}
						return nil
					}),
				),
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName:            "restate_deployment.acc",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "force_destroy"},
			},
		},
	})
}

func requireNonEmpty(attribute string) func(string) error {
	return func(value string) error {
		if value == "" {
			return fmt.Errorf("%s is empty", attribute)
		}
		return nil
	}
}
