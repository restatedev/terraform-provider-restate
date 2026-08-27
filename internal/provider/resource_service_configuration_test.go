package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
)

func serviceResponse(name, serviceType string, public bool, journalRetention string) string {
	return fmt.Sprintf(`{
	  "name": %q, "ty": %q, "revision": 3, "deployment_id": "dp_1a2b3c",
	  "public": %t, "journal_retention": %q,
	  "idempotency_retention": "1day", "inactivity_timeout": "1m", "abort_timeout": "1m",
	  "handlers": [{"name":"handleOrder","ty":"Exclusive","input_description":"json","output_description":"json"}]
	}`, name, serviceType, public, journalRetention)
}

// serviceState is one service's mutable configuration, so a test can both let the provider change it
// and change it out of band.
type serviceState struct {
	public           bool
	journalRetention string
}

// serviceServer models one service statefully: a PATCH applies what it is sent, and a GET reports the
// current values. It returns the state so a test can mutate it directly to simulate an out-of-band
// change.
//
// A static route cannot express this. The provider verifies that the API applied what it was asked
// for, and a server that always reports its original values looks exactly like a server that ignored
// the request -- so a static fixture would make a correct provider fail its own consistency check.
func serviceServer(t *testing.T, name, serviceType, journalRetention string) (*testutil.APIServer, *serviceState) {
	t.Helper()

	current := &serviceState{journalRetention: journalRetention}

	server := adminServer(t)
	server.OnFunc("/services/"+name, func(request testutil.Request) testutil.Response {
		if request.Method == http.MethodPatch {
			var body struct {
				Public           *bool   `json:"public"`
				JournalRetention *string `json:"journal_retention"`
			}
			if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
				return testutil.Response{Status: http.StatusBadRequest, Body: `{"message":"malformed request body"}`}
			}
			if body.Public != nil {
				current.public = *body.Public
			}
			if body.JournalRetention != nil {
				current.journalRetention = *body.JournalRetention
			}
		}
		return testutil.JSON(serviceResponse(name, serviceType, current.public, current.journalRetention))
	})
	return server, current
}

// vanishService makes the service disappear, as it would if the deployment exposing it were removed.
func vanishService(server *testutil.APIServer, name string) {
	server.OnFunc("/services/"+name, func(testutil.Request) testutil.Response {
		return testutil.Response{Status: http.StatusNotFound, Body: `{"message":"service not found"}`}
	})
}

func TestServiceConfigurationApply(t *testing.T) {
	clearProviderEnvironment(t)
	admin, _ := serviceServer(t, "OrderService", "Service", "7days")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_service_configuration" "test" {
  service_name      = "OrderService"
  public            = false
  journal_retention = "7 days"
}
`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("restate_service_configuration.test", "id", "OrderService"),
				resource.TestCheckResourceAttr("restate_service_configuration.test", "service_type", "Service"),
				resource.TestCheckResourceAttr("restate_service_configuration.test", "revision", "3"),
				resource.TestCheckResourceAttr("restate_service_configuration.test", "deployment_id", "dp_1a2b3c"),
				resource.TestCheckResourceAttr("restate_service_configuration.test", "public", "false"),
			),
		}},
	})

	// Only what was configured may be sent. The contract does not define what a JSON null means for
	// these fields, so an unset field must be omitted rather than nulled.
	patches := patchRequests(admin)
	if len(patches) == 0 {
		t.Fatal("no modify request was sent")
	}
	var body map[string]any
	patches[0].DecodeBody(t, &body)
	if body["public"] != false {
		t.Errorf("public = %v, want an explicit false", body["public"])
	}
	if body["journal_retention"] != "7 days" {
		t.Errorf("journal_retention = %v", body["journal_retention"])
	}
	for _, field := range []string{"idempotency_retention", "inactivity_timeout", "abort_timeout", "workflow_completion_retention"} {
		if _, present := body[field]; present {
			t.Errorf("%s was sent despite not being configured: %v", field, body[field])
		}
	}
}

// This is what the semantic duration type exists for: the server may echo a duration back in a
// different spelling, and that must not be a permanent diff.
func TestServiceConfigurationDurationSpellingIsNotDrift(t *testing.T) {
	clearProviderEnvironment(t)
	// Configured as "7 days"; the server reports the equivalent ISO-8601 form.
	admin, _ := serviceServer(t, "OrderService", "Service", "P7D")

	config := adminConfig(admin.URL, `
resource "restate_service_configuration" "test" {
  service_name      = "OrderService"
  journal_retention = "7 days"
}
`)

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

// A genuinely different duration must still show as drift, so semantic comparison cannot be hiding
// real changes.
func TestServiceConfigurationDetectsARealDurationChange(t *testing.T) {
	clearProviderEnvironment(t)
	admin, service := serviceServer(t, "OrderService", "Service", "7days")

	config := adminConfig(admin.URL, `
resource "restate_service_configuration" "test" {
  service_name      = "OrderService"
  journal_retention = "7 days"
}
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					// Someone shortened the retention outside Terraform. Refresh must see it, and the
					// plan must put it back. The server still honours what it is sent, so the update
					// can actually apply.
					service.journalRetention = "1day"
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_service_configuration.test", plancheck.ResourceActionUpdate),
					},
				},
				// The update must actually apply, so the server goes back to honouring what it is
				// sent before the step's own apply runs.
				Check: resource.TestCheckResourceAttr("restate_service_configuration.test", "journal_retention", "7 days"),
			},
		},
	})
}

