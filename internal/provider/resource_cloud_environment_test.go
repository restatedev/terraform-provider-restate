package provider_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
)

const testProviderAccountID = "acc_1a2b3c"

// cloudFake starts a stateful Cloud Management API fake. It models the environment and key
// lifecycles rather than replaying a queue of responses, which matters because both are asynchronous:
// after a destroy is accepted, describing the object must start reporting it gone, or the provider's
// correct decision to poll to a terminal state becomes a test that waits out its timeout.
func cloudFake(t *testing.T) *testutil.CloudFake {
	t.Helper()
	return testutil.NewCloudFake(t, testProviderAccountID)
}

// cloudProviderConfig renders a provider block pointed at a Cloud fake.
func cloudProviderConfig(cloudURL string) string {
	return providerBlock(map[string]string{
		"cloud_api_url":      cloudURL,
		"account_id":         testProviderAccountID,
		"management_api_key": "key_management",
	})
}

// environmentConfig renders a provider block plus one environment resource.
func environmentConfig(cloudURL, body string) string {
	return cloudProviderConfig(cloudURL) + "\nresource \"restate_cloud_environment\" \"test\" {\n" + body + "}\n"
}

func TestEnvironmentCreate(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: environmentConfig(cloud.URL, `
  name    = "production"
  region  = "us-east-1"
  tier_id = "BUSINESS"
`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("restate_cloud_environment.test", "id", "env_1"),
				resource.TestCheckResourceAttr("restate_cloud_environment.test", "account_id", testProviderAccountID),
				resource.TestCheckResourceAttr("restate_cloud_environment.test", "name", "production"),
				resource.TestCheckResourceAttr("restate_cloud_environment.test", "region", "us-east-1"),
				resource.TestCheckResourceAttr("restate_cloud_environment.test", "tier_id", "BUSINESS"),
				// A successful apply always leaves the environment active; the create response alone
				// is not proof of that, which is why the provider polls.
				resource.TestCheckResourceAttr("restate_cloud_environment.test", "status", "ACTIVE"),
				// The connection metadata comes from the describe response, not from create.
				resource.TestCheckResourceAttr("restate_cloud_environment.test", "admin_base_url", "https://env_1.env.us.restate.cloud:9070"),
				resource.TestCheckResourceAttr("restate_cloud_environment.test", "ingress_base_url", "https://env_1.env.us.restate.cloud"),
				resource.TestCheckResourceAttr("restate_cloud_environment.test", "signing_public_key", "publickeyv1_env_1"),
				resource.TestCheckResourceAttr("restate_cloud_environment.test", "region_display_name", "US East (N. Virginia)"),
				resource.TestCheckResourceAttr("restate_cloud_environment.test", "tunnel_srv", "_tunnel._tcp.us.restate.cloud"),
				// Deletion protection is off unless asked for, so a test environment can be torn down.
				resource.TestCheckResourceAttr("restate_cloud_environment.test", "deletion_protection", "false"),
			),
		}},
	})
}

// The create response is an acknowledgement, not proof of provisioning, so the provider must poll
// until the status is terminal.
func TestEnvironmentCreatePollsUntilActive(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	cloud.PendingPolls = 3

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: environmentConfig(cloud.URL, "\n  name = \"production\"\n"),
			Check:  resource.TestCheckResourceAttr("restate_cloud_environment.test", "status", "ACTIVE"),
		}},
	})

	if count := cloud.CallCount("DescribeEnvironment"); count < 3 {
		t.Errorf("DescribeEnvironment called %d times, want at least the 3 polls the fake required", count)
	}
}

// A repeated plan after apply must be empty, which is the basic convergence property.
func TestEnvironmentPlanIsEmptyAfterApply(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := environmentConfig(cloud.URL, "\n  name = \"production\"\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// With no region or tier configured, the API's choice must be persisted so later plans converge
// rather than diffing forever against a null.
func TestEnvironmentPersistsTheAPIChosenRegionAndTier(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := environmentConfig(cloud.URL, "\n  name = \"production\"\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_environment.test", "region", "us-east-1"),
					resource.TestCheckResourceAttr("restate_cloud_environment.test", "tier_id", "FREE"),
				),
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	// Nothing the user did not ask for may appear on the wire.
	var body map[string]any
	cloud.RequestsFor("CreateEnvironment")[0].DecodeBody(t, &body)
	for _, field := range []string{"region", "tierId", "payment", "profileId"} {
		if _, present := body[field]; present {
			t.Errorf("%s was sent despite not being configured: %v", field, body[field])
		}
	}
}

func TestEnvironmentRenameIsAppliedInPlace(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: environmentConfig(cloud.URL, "\n  name   = \"production\"\n  region = \"us-east-1\"\n")},
			{
				Config: environmentConfig(cloud.URL, "\n  name   = \"production-eu\"\n  region = \"us-east-1\"\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						// A rename is an in-place update: the API has UpdateEnvironment for exactly
						// this, so replacing a healthy environment would be gratuitous.
						plancheck.ExpectResourceAction("restate_cloud_environment.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_environment.test", "name", "production-eu"),
					// The same environment, not a replacement.
					resource.TestCheckResourceAttr("restate_cloud_environment.test", "id", "env_1"),
				),
			},
		},
	})

	if count := cloud.CallCount("UpdateEnvironment"); count != 1 {
		t.Errorf("UpdateEnvironment called %d times, want 1", count)
	}
	if count := cloud.CallCount("CreateEnvironment"); count != 1 {
		t.Errorf("CreateEnvironment called %d times, want 1: a rename must not replace the environment", count)
	}
}

