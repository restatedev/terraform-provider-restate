package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// apiKeyConfig renders a provider block, an environment for the key to belong to, and the key.
//
// The environment is real rather than a hard-coded ID so the fake can reject a key for an
// environment that does not exist, and so the dependency ordering the resources actually have is
// exercised.
func apiKeyConfig(cloudURL, body string) string {
	return cloudProviderConfig(cloudURL) + `
resource "restate_cloud_environment" "test" {
  name = "production"
}

resource "restate_cloud_api_key" "test" {
  environment_id = restate_cloud_environment.test.id
` + body + "}\n"
}

func TestAPIKeyCreateReturnsTheSecret(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: apiKeyConfig(cloud.URL, `
  role_id     = "rst:role::AdminAccess"
  description = "managed by terraform"
`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("restate_cloud_api_key.test", "id", "key_1"),
				resource.TestCheckResourceAttr("restate_cloud_api_key.test", "account_id", testProviderAccountID),
				resource.TestCheckResourceAttr("restate_cloud_api_key.test", "environment_id", "env_1"),
				resource.TestCheckResourceAttr("restate_cloud_api_key.test", "role_id", "rst:role::AdminAccess"),
				resource.TestCheckResourceAttr("restate_cloud_api_key.test", "state", "ACTIVE"),
				resource.TestCheckResourceAttr("restate_cloud_api_key.test", "description", "managed by terraform"),
				// The secret comes from the create response and nowhere else.
				resource.TestCheckResourceAttr("restate_cloud_api_key.test", "api_key", "key_secret_1"),
			),
		}},
	})
}

func TestAPIKeyCreateMismatchRetainsStateForRevocation(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	cloud.CreatedAPIKeyRoleOverride = "rst:role::DeveloperAccess"

	config := cloudProviderConfig(cloud.URL) + `
resource "restate_cloud_api_key" "test" {
  environment_id = "env_existing"
  role_id        = "rst:role::AdminAccess"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`(?s)did not apply the requested value.*DeveloperAccess`),
		}},
	})

	if got := cloud.KeyCount(); got != 0 {
		t.Errorf("KeyCount = %d, want 0: failed create should retain state for revocation", got)
	}
}

// Describe never returns the secret, so a refresh must keep what state already holds. Overwriting it
// would destroy the only copy of a live credential.
func TestAPIKeyRefreshPreservesTheSecret(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := apiKeyConfig(cloud.URL, "\n  role_id = \"rst:role::AdminAccess\"\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("restate_cloud_api_key.test", "api_key", "key_secret_1"),
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				// After a refresh whose response contained no secret, the stored one must survive.
				Check: resource.TestCheckResourceAttr("restate_cloud_api_key.test", "api_key", "key_secret_1"),
			},
		},
	})

	if cloud.CallCount("DescribeApiKey") == 0 {
		t.Error("the key was never refreshed, so secret preservation was not exercised")
	}
}

// An absent description must stay null rather than becoming an empty string, or a key with no
// description would show a permanent diff.
func TestAPIKeyWithNoDescriptionConverges(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := apiKeyConfig(cloud.URL, "\n  role_id = \"rst:role::AdminAccess\"\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckNoResourceAttr("restate_cloud_api_key.test", "description"),
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAPIKeyRejectsAnExplicitEmptyDescription(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: apiKeyConfig(cloud.URL, `
  role_id     = "rst:role::AdminAccess"
  description = ""
`),
			ExpectError: regexp.MustCompile(`(?s)description.*at least 1`),
		}},
	})
}

// The API has no key update operation, so every configurable change must replace the key rather than
// being silently ignored. Replacement mints a new secret, which is exactly why it must be visible.
func TestAPIKeyChangesRequireReplacement(t *testing.T) {
	for _, tt := range []struct {
		name   string
		before string
		after  string
	}{
		{
			name:   "role",
			before: "\n  role_id = \"rst:role::AdminAccess\"\n",
			after:  "\n  role_id = \"rst:role::FullAccess\"\n",
		},
		{
			// The description looks harmless, but the contract has no way to edit it, so the honest
			// plan is a replacement rather than a no-op that leaves state disagreeing with reality.
			name:   "description",
			before: "\n  role_id = \"rst:role::AdminAccess\"\n",
			after:  "\n  role_id     = \"rst:role::AdminAccess\"\n  description = \"now labelled\"\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearProviderEnvironment(t)
			cloud := cloudFake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{
					{
						Config: apiKeyConfig(cloud.URL, tt.before),
						Check:  resource.TestCheckResourceAttr("restate_cloud_api_key.test", "api_key", "key_secret_1"),
					},
					{
						Config: apiKeyConfig(cloud.URL, tt.after),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{
								plancheck.ExpectResourceAction("restate_cloud_api_key.test", plancheck.ResourceActionDestroyBeforeCreate),
							},
						},
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttr("restate_cloud_api_key.test", "id", "key_2"),
							// A replacement mints a new secret and revokes the old one.
							resource.TestCheckResourceAttr("restate_cloud_api_key.test", "api_key", "key_secret_2"),
						),
					},
				},
			})
		})
	}
}

// Changing the environment replaces the key too: a key is minted for one environment and cannot move.
func TestAPIKeyEnvironmentChangeRequiresReplacement(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	before := cloudProviderConfig(cloud.URL) + `
resource "restate_cloud_environment" "first" {
  name = "production"
}

resource "restate_cloud_environment" "second" {
  name = "staging"
}

resource "restate_cloud_api_key" "test" {
  environment_id = restate_cloud_environment.first.id
  role_id        = "rst:role::AdminAccess"
}
`
	after := cloudProviderConfig(cloud.URL) + `
resource "restate_cloud_environment" "first" {
  name = "production"
}

resource "restate_cloud_environment" "second" {
  name = "staging"
}

resource "restate_cloud_api_key" "test" {
  environment_id = restate_cloud_environment.second.id
  role_id        = "rst:role::AdminAccess"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: before},
			{
				Config: after,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_api_key.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
		},
	})
}

