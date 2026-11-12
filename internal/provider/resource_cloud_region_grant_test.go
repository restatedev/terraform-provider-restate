package provider_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
)

// managedByocConfig is the whole region grant flow, with a static map standing in for the grant
// module's output. The region is unprotected unless regionArguments says otherwise, so a test can
// destroy it.
func managedByocConfig(cloudURL, owner, regionArguments string) string {
	if regionArguments == "" {
		regionArguments = "deletion_protection = false"
	}
	return cloudProviderConfig(cloudURL) + fmt.Sprintf(`
resource "restate_cloud_region_grant" "aws" {
  owner            = %q
  cloud            = "aws"
  cloud_account_id = "123456789012"
}

resource "restate_cloud_region_grant_confirmation" "aws" {
  grant_id = restate_cloud_region_grant.aws.id
  grant = {
    roleArn             = "arn:aws:iam::123456789012:role/restate-cloud-${restate_cloud_region_grant.aws.resource_prefix}"
    permissionsBoundary = "arn:aws:iam::123456789012:policy/boundary"
  }
}

resource "restate_cloud_byoc_region" "eu" {
  name         = "prod-eu"
  grant_id     = restate_cloud_region_grant_confirmation.aws.grant_id
  cloud_region = "eu-central-1"
  %s
}
`, owner, regionArguments)
}

func TestManagedByocRegionLifecycle(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: managedByocConfig(cloud.URL, "ACCOUNT", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_region_grant.aws", "id", "rgr_1"),
					resource.TestCheckResourceAttr("restate_cloud_region_grant.aws", "owner_id", testProviderAccountID),
					resource.TestCheckResourceAttr("restate_cloud_region_grant.aws", "grant_subject", "grant:rgr_1"),
					resource.TestCheckResourceAttr("restate_cloud_region_grant.aws", "resource_prefix", "rgr-1-"),
					resource.TestCheckResourceAttr("restate_cloud_region_grant.aws", "restate_issuer", "https://issuer.restate.cloud"),
					resource.TestCheckResourceAttr("restate_cloud_region_grant_confirmation.aws", "status", "READY"),
					resource.TestCheckResourceAttr("restate_cloud_byoc_region.eu", "id", "rgn_1"),
					resource.TestCheckResourceAttr("restate_cloud_byoc_region.eu", "status", "ACTIVE"),
					// The region's cloud account comes from the grant, not from the configuration.
					resource.TestCheckResourceAttr("restate_cloud_byoc_region.eu", "cloud", "aws"),
					resource.TestCheckResourceAttr("restate_cloud_byoc_region.eu", "cloud_account_id", "123456789012"),
					resource.TestCheckResourceAttr("restate_cloud_byoc_region.eu", "display_name", "prod-eu"),
					func(*terraform.State) error {
						if output := cloud.Grant("rgr_1").Output; output["permissionsBoundary"] != "arn:aws:iam::123456789012:policy/boundary" {
							return fmt.Errorf("the grant was confirmed with %v", output)
						}
						return nil
					},
				),
			},
			{
				// The grant's status moved to READY after its create; a refresh must not turn that into a
				// change.
				Config: managedByocConfig(cloud.URL, "ACCOUNT", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if status := cloud.ByocRegion("rgn_1").Status; status != "DELETED" {
				return fmt.Errorf("region is %s after destroy", status)
			}
			if status := cloud.Grant("rgr_1").Status; status != "DELETED" {
				return fmt.Errorf("grant is %s after destroy", status)
			}
			return nil
		},
	})
}

// An organization's region grant key cannot describe the account, but manages the organization's
// grants and regions. It reaches nothing else, which the provider says plainly.
func TestOrganizationRegionGrantKey(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	cloud.OrganizationKey = true

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: managedByocConfig(cloud.URL, "ORGANIZATION", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_region_grant.aws", "owner", "ORGANIZATION"),
					resource.TestCheckResourceAttr("restate_cloud_region_grant.aws", "owner_id", "org_1"),
					resource.TestCheckResourceAttr("restate_cloud_byoc_region.eu", "status", "ACTIVE"),
				),
			},
			{
				Config:      managedByocConfig(cloud.URL, "ORGANIZATION", "") + "\nresource \"restate_cloud_environment\" \"e\" {\n  name = \"prod\"\n}\n",
				ExpectError: regexp.MustCompile(`needs an account's management API key`),
			},
		},
	})
}

