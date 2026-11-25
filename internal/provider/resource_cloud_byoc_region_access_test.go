package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
)

const (
	accessResource  = "restate_cloud_byoc_region_access.eu"
	testServiceName = "com.amazonaws.vpce.eu-central-1.vpce-svc-rgn_1"
	principalA      = "arn:aws:iam::210987654321:root"
	principalB      = "arn:aws:iam::210987654321:role/consumer"
)

// byocAccessConfig builds the region with its grant, and sets the access of that region. The extra
// resource stands for an aws_vpc_endpoint, which takes the service name in the same apply.
func byocAccessConfig(cloudURL, access string) string {
	return managedByocConfig(cloudURL, "ACCOUNT", "") + fmt.Sprintf(`
resource "restate_cloud_byoc_region_access" "eu" {
  region_id = restate_cloud_byoc_region.eu.id
%s
}

resource "terraform_data" "consumer" {
  input = restate_cloud_byoc_region_access.eu.endpoint_service_name
}
`, access)
}

// lastAccessRequest decodes the most recent SetRegionAccess body into its raw fields, so a test can
// tell an absent field from an empty one.
func lastAccessRequest(t *testing.T, cloud *testutil.CloudFake) map[string]json.RawMessage {
	t.Helper()
	requests := cloud.RequestsFor("SetRegionAccess")
	if len(requests) == 0 {
		t.Fatal("SetRegionAccess was not called")
	}
	var body map[string]json.RawMessage
	requests[len(requests)-1].DecodeBody(t, &body)
	return body
}

func TestByocRegionAccessLifecycle(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	cloud.AccessServicePolls = 1

	private := func(principals, cidrs string) string {
		return fmt.Sprintf(`
  entry_source_cidrs = %s
  private_link = {
    enabled             = true
    allowed_principals  = %s
    acceptance_required = true
  }`, cidrs, principals)
	}
	first := byocAccessConfig(cloud.URL, private(fmt.Sprintf("[%q, %q]", principalA, principalB), "[]"))
	second := byocAccessConfig(cloud.URL, private(fmt.Sprintf("[%q]", principalA), `["203.0.113.0/24", "198.51.100.0/24"]`))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: first,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(accessResource, "id", "rgn_1"),
					resource.TestCheckResourceAttr(accessResource, "region_id", "rgn_1"),
					resource.TestCheckResourceAttr(accessResource, "account_id", testProviderAccountID),
					// An empty list admits no client directly, and is not the same as leaving it out.
					resource.TestCheckResourceAttr(accessResource, "entry_source_cidrs.#", "0"),
					resource.TestCheckResourceAttr(accessResource, "private_link.enabled", "true"),
					resource.TestCheckResourceAttr(accessResource, "private_link.allowed_principals.#", "2"),
					resource.TestCheckResourceAttr(accessResource, "private_link.acceptance_required", "true"),
					// The apply waited for AWS to create the service, so a dependent resource gets the
					// name in the same apply.
					resource.TestCheckResourceAttr(accessResource, "endpoint_service_name", testServiceName),
					resource.TestCheckResourceAttr(accessResource, "endpoint_service_state", "Available"),
					resource.TestCheckResourceAttr(accessResource, "availability_zone_ids.#", "2"),
					resource.TestCheckResourceAttr("terraform_data.consumer", "output", testServiceName),
					func(*terraform.State) error {
						access := cloud.ByocRegion("rgn_1").Access
						if access == nil || access.EntrySourceCIDRs == nil || len(access.EntrySourceCIDRs) != 0 ||
							access.PrivateLink == nil || !access.PrivateLink.Enabled || !access.PrivateLink.AcceptanceRequired {
							return fmt.Errorf("region access is %+v", access)
						}
						return nil
					},
				),
			},
			{
				// A refresh after the create finds nothing to change.
				Config: first,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: second,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(accessResource, plancheck.ResourceActionUpdate),
						// The service survives a change of principals. A name that turned unknown would show
						// every endpoint that uses it as replaced.
						plancheck.ExpectKnownValue(accessResource, tfjsonpath.New("endpoint_service_name"), knownvalue.StringExact(testServiceName)),
						plancheck.ExpectResourceAction("terraform_data.consumer", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(accessResource, "entry_source_cidrs.#", "2"),
					resource.TestCheckResourceAttr(accessResource, "private_link.allowed_principals.#", "1"),
					resource.TestCheckResourceAttr(accessResource, "endpoint_service_name", testServiceName),
					func(*terraform.State) error {
						access := cloud.ByocRegion("rgn_1").Access
						if access == nil || !reflect.DeepEqual(access.EntrySourceCIDRs, []string{"198.51.100.0/24", "203.0.113.0/24"}) ||
							!reflect.DeepEqual(access.PrivateLink.AllowedPrincipals, []string{principalA}) {
							return fmt.Errorf("region access is %+v", access)
						}
						return nil
					},
				),
			},
			{
				Config:            second,
				ResourceName:      accessResource,
				ImportState:       true,
				ImportStateId:     testProviderAccountID + "/rgn_1",
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					// Restate stores no timeouts.
					"timeouts",
				},
			},
			{
				// Without the arguments, the region returns to its defaults: every address, and no service.
				Config: byocAccessConfig(cloud.URL, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(accessResource, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(accessResource, "entry_source_cidrs.#"),
					resource.TestCheckNoResourceAttr(accessResource, "private_link.enabled"),
					resource.TestCheckNoResourceAttr(accessResource, "endpoint_service_name"),
					resource.TestCheckNoResourceAttr(accessResource, "availability_zone_ids.#"),
					func(*terraform.State) error {
						body := lastAccessRequest(t, cloud)
						if len(body) != 1 || body["regionId"] == nil {
							return fmt.Errorf("the request was %v, want only the region ID", body)
						}
						return nil
					},
				),
			},
			{
				// The settings come back, so that the destroy has something to reset.
				Config: first,
			},
		},
		CheckDestroy: func(*terraform.State) error {
			access := cloud.ByocRegion("rgn_1").Access
			if access == nil || access.EntrySourceCIDRs != nil || access.PrivateLink != nil {
				return fmt.Errorf("region access is %+v after destroy, want the defaults", access)
			}
			if status := cloud.ByocRegion("rgn_1").Status; status != "DELETED" {
				return fmt.Errorf("region is %s after destroy", status)
			}
			return nil
		},
	})
}

