package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func byokKeyConfig(cloudURL, body string) string {
	return cloudProviderConfig(cloudURL) + `
resource "restate_cloud_byok_registration_key" "test" {
  cloud           = "gcp"
  provider_region = "us-central1"
` + body + "}\n"
}

// The key and the region it reserves come from the create response and are kept by every later
// refresh, because the API cannot describe a key. Destroying the resource, by default, does not touch
// the region, so a pipeline that tears down its cluster does not silently remove a registered region.
func TestByokRegistrationKeyLifecycle(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := byokKeyConfig(cloud.URL, "  region_display_name = \"customer-a us-central1\"\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: config,
				// The key is a credential for as long as it lives, so Terraform must never print it.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectSensitiveValue("restate_cloud_byok_registration_key.test", tfjsonpath.New("key")),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "id", "rgn_byok1"),
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "region_id", "rgn_byok1"),
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "account_id", testProviderAccountID),
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "key", "rrk_1.fakesecret1"),
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "expires_at", "2026-11-18T16:00:00.000Z"),
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "deregister_region_on_destroy", "false"),
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "image_pull.registry", "registry.fake.restate.cloud"),
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "image_pull.username", "restate"),
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "image_pull.password", "key_fakepull1"),
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "image_pull.repository_prefix", "registry.fake.restate.cloud/fake-"),
					func(*terraform.State) error {
						// What the key was minted for is what the registering region must report.
						if key := cloud.ByokKey("rgn_byok1"); key.Cloud != "gcp" || key.ProviderRegion != "us-central1" || key.DisplayName != "customer-a us-central1" {
							return fmt.Errorf("the key was minted for %+v", key)
						}
						return nil
					},
				),
			},
			{
				Config: config,
				// The second apply refreshes and plans against the first one's state. The key, which
				// Restate shows once, must survive, and nothing is minted again.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
						// Still sensitive once it is in the state.
						plancheck.ExpectSensitiveValue("restate_cloud_byok_registration_key.test", tfjsonpath.New("key")),
						plancheck.ExpectSensitiveValue("restate_cloud_byok_registration_key.test", tfjsonpath.New("image_pull").AtMapKey("password")),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "key", "rrk_1.fakesecret1"),
					// Restate shows the credential once, so a refresh must keep it too.
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "image_pull.password", "key_fakepull1"),
					func(*terraform.State) error {
						if got := cloud.CallCount("CreateByokRegistrationKey"); got != 1 {
							return fmt.Errorf("%d keys were minted, want 1", got)
						}
						return nil
					},
				),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if got := cloud.CallCount("DeleteByokRegion"); got != 0 {
				return fmt.Errorf("a destroy without deregister_region_on_destroy called DeleteByokRegion %d times", got)
			}
			return nil
		},
	})
}