// Workflow completion retention is meaningless on a non-workflow service. Checking the service's
// actual type produces a far better message than the server's rejection.
func TestServiceConfigurationRejectsWorkflowRetentionOnANonWorkflow(t *testing.T) {
	clearProviderEnvironment(t)
	admin, _ := serviceServer(t, "OrderService", "VirtualObject", "7days")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_service_configuration" "test" {
  service_name                  = "OrderService"
  workflow_completion_retention = "P30D"
}
`),
			ExpectError: regexp.MustCompile(`(?s)applies only to a workflow service.*VirtualObject`),
		}},
	})

	if len(patchRequests(admin)) != 0 {
		t.Error("a modify request was sent for a setting the service cannot have")
	}
}

func TestServiceConfigurationAcceptsWorkflowRetentionOnAWorkflow(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminServer(t)
	admin.UseFullPath = true
	admin.On("/services/OnboardingWorkflow", testutil.JSON(`{
	  "name":"OnboardingWorkflow","ty":"Workflow","revision":1,"deployment_id":"dp_1",
	  "workflow_completion_retention":"P30D","handlers":[]
	}`))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_service_configuration" "test" {
  service_name                  = "OnboardingWorkflow"
  workflow_completion_retention = "P30D"
}
`),
			Check: resource.TestCheckResourceAttr("restate_service_configuration.test", "service_type", "Workflow"),
		}},
	})
}

// A resource with nothing set would apply cleanly and change nothing, since the provider never sends
// a null. Rejecting it at validation is more useful than a successful no-op.
func TestServiceConfigurationRejectsAnEmptyConfiguration(t *testing.T) {
	clearProviderEnvironment(t)
	admin, _ := serviceServer(t, "OrderService", "Service", "7days")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_service_configuration" "test" {
  service_name = "OrderService"
}
`),
			ExpectError: regexp.MustCompile(`(?s)manages nothing.*At least one of`),
		}},
	})
}

// A service exists only once a deployment exposing it is registered, so the error has to point at
// that rather than just saying "not found".
func TestServiceConfigurationReportsAMissingService(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminServer(t)
	admin.UseFullPath = true
	admin.On("/services/Nonexistent", testutil.Response{Status: 404, Body: `{"message":"service not found"}`})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_service_configuration" "test" {
  service_name = "Nonexistent"
  public       = false
}
`),
			ExpectError: regexp.MustCompile(`(?s)Restate service not found.*deployment exposing it is registered.*depends_on`),
		}},
	})
}

// The service disappearing means the deployment exposing it is gone, so there is nothing to
// configure and the resource must leave state.
func TestServiceConfigurationRefreshRemovesAVanishedService(t *testing.T) {
	clearProviderEnvironment(t)
	admin, _ := serviceServer(t, "OrderService", "Service", "7days")

	config := adminConfig(admin.URL, `
resource "restate_service_configuration" "test" {
  service_name = "OrderService"
  public       = false
}
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					vanishService(admin, "OrderService")
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_service_configuration.test", plancheck.ResourceActionCreate),
					},
				},
				// Recreating needs the service back.
				ExpectError: regexp.MustCompile(`(?s)Restate service not found`),
			},
		},
	})
}

// Destroy is a state-only operation, because the API cannot reset a setting to its discovered value.
// It must warn rather than silently doing nothing.
func TestServiceConfigurationDestroyLeavesSettingsApplied(t *testing.T) {
	clearProviderEnvironment(t)
	admin, _ := serviceServer(t, "OrderService", "Service", "7days")

	config := adminConfig(admin.URL, `
resource "restate_service_configuration" "test" {
  service_name = "OrderService"
  public       = false
}
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{Config: config, Destroy: true},
		},
	})

	// A destroy must not have sent a modify request: there is nothing safe to send, and sending an
	// empty or nulled body would be guessing at semantics the contract does not define.
	before := len(patchRequests(admin))
	if before == 0 {
		t.Fatal("the create sent no modify request")
	}
	if before > 1 {
		t.Errorf("%d modify requests were sent; a destroy must send none", before)
	}
}

// Changing the service name means configuring a different service, which replaces the resource --
// and, per the partial-management contract, leaves the old service's settings applied.
func TestServiceConfigurationServiceNameChangeRequiresReplacement(t *testing.T) {
	clearProviderEnvironment(t)
	admin, _ := serviceServer(t, "OrderService", "Service", "7days")
	admin.On("/services/PaymentService", testutil.JSON(serviceResponse("PaymentService", "Service", false, "7days")))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: adminConfig(admin.URL, `
resource "restate_service_configuration" "test" {
  service_name = "OrderService"
  public       = false
}
`)},
			{
				Config: adminConfig(admin.URL, `
resource "restate_service_configuration" "test" {
  service_name = "PaymentService"
  public       = false
}
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_service_configuration.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
		},
	})
}

// An attribute the configuration does not manage must stay out of state, so it cannot appear in a
// plan as something Terraform wants to change.
func TestServiceConfigurationLeavesUnmanagedAttributesNull(t *testing.T) {
	clearProviderEnvironment(t)
	admin, _ := serviceServer(t, "OrderService", "Service", "7days")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_service_configuration" "test" {
  service_name = "OrderService"
  public       = false
}
`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("restate_service_configuration.test", "public", "false"),
				// The API reports these, but the configuration does not manage them, so they must not
				// be adopted into state.
				resource.TestCheckNoResourceAttr("restate_service_configuration.test", "journal_retention"),
				resource.TestCheckNoResourceAttr("restate_service_configuration.test", "idempotency_retention"),
				resource.TestCheckNoResourceAttr("restate_service_configuration.test", "abort_timeout"),
			),
		}},
	})
}

// patchRequests returns the modify requests the server received.
func patchRequests(server *testutil.APIServer) []testutil.Request {
	var patches []testutil.Request
	for _, request := range server.Requests() {
		if request.Method == "PATCH" {
			patches = append(patches, request)
		}
	}
	return patches
}