// The account-scoped management role is the provider's own credential. Granting it to an environment
// key would be a privilege escalation, so it must be rejected at plan time.
func TestAPIKeyRejectsTheManagementRole(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config:      apiKeyConfig(cloud.URL, "\n  role_id = \"rst:role::ManagementApiAccess\"\n"),
			ExpectError: regexp.MustCompile(`Attribute role_id value must be one of`),
		}},
	})

	if cloud.CallCount("CreateApiKey") != 0 {
		t.Error("a create was sent for the management role")
	}
}

func TestAPIKeyRejectsAnUnknownRole(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config:      apiKeyConfig(cloud.URL, "\n  role_id = \"AdminAccess\"\n"),
			ExpectError: regexp.MustCompile(`Attribute role_id value must be one of`),
		}},
	})
}

func TestAPIKeyAcceptsEveryEnvironmentRole(t *testing.T) {
	for _, role := range []string{
		"rst:role::FullAccess",
		"rst:role::IngressAccess",
		"rst:role::AdminAccess",
		"rst:role::CompleteAwakeableAccess",
		"rst:role::TunnelAccess",
	} {
		t.Run(role, func(t *testing.T) {
			clearProviderEnvironment(t)
			cloud := cloudFake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{{
					Config: apiKeyConfig(cloud.URL, fmt.Sprintf("\n  role_id = %q\n", role)),
					Check:  resource.TestCheckResourceAttr("restate_cloud_api_key.test", "role_id", role),
				}},
			})
		})
	}
}

// A key revoked out of band must be removed from state so the next plan mints a replacement.
func TestAPIKeyRefreshRemovesADeletedKey(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := apiKeyConfig(cloud.URL, "\n  role_id = \"rst:role::AdminAccess\"\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					// The API reports a revoked key as DELETED rather than 404, and both must count
					// as absence.
					cloud.Key("key_1").State = "DELETED"
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_api_key.test", plancheck.ResourceActionCreate),
					},
				},
				Check: resource.TestCheckResourceAttr("restate_cloud_api_key.test", "id", "key_2"),
			},
		},
	})
}