// A control plane that predates image_pull, or a stage that serves no images, answers without it,
// and the attribute is null rather than an error.
func TestByokRegistrationKeyWithoutImagePull(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	cloud.OmitByokImagePull = true

	config := byokKeyConfig(cloud.URL, "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "key", "rrk_1.fakesecret1"),
					resource.TestCheckNoResourceAttr("restate_cloud_byok_registration_key.test", "image_pull.password"),
					resource.TestCheckNoResourceAttr("restate_cloud_byok_registration_key.test", "image_pull.registry"),
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
}

// Every argument of the request is part of what the key was minted for, so a change mints a new key,
// with a new region id. Only deregister_region_on_destroy, which lives in the state, changes in place.
func TestByokRegistrationKeyArgumentsReplaceTheKey(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: byokKeyConfig(cloud.URL, "")},
			{
				Config: byokKeyConfig(cloud.URL, "  deregister_region_on_destroy = true\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_byok_registration_key.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "region_id", "rgn_byok1"),
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "key", "rrk_1.fakesecret1"),
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "deregister_region_on_destroy", "true"),
				),
			},
			{
				Config: cloudProviderConfig(cloud.URL) + `
resource "restate_cloud_byok_registration_key" "test" {
  cloud                        = "gcp"
  provider_region              = "europe-west1"
  deregister_region_on_destroy = true
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_byok_registration_key.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "region_id", "rgn_byok2"),
					resource.TestCheckResourceAttr("restate_cloud_byok_registration_key.test", "key", "rrk_2.fakesecret2"),
				),
			},
		},
	})
}

// A replacement destroys the old resource first, so it removes the region the old key registered when
// deregister_region_on_destroy is on, and leaves it registered when it is off.
func TestByokRegistrationKeyReplacementDeregistersOnlyWhenAsked(t *testing.T) {
	for _, tt := range []struct {
		name       string
		deregister bool
	}{{"on", true}, {"off", false}} {
		t.Run(tt.name, func(t *testing.T) {
			clearProviderEnvironment(t)
			cloud := cloudFake(t)

			config := func(region string) string {
				return cloudProviderConfig(cloud.URL) + fmt.Sprintf(`
resource "restate_cloud_byok_registration_key" "test" {
  cloud                        = "gcp"
  provider_region              = %q
  deregister_region_on_destroy = %t
}
`, region, tt.deregister)
			}

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{
					{Config: config("us-central1")},
					{
						PreConfig: func() { cloud.RegisterByokRegion("rgn_byok1") },
						Config:    config("europe-west1"),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{
								plancheck.ExpectResourceAction("restate_cloud_byok_registration_key.test", plancheck.ResourceActionDestroyBeforeCreate),
							},
						},
						Check: func(*terraform.State) error {
							status := cloud.ByocRegion("rgn_byok1").Status
							calls := cloud.CallCount("DeleteByokRegion")
							switch {
							case tt.deregister && (calls != 1 || status != "DELETED"):
								return fmt.Errorf("the replacement made %d DeleteByokRegion calls and left the old region %s", calls, status)
							case !tt.deregister && (calls != 0 || status != "ACTIVE"):
								return fmt.Errorf("the replacement made %d DeleteByokRegion calls and left the old region %s", calls, status)
							}
							return nil
						},
					},
				},
			})
		})
	}
}

// With deregister_region_on_destroy a destroy removes the region the chart registered, and a key that
// no chart used reserved no region, so the API's 404 is a finished destroy rather than a failure.
func TestByokRegistrationKeyDeregistersTheRegionOnDestroy(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := byokKeyConfig(cloud.URL, "  deregister_region_on_destroy = true\n")

	t.Run("registered", func(t *testing.T) {
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: protocolProviders(),
			Steps: []resource.TestStep{
				{Config: config},
				{
					PreConfig: func() { cloud.RegisterByokRegion("rgn_byok1") },
					Config:    config,
				},
			},
			CheckDestroy: func(*terraform.State) error {
				if status := cloud.ByocRegion("rgn_byok1").Status; status != "DELETED" {
					return fmt.Errorf("region is %s after destroy", status)
				}
				return nil
			},
		})
	})

	t.Run("never registered", func(t *testing.T) {
		before := cloud.CallCount("DeleteByokRegion")
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: protocolProviders(),
			Steps:                    []resource.TestStep{{Config: config}},
			CheckDestroy: func(*terraform.State) error {
				if cloud.CallCount("DeleteByokRegion") == before {
					return fmt.Errorf("the destroy never asked Restate to remove the region")
				}
				return nil
			},
		})
	})
}

// Restate refuses to remove a region that hosts environments, which stops the destroy and keeps the
// resource, so the cluster that a dependent resource would destroy next is still there.
func TestByokRegistrationKeyDestroyStopsWhileTheRegionHostsEnvironments(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := byokKeyConfig(cloud.URL, "  deregister_region_on_destroy = true\n")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					cloud.RegisterByokRegion("rgn_byok1")
					cloud.ByocRegion("rgn_byok1").Environments = 1
				},
				Config:      config,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`(?s)rgn_byok1\s+still\s+hosts\s+environments`),
			},
			{
				PreConfig: func() { cloud.ByocRegion("rgn_byok1").Environments = 0 },
				Config:    config,
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if status := cloud.ByocRegion("rgn_byok1").Status; status != "DELETED" {
				return fmt.Errorf("region is %s after destroy", status)
			}
			return nil
		},
	})
}

func TestByokRegistrationKeyRefusesWhatTheAPIRefuses(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	cloud.ByokKeyLimit = 1

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: cloudProviderConfig(cloud.URL) + `
resource "restate_cloud_byok_registration_key" "test" {
  cloud           = "aws"
  provider_region = "eu-central-1"
}
`,
				ExpectError: regexp.MustCompile(`value must be one of: \["gcp"\]`),
			},
			{
				// The limit counts keys that no region has used, and the diagnostic says so.
				Config: byokKeyConfig(cloud.URL, "") + `
resource "restate_cloud_byok_registration_key" "second" {
  cloud           = "gcp"
  provider_region = "us-central1"
}
`,
				ExpectError: regexp.MustCompile(`(?s)most registration keys that no region has used`),
			},
		},
	})
}

func TestByokRegistrationKeyNeedsCloudCredentials(t *testing.T) {
	clearProviderEnvironment(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: providerBlock(map[string]string{}) + `
resource "restate_cloud_byok_registration_key" "test" {
  cloud           = "gcp"
  provider_region = "us-central1"
}
`,
			ExpectError: regexp.MustCompile(`restate_cloud_byok_registration_key requires Restate Cloud Management API\s+configuration`),
		}},
	})
}