// Destroying the confirmation deletes the grant at Restate, which refuses while a region still uses
// it. The refusal must stop the destroy before the cloud side of the grant is removed.
func TestConfirmationDestroyStopsWhileARegionUsesTheGrant(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	grantOnly := cloudProviderConfig(cloud.URL) + `
resource "restate_cloud_region_grant" "aws" {
  cloud            = "aws"
  cloud_account_id = "123456789012"
}

resource "restate_cloud_region_grant_confirmation" "aws" {
  grant_id = restate_cloud_region_grant.aws.id
  grant    = { roleArn = "arn:aws:iam::123456789012:role/r" }
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: grantOnly},
			{
				// A region that another configuration built with the grant.
				PreConfig: func() {
					if _, err := cloud.Client().Post(cloud.URL+"/"+testProviderAccountID+"/CreateRegion", "application/json",
						strings.NewReader(`{"name":"elsewhere","cloud":"aws","cloudRegion":"us-east-1","aws":{"accountId":"123456789012"},"grantId":"rgr_1"}`)); err != nil {
						t.Fatal(err)
					}
				},
				Config:      grantOnly,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`(?s)Restate kept the region grant.*Regions still use region grant rgr_1.*rgn_1`),
			},
			{
				PreConfig: func() { cloud.ByocRegion("rgn_1").Status = "DELETED" },
				Config:    grantOnly,
			},
		},
	})
}

// A grant that expired, or was deleted out of band, leaves the state on refresh, so the next apply
// creates a new grant and confirms it.
func TestRegionGrantThatEndedIsCreatedAgain(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := cloudProviderConfig(cloud.URL) + `
resource "restate_cloud_region_grant" "gcp" {
  cloud            = "gcp"
  cloud_account_id = "acme-restate"
}

resource "restate_cloud_region_grant_confirmation" "gcp" {
  grant_id = restate_cloud_region_grant.gcp.id
  grant    = { serviceAccount = "sa-${restate_cloud_region_grant.gcp.resource_prefix}" }
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() { cloud.Grant("rgr_1").Status = "EXPIRED" },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_cloud_region_grant.gcp", "id", "rgr_2"),
					resource.TestCheckResourceAttr("restate_cloud_region_grant_confirmation.gcp", "status", "READY"),
				),
			},
		},
	})
}

func TestRegionGrantValidatesTheCloudAccount(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: cloudProviderConfig(cloud.URL) + `
resource "restate_cloud_region_grant" "aws" {
  cloud            = "aws"
  cloud_account_id = "acme-restate"
}
`,
			ExpectError: regexp.MustCompile(`is not an AWS account ID of 12 digits`),
		}},
	})
}

// A region on a grant that is not confirmed yet would be refused by the API with a bare 409; the
// provider says what to do instead.
func TestByocRegionNeedsAConfirmedGrant(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: cloudProviderConfig(cloud.URL) + `
resource "restate_cloud_region_grant" "aws" {
  cloud            = "aws"
  cloud_account_id = "123456789012"
}

resource "restate_cloud_byoc_region" "eu" {
  name         = "prod-eu"
  grant_id     = restate_cloud_region_grant.aws.id
  cloud_region = "eu-central-1"
}
`,
			ExpectError: regexp.MustCompile(`(?s)rgr_1 is PENDING.*restate_cloud_region_grant_confirmation`),
		}},
	})
}