// A missing list admits every address and an empty one admits none. The provider must send, store and
// refresh the two as different settings.
func TestByocRegionAccessKeepsNullAndEmptyApart(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	omitted := byocAccessConfig(cloud.URL, "")
	empty := byocAccessConfig(cloud.URL, "  entry_source_cidrs = []")
	listed := byocAccessConfig(cloud.URL, `  entry_source_cidrs = ["203.0.113.0/24"]`)
	update := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction(accessResource, plancheck.ResourceActionUpdate),
	}}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: omitted,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(accessResource, "entry_source_cidrs.#"),
					func(*terraform.State) error {
						if _, present := lastAccessRequest(t, cloud)["entrySourceCidrs"]; present {
							return fmt.Errorf("entrySourceCidrs was sent for a configuration that leaves it out")
						}
						if cidrs := cloud.ByocRegion("rgn_1").Access.EntrySourceCIDRs; cidrs != nil {
							return fmt.Errorf("the region stored %#v, want none", cidrs)
						}
						return nil
					},
				),
			},
			{Config: omitted, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				Config:           empty,
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(accessResource, "entry_source_cidrs.#", "0"),
					func(*terraform.State) error {
						if got := string(lastAccessRequest(t, cloud)["entrySourceCidrs"]); got != "[]" {
							return fmt.Errorf("entrySourceCidrs was sent as %q, want []", got)
						}
						if cidrs := cloud.ByocRegion("rgn_1").Access.EntrySourceCIDRs; cidrs == nil || len(cidrs) != 0 {
							return fmt.Errorf("the region stored %#v, want an empty list", cidrs)
						}
						return nil
					},
				),
			},
			{Config: empty, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{Config: listed, ConfigPlanChecks: update, Check: resource.TestCheckResourceAttr(accessResource, "entry_source_cidrs.#", "1")},
			{
				Config:           omitted,
				ConfigPlanChecks: update,
				Check:            resource.TestCheckNoResourceAttr(accessResource, "entry_source_cidrs.#"),
			},
		},
	})
}