// An out-of-band role change is real drift and must plan a replacement, since the role cannot be
// updated in place.
func TestAPIKeyRefreshDetectsAnOutOfBandRoleChange(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := apiKeyConfig(cloud.URL, "\n  role_id = \"rst:role::AdminAccess\"\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() { cloud.Key("key_1").Role = "rst:role::IngressAccess" },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_api_key.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: resource.TestCheckResourceAttr("restate_cloud_api_key.test", "role_id", "rst:role::AdminAccess"),
			},
		},
	})
}

// Import can recover everything except the secret, which the API never returns.
func TestAPIKeyImportCannotRecoverTheSecret(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: apiKeyConfig(cloud.URL, "\n  role_id = \"rst:role::AdminAccess\"\n")},
			{
				ResourceName:  "restate_cloud_api_key.test",
				ImportState:   true,
				ImportStateId: testProviderAccountID + "/env_1/key_1",
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("got %d imported instances, want 1", len(states))
					}
					state := states[0]
					if got := state.Attributes["id"]; got != "key_1" {
						return fmt.Errorf("id = %q, want key_1", got)
					}
					if got := state.Attributes["role_id"]; got != "rst:role::AdminAccess" {
						return fmt.Errorf("role_id = %q", got)
					}
					if got := state.Attributes["environment_id"]; got != "env_1" {
						return fmt.Errorf("environment_id = %q", got)
					}
					// The secret is genuinely unrecoverable, so null is the only honest value.
					if got, present := state.Attributes["api_key"]; present && got != "" {
						return fmt.Errorf("api_key = %q, want it absent: the API cannot return a key's secret", got)
					}
					return nil
				},
			},
		},
	})
}

func TestAPIKeyImportRejectsAMalformedID(t *testing.T) {
	for _, id := range []string{"key_1", "acc_1/key_1", "acc_1/env_1/key_1/extra", "acc_1//key_1"} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			clearProviderEnvironment(t)
			cloud := cloudFake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{
					{Config: apiKeyConfig(cloud.URL, "\n  role_id = \"rst:role::AdminAccess\"\n")},
					{
						ResourceName:  "restate_cloud_api_key.test",
						ImportState:   true,
						ImportStateId: id,
						ExpectError:   regexp.MustCompile(`(?s)Invalid import ID|does not match the configured account`),
					},
				},
			})
		})
	}
}

func TestAPIKeyImportRejectsAMismatchedAccount(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: apiKeyConfig(cloud.URL, "\n  role_id = \"rst:role::AdminAccess\"\n")},
			{
				ResourceName:  "restate_cloud_api_key.test",
				ImportState:   true,
				ImportStateId: "acc_somebody_else/env_1/key_1",
				ExpectError:   regexp.MustCompile(`(?s)does not match the configured account`),
			},
		},
	})
}

// Declaring a sensitive attribute in a non-sensitive output must be refused by Terraform, which is
// the observable proof that the schema marks it sensitive.
func TestAPIKeySecretCannotLeakIntoANonSensitiveOutput(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: apiKeyConfig(cloud.URL, "\n  role_id = \"rst:role::AdminAccess\"\n") + `
output "leaked" {
  value = restate_cloud_api_key.test.api_key
}
`,
			ExpectError: regexp.MustCompile(`(?s)Output refers to sensitive values`),
		}},
	})
}

func TestAPIKeySecretIsUsableThroughASensitiveOutput(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: apiKeyConfig(cloud.URL, "\n  role_id = \"rst:role::AdminAccess\"\n") + `
output "secret" {
  value     = restate_cloud_api_key.test.api_key
  sensitive = true
}
`,
			Check: resource.TestCheckResourceAttr("restate_cloud_api_key.test", "api_key", "key_secret_1"),
		}},
	})
}

// Destroying the key revokes it, which is the operation's whole purpose and needs no separate opt-in.
func TestAPIKeyDestroyRevokesTheKey(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := apiKeyConfig(cloud.URL, "\n  role_id = \"rst:role::AdminAccess\"\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{Config: config, Destroy: true},
		},
	})

	if got := cloud.KeyCount(); got != 0 {
		t.Errorf("KeyCount = %d, want 0", got)
	}
}