func TestEnvironmentTierChangeIsAppliedInPlace(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: environmentConfig(cloud.URL, "\n  name    = \"production\"\n  tier_id = \"STARTER\"\n")},
			{
				Config: environmentConfig(cloud.URL, "\n  name    = \"production\"\n  tier_id = \"BUSINESS\"\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_environment.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("restate_cloud_environment.test", "tier_id", "BUSINESS"),
			},
		},
	})

	if count := cloud.CallCount("UpdateEnvironmentTier"); count != 1 {
		t.Errorf("UpdateEnvironmentTier called %d times, want 1", count)
	}
	// A rename was not requested, so the rename operation must not have been called: the two are
	// separate operations with different semantics and are applied separately.
	if count := cloud.CallCount("UpdateEnvironment"); count != 0 {
		t.Errorf("UpdateEnvironment called %d times, want 0", count)
	}
}

// The API has no region migration, so a region change is the one thing that must replace the
// environment rather than being quietly ignored.
func TestEnvironmentRegionChangeRequiresReplacement(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: environmentConfig(cloud.URL, "\n  name   = \"production\"\n  region = \"us-east-1\"\n")},
			{
				Config: environmentConfig(cloud.URL, "\n  name   = \"production\"\n  region = \"eu-central-1\"\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_environment.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_environment.test", "region", "eu-central-1"),
					// A genuinely new environment.
					resource.TestCheckResourceAttr("restate_cloud_environment.test", "id", "env_2"),
				),
			},
		},
	})

	// Two creates prove the environment was genuinely replaced rather than mutated. The destroy
	// count is not asserted: the replacement destroys the first environment and the test harness's
	// own cleanup destroys the second.
	if count := cloud.CallCount("CreateEnvironment"); count != 2 {
		t.Errorf("CreateEnvironment called %d times, want 2", count)
	}
}

// A tier the API cannot be asked for must be rejected at plan time, not sent and refused.
func TestEnvironmentRejectsAnUnrequestableTier(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config:      environmentConfig(cloud.URL, "\n  name    = \"production\"\n  tier_id = \"CUSTOM\"\n"),
			ExpectError: regexp.MustCompile(`Attribute tier_id value must be one of`),
		}},
	})

	if cloud.CallCount("CreateEnvironment") != 0 {
		t.Error("a create was sent for an unrequestable tier")
	}
}