// A change made outside Terraform shows in the next plan and is undone by the next apply.
func TestByocRegionAccessDetectsDrift(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	config := byocAccessConfig(cloud.URL, fmt.Sprintf(`
  entry_source_cidrs = ["203.0.113.0/24"]
  private_link = {
    enabled            = true
    allowed_principals = [%q]
  }`, principalA))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					access := cloud.ByocRegion("rgn_1").Access
					access.EntrySourceCIDRs = []string{"198.51.100.0/24"}
					access.PrivateLink.AllowedPrincipals = []string{principalB}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(accessResource, plancheck.ResourceActionUpdate),
				}},
				Check: func(*terraform.State) error {
					access := cloud.ByocRegion("rgn_1").Access
					if !reflect.DeepEqual(access.EntrySourceCIDRs, []string{"203.0.113.0/24"}) ||
						!reflect.DeepEqual(access.PrivateLink.AllowedPrincipals, []string{principalA}) {
						return fmt.Errorf("region access is %+v after the apply", access)
					}
					return nil
				},
			},
			{
				// Someone reset the access altogether.
				PreConfig: func() { cloud.ByocRegion("rgn_1").Access = nil },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(accessResource, plancheck.ResourceActionUpdate),
				}},
			},
		},
	})
}

// A region that was deleted out of band takes its access with it, and the next apply builds both
// again.
func TestByocRegionAccessLeavesTheStateWithItsRegion(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	config := byocAccessConfig(cloud.URL, `  entry_source_cidrs = ["203.0.113.0/24"]`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() { cloud.ByocRegion("rgn_1").Status = "DELETED" },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(accessResource, plancheck.ResourceActionCreate),
				}},
				Check: resource.TestCheckResourceAttr(accessResource, "region_id", "rgn_2"),
			},
		},
	})
}

// A block that says `enabled = false` and a region with no endpoint service are one setting, however
// the API words it, so the block must not turn into a change on every plan.
func TestByocRegionAccessDisabledPrivateLink(t *testing.T) {
	for name, omit := range map[string]bool{"reported as disabled": false, "reported as absent": true} {
		t.Run(name, func(t *testing.T) {
			clearProviderEnvironment(t)
			cloud := cloudFake(t)
			cloud.OmitDisabledPrivateLink = omit
			config := byocAccessConfig(cloud.URL, fmt.Sprintf(`
  private_link = {
    enabled            = false
    allowed_principals = [%q]
  }`, principalA))

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{
					{
						Config: config,
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttr(accessResource, "private_link.enabled", "false"),
							resource.TestCheckNoResourceAttr(accessResource, "endpoint_service_name"),
						),
					},
					{Config: config, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
				},
			})
		})
	}
}

// An empty principal list is a setting of its own for the configuration, and the API cannot tell it
// from none.
func TestByocRegionAccessKeepsAnEmptyPrincipalList(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	config := byocAccessConfig(cloud.URL, `
  private_link = {
    enabled            = true
    allowed_principals = []
  }`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config, Check: resource.TestCheckResourceAttr(accessResource, "private_link.allowed_principals.#", "0")},
			{Config: config, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

func TestByocRegionAccessValidatesTheArguments(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)

	rejected := map[string]struct{ access, message string }{
		"host bits":    {`entry_source_cidrs = ["10.0.0.1/24"]`, `(?s)host\s+bits\s+set.*"10.0.0.0/24"`},
		"leading zero": {`entry_source_cidrs = ["010.0.0.0/8"]`, `not\s+an\s+IPv4\s+network`},
		"IPv6":         {`entry_source_cidrs = ["2001:db8::/32"]`, `not\s+an\s+IPv4\s+network`},
		"no prefix":    {`entry_source_cidrs = ["203.0.113.0"]`, `not\s+an\s+IPv4\s+network`},
		"too many":     {`entry_source_cidrs = [for i in range(11) : "10.${i}.0.0/16"]`, `(?s)at\s+most\s+10`},
		"principal": {`private_link = {
    enabled            = true
    allowed_principals = ["210987654321"]
  }`, `must\s+be\s+an\s+IAM\s+principal\s+ARN`},
		"too many principals": {`private_link = {
    enabled            = true
    allowed_principals = [for i in range(21) : "arn:aws:iam::210987654321:user/u${i}"]
  }`, `(?s)at\s+most\s+20`},
		"no enabled": {`private_link = {
    allowed_principals = []
  }`, `attribute\s+"enabled"\s+is\s+required`},
	}
	steps := []resource.TestStep{}
	for name, test := range rejected {
		steps = append(steps, resource.TestStep{
			Config:      byocAccessConfig(cloud.URL, "  "+test.access),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(test.message),
		})
		_ = name
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: protocolProviders(), Steps: steps})
}