func TestByocRegionDeletionProtection(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	protectedRegion := managedByocConfig(cloud.URL, "ACCOUNT", "deletion_protection = true")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: protectedRegion},
			{
				Config:      protectedRegion,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`protected from deletion`),
			},
			{
				// Clearing the flag is an in-place update, not a replacement of the region.
				Config: managedByocConfig(cloud.URL, "ACCOUNT", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_byoc_region.eu", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
}

// A changed module output is confirmed again in place, and Restate refuses it. Replacing the
// confirmation instead would delete the grant that regions depend on.
func TestConfirmationRefusesAnotherOutputInPlace(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	config := func(role string) string {
		return cloudProviderConfig(cloud.URL) + fmt.Sprintf(`
resource "restate_cloud_region_grant" "aws" {
  cloud            = "aws"
  cloud_account_id = "123456789012"
}

resource "restate_cloud_region_grant_confirmation" "aws" {
  grant_id = restate_cloud_region_grant.aws.id
  grant    = { roleArn = %q }
}
`, role)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config("arn:aws:iam::123456789012:role/first")},
			{
				Config: config("arn:aws:iam::123456789012:role/second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_cloud_region_grant_confirmation.aws", plancheck.ResourceActionUpdate),
					},
				},
				ExpectError: regexp.MustCompile(`(?s)another\s+output.*replace\s+the\s+restate_cloud_region_grant.*HTTP 409`),
			},
			{
				// The grant survived the refused change.
				Config: config("arn:aws:iam::123456789012:role/first"),
				Check: func(*terraform.State) error {
					if status := cloud.Grant("rgr_1").Status; status != "READY" {
						return fmt.Errorf("grant is %s", status)
					}
					return nil
				},
			},
		},
	})
}

func TestImportRegionGrantAndConfirmation(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	config := managedByocConfig(cloud.URL, "ACCOUNT", "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config:            config,
				ResourceName:      "restate_cloud_region_grant.aws",
				ImportState:       true,
				ImportStateId:     testProviderAccountID + "/rgr_1",
				ImportStateVerify: true,
				// The confirmation and the region change these after the grant's own create, and the
				// state compared against was written by that create.
				ImportStateVerifyIgnore: []string{"status", "status_reason", "expires_at", "region_ids"},
			},
			{
				Config:            config,
				ResourceName:      "restate_cloud_byoc_region.eu",
				ImportState:       true,
				ImportStateId:     testProviderAccountID + "/rgn_1",
				ImportStateVerify: true,
				// Restate does not store deletion_protection, so an import takes the protected default.
				ImportStateVerifyIgnore: []string{"timeouts", "deletion_protection"},
			},
			{
				Config:                  config,
				ResourceName:            "restate_cloud_region_grant_confirmation.aws",
				ImportState:             true,
				ImportStateId:           "rgr_1",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"grant", "timeouts"},
			},
		},
	})
}

// Restate gave up signing in through the cloud grant: the apply fails with the cloud's answer, and
// no region is built on a grant that is not READY.
func TestConfirmationReportsAFailedSignIn(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	cloud.ConfirmFailure = "AccessDenied: not authorized to perform sts:AssumeRoleWithWebIdentity"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config:      managedByocConfig(cloud.URL, "ACCOUNT", ""),
			ExpectError: regexp.MustCompile(`(?s)kept refusing.*AssumeRoleWithWebIdentity`),
		}},
	})
	if cloud.ByocRegion("rgn_1") != nil {
		t.Error("a region was built on a grant that is not READY")
	}
}

// Restate answers 404 for a grant of another account as it does for a deleted one. A provider pointed
// at another account must not forget a live grant, or a destroy could remove its cloud side.
func TestRegionGrantReadThroughAnotherAccountIsAnError(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	config := cloudProviderConfig(cloud.URL) + `
resource "restate_cloud_region_grant" "aws" {
  cloud            = "aws"
  cloud_account_id = "123456789012"
}
`
	other := testutil.NewCloudFake(t, "acc_other")
	otherConfig := providerBlock(map[string]string{
		"cloud_api_url":      other.URL,
		"account_id":         "acc_other",
		"management_api_key": "key_management",
	}) + config[strings.Index(config, "resource "):]

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config:      otherConfig,
				ExpectError: regexp.MustCompile(`(?s)not found through this account.*acc_1a2b3c`),
			},
			{Config: config},
		},
	})
}