func TestEnvironmentRejectsAnInvalidName(t *testing.T) {
	for _, name := range []string{
		"Production",            // uppercase
		"-production",           // leading hyphen
		"production-",           // trailing hyphen
		"a",                     // too short
		"prod_uction",           // underscore
		strings.Repeat("a", 64), // too long
	} {
		t.Run(name, func(t *testing.T) {
			clearProviderEnvironment(t)
			cloud := cloudFake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{{
					Config: environmentConfig(cloud.URL, fmt.Sprintf("\n  name = %q\n", name)),
					// The framework wraps the message, so match the diagnostic summary and the
					// distinctive start of the detail rather than a phrase a line break can split.
					ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Value Match.*Attribute name must be 2 to 63 characters`),
				}},
			})

			if cloud.CallCount("CreateEnvironment") != 0 {
				t.Error("a create was sent for an invalid name")
			}
		})
	}
}

// Terraform cannot complete a browser payment flow. The apply must fail, and the environment ID must
// reach state so a half-provisioned environment can be destroyed rather than found by hand.
func TestEnvironmentCreateRefusesInteractiveCheckout(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	cloud.CheckoutURLOnCreate = "https://checkout.stripe.com/c/pay/cs_test_abc"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config:      environmentConfig(cloud.URL, "\n  name    = \"production\"\n  tier_id = \"BUSINESS\"\n"),
			ExpectError: regexp.MustCompile(`(?s)requires interactive payment.*env_1`),
		}},
	})
}

// A tier change that needs a browser must fail loudly, and state must not report a tier that was
// never applied.
func TestEnvironmentTierChangeRefusesInteractiveCheckout(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: environmentConfig(cloud.URL, "\n  name    = \"production\"\n  tier_id = \"STARTER\"\n")},
			{
				PreConfig: func() {
					cloud.CheckoutURLOnTierChange = "https://checkout.stripe.com/c/pay/cs_test_abc"
				},
				Config:      environmentConfig(cloud.URL, "\n  name    = \"production\"\n  tier_id = \"PREMIUM\"\n"),
				ExpectError: regexp.MustCompile(`(?s)requires interactive payment`),
			},
			{
				// The tier was not applied, so a plan against the original configuration must be
				// empty: state was re-read after the partial update rather than taking the plan's
				// word for it.
				PreConfig: func() { cloud.CheckoutURLOnTierChange = "" },
				Config:    environmentConfig(cloud.URL, "\n  name    = \"production\"\n  tier_id = \"STARTER\"\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr("restate_cloud_environment.test", "tier_id", "STARTER"),
			},
		},
	})
}

// Provisioning that fails must not be waited out to the timeout, and the failure must name the
// environment so it can be cleaned up.
func TestEnvironmentCreateReportsFailedProvisioning(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	cloud.FailProvisioning = true

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config:      environmentConfig(cloud.URL, "\n  name = \"production\"\n"),
			ExpectError: regexp.MustCompile(`(?s)did not finish provisioning.*env_1.*failed to provision`),
		}},
	})

	// The failure must be immediate rather than waiting out the create timeout. Two describes is the
	// expected total: one that observed FAILED, and one from the harness refreshing the recorded
	// orphan before cleaning it up. Anything more would mean the FAILED status was being polled
	// through.
	if count := cloud.CallCount("DescribeEnvironment"); count > 2 {
		t.Errorf("DescribeEnvironment called %d times, want at most 2: a FAILED status must not be waited out", count)
	}
}

// An environment deleted out of band must be removed from state so the next plan recreates it,
// rather than failing every refresh.
func TestEnvironmentRefreshRemovesADeletedEnvironment(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := environmentConfig(cloud.URL, "\n  name = \"production\"\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					// The API reports a recently destroyed environment as DELETED rather than 404,
					// and both must be treated as absence.
					cloud.Environment("env_1").Status = "DELETED"
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_environment.test", plancheck.ResourceActionCreate),
					},
				},
				Check: resource.TestCheckResourceAttr("restate_cloud_environment.test", "id", "env_2"),
			},
		},
	})
}

// An out-of-band rename is real drift and must show up as a plan to rename it back.
func TestEnvironmentRefreshDetectsAnOutOfBandRename(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := environmentConfig(cloud.URL, "\n  name = \"production\"\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() { cloud.Environment("env_1").Name = "renamed-elsewhere" },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_environment.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("restate_cloud_environment.test", "name", "production"),
			},
		},
	})
}

// An out-of-band tier change is drift too, and the provider must plan to put it back rather than
// adopting it silently.
func TestEnvironmentRefreshDetectsAnOutOfBandTierChange(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := environmentConfig(cloud.URL, "\n  name    = \"production\"\n  tier_id = \"STARTER\"\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() { cloud.Environment("env_1").Tier = "PREMIUM" },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_environment.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("restate_cloud_environment.test", "tier_id", "STARTER"),
			},
		},
	})
}

func TestEnvironmentDeletionProtectionBlocksDestroy(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	protected := environmentConfig(cloud.URL, "\n  name                = \"production\"\n  deletion_protection = true\n")
	unprotected := environmentConfig(cloud.URL, "\n  name                = \"production\"\n  deletion_protection = false\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: protected,
				Check:  resource.TestCheckResourceAttr("restate_cloud_environment.test", "deletion_protection", "true"),
			},
			{
				Config:      protected,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`(?s)protected from deletion.*deletion_protection = false`),
			},
			{
				// Clearing the guard is a separate, deliberate apply, which is the point: an
				// accidental destroy cannot also clear its own protection.
				//
				// The refused destroy above must not have reached the API at all, and this is the
				// last moment that can be observed: the harness destroys the environment for real
				// once this step's state is in place.
				PreConfig: func() {
					if got := cloud.CallCount("DestroyEnvironment"); got != 0 {
						t.Errorf("DestroyEnvironment was called %d times while protection was on", got)
					}
					if got := cloud.EnvironmentCount(); got != 1 {
						t.Errorf("EnvironmentCount = %d, want the protected environment to have survived", got)
					}
				},
				Config: unprotected,
			},
		},
	})

	// The environment is gone by now, because the last step cleared the guard and the harness then
	// destroyed it. What matters is the assertion inside the steps above: the refused destroy sent no
	// request at all, which is checked by destroysBeforeUnprotecting.
	if got := cloud.EnvironmentCount(); got != 0 {
		t.Errorf("EnvironmentCount = %d after the test's own cleanup, want 0", got)
	}
}

// Destruction is asynchronous, so returning early would let a dependent resource race the teardown.
func TestEnvironmentDestroyWaitsForTerminalState(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := environmentConfig(cloud.URL, "\n  name = \"production\"\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{Config: config, Destroy: true},
		},
	})

	if cloud.CallCount("DestroyEnvironment") != 1 {
		t.Errorf("DestroyEnvironment called %d times, want 1", cloud.CallCount("DestroyEnvironment"))
	}
	if got := cloud.EnvironmentCount(); got != 0 {
		t.Errorf("EnvironmentCount = %d, want 0", got)
	}
}

func TestEnvironmentImport(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: environmentConfig(cloud.URL, "\n  name    = \"production\"\n  region  = \"us-east-1\"\n  tier_id = \"BUSINESS\"\n")},
			{
				ResourceName:  "restate_cloud_environment.test",
				ImportState:   true,
				ImportStateId: testProviderAccountID + "/env_1",
				// Import must populate enough state that the next plan is empty. Deletion protection
				// has no server-side representation, so it comes from the schema default, which
				// matches this configuration.
				ImportStateVerify: true,
				// The timeouts block is provider-side configuration with nothing to import from.
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
		},
	})
}

func TestEnvironmentImportRejectsAMalformedID(t *testing.T) {
	for _, id := range []string{"env_1", "acc_1/env_1/extra", "acc_1/", "/env_1"} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			clearProviderEnvironment(t)
			cloud := cloudFake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{
					{Config: environmentConfig(cloud.URL, "\n  name = \"production\"\n")},
					{
						ResourceName:  "restate_cloud_environment.test",
						ImportState:   true,
						ImportStateId: id,
						ExpectError:   regexp.MustCompile(`(?s)Invalid import ID|does not match the configured account`),
					},
				},
			})
		})
	}
}

// Importing into a provider configured for a different account must be refused, not silently adopt
// an environment from elsewhere.
func TestEnvironmentImportRejectsAMismatchedAccount(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: environmentConfig(cloud.URL, "\n  name = \"production\"\n")},
			{
				ResourceName:  "restate_cloud_environment.test",
				ImportState:   true,
				ImportStateId: "acc_somebody_else/env_1",
				ExpectError:   regexp.MustCompile(`(?s)does not match the configured account.*acc_somebody_else`),
			},
		},
	})
}

// Every computed attribute is server-derived with no corresponding mutation, so none of them may be
// configurable. Setting one has to be a configuration error rather than a silently ignored value.
func TestEnvironmentComputedAttributesAreNotConfigurable(t *testing.T) {
	for _, attribute := range []string{
		"status", "signing_public_key", "ingress_base_url", "admin_base_url",
		"invocation_metrics_url", "tunnel_base_url", "tunnel_srv", "proxy_base_url",
		"region_display_name", "account_id", "id",
	} {
		t.Run(attribute, func(t *testing.T) {
			clearProviderEnvironment(t)
			cloud := cloudFake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{{
					Config:      environmentConfig(cloud.URL, fmt.Sprintf("\n  name = \"production\"\n  %s = \"anything\"\n", attribute)),
					ExpectError: regexp.MustCompile(`(?s)Value Conflicts with Configured Attribute|not expected here|read-only`),
				}},
			})
		})
	}
}

// The environment data source must read an environment the configuration does not manage, which is
// how stage 2 of a two-stage bootstrap finds its admin URL.
func TestEnvironmentDataSource(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: environmentConfig(cloud.URL, "\n  name    = \"production\"\n  tier_id = \"BUSINESS\"\n") + `
data "restate_cloud_environment" "looked_up" {
  id = restate_cloud_environment.test.id
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.restate_cloud_environment.looked_up", "name", "production"),
				resource.TestCheckResourceAttr("data.restate_cloud_environment.looked_up", "tier_id", "BUSINESS"),
				resource.TestCheckResourceAttr("data.restate_cloud_environment.looked_up", "status", "ACTIVE"),
				resource.TestCheckResourceAttr("data.restate_cloud_environment.looked_up", "admin_base_url", "https://env_1.env.us.restate.cloud:9070"),
				resource.TestCheckResourceAttr("data.restate_cloud_environment.looked_up", "account_id", testProviderAccountID),
			),
		}},
	})
}

// A data source whose target does not exist is an error, not an empty result: something in the
// configuration depends on it.
func TestEnvironmentDataSourceReportsAMissingEnvironment(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: cloudProviderConfig(cloud.URL) + `
data "restate_cloud_environment" "missing" {
  id = "env_nonexistent"
}
`,
			ExpectError: regexp.MustCompile(`(?s)Restate Cloud environment not found.*env_nonexistent`),
		}},
	})
}