func TestByocRegionAccessExplainsEachRefusal(t *testing.T) {
	config := func(cloudURL string) string {
		return byocAccessConfig(cloudURL, `  entry_source_cidrs = ["203.0.113.0/24"]`)
	}
	for name, test := range map[string]struct {
		status  int
		message string
		want    string
	}{
		"invalid":         {http.StatusBadRequest, "the setting is not valid", `(?s)Access\s+is\s+available\s+for\s+AWS\s+regions.*the\s+setting\s+is\s+not\s+valid`},
		"not allowed":     {http.StatusForbidden, "the grant may not be used", `(?s)key\s+may\s+not\s+set.*the\s+grant\s+may\s+not\s+be\s+used`},
		"unknown":         {http.StatusNotFound, "no such region", `(?s)did\s+not\s+find\s+region.*offers\s+` + "`SetRegionAccess`"},
		"not a grant":     {http.StatusConflict, "the region is set up with an installer", `(?s)cannot\s+take\s+an\s+access\s+setting.*installer`},
		"not implemented": {http.StatusNotImplemented, "this stage does not support the setting", `(?s)does\s+not\s+offer\s+region\s+access\s+yet.*HTTP\s+501`},
	} {
		t.Run(name, func(t *testing.T) {
			clearProviderEnvironment(t)
			cloud := cloudFake(t)
			cloud.AccessFailures = []testutil.Response{testutil.Error(test.status, test.message)}

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{{
					Config:      config(cloud.URL),
					ExpectError: regexp.MustCompile(test.want),
				}},
			})
			if calls := cloud.CallCount("SetRegionAccess"); calls != 1 {
				t.Errorf("SetRegionAccess was called %d times, want 1: a refusal is not retried", calls)
			}
		})
	}
}

// A 503 means the region's infrastructure cannot take the setting yet, and the apply tries again.
func TestByocRegionAccessRetriesAnUnavailableRegion(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	cloud.AccessFailures = []testutil.Response{testutil.Error(http.StatusServiceUnavailable, "the infrastructure cannot take the setting yet")}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: byocAccessConfig(cloud.URL, `  entry_source_cidrs = ["203.0.113.0/24"]`),
			Check: func(*terraform.State) error {
				if calls := cloud.CallCount("SetRegionAccess"); calls != 2 {
					return fmt.Errorf("SetRegionAccess was called %d times, want 2", calls)
				}
				return nil
			},
		}},
	})
}

// A create that runs out of time while AWS builds the service records nothing: a state would make
// Terraform replace the resource, and the replacement resets the access first. The next apply sends
// the same setting and finds the service.
func TestByocRegionAccessResumesAfterAWaitThatTimedOut(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	cloud.AccessServicePolls = 1000

	config := byocAccessConfig(cloud.URL, fmt.Sprintf(`
  private_link = {
    enabled            = true
    allowed_principals = [%q]
  }
  timeouts {
    create = "2s"
  }`, principalA))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)accepted\s+the\s+setting.*Nothing\s+is\s+recorded\s+in\s+the\s+state`),
			},
			{
				PreConfig: func() { cloud.ByocRegion("rgn_1").Access.PrivateLink.ClearPendingPolls() },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(accessResource, "endpoint_service_name", testServiceName),
					func(*terraform.State) error {
						// The same setting was sent again, and the service was not recreated.
						if calls := cloud.CallCount("SetRegionAccess"); calls != 2 {
							return fmt.Errorf("SetRegionAccess was called %d times, want 2", calls)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestByocRegionAccessImportNeedsAccountAndRegion(t *testing.T) {
	clearProviderEnvironment(t)
	cloud := cloudFake(t)
	config := byocAccessConfig(cloud.URL, `  entry_source_cidrs = ["203.0.113.0/24"]`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{Config: config, ResourceName: accessResource, ImportState: true, ImportStateId: "rgn_1", ExpectError: regexp.MustCompile(`Expected\s+` + "`<account ID>/<region ID>`")},
			{Config: config, ResourceName: accessResource, ImportState: true, ImportStateId: "acc_other/rgn_1", ExpectError: regexp.MustCompile(`does\s+not\s+match\s+the\s+configured\s+account`)},
			{Config: config, ResourceName: accessResource, ImportState: true, ImportStateId: testProviderAccountID + "/rgn_404", ExpectError: regexp.MustCompile(`no\s+longer\s+exists|rgn_404`)},
		},
	})
}
