package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
)

// adminFake starts a stateful Admin API fake. It models the deployment, Kafka cluster and
// subscription lifecycles rather than replaying canned responses, which matters because the provider
// verifies that the API applied what it was asked for: a static fixture that always reports its
// original values is indistinguishable from a server that ignored the request.
func adminFake(t *testing.T) *testutil.AdminFake {
	t.Helper()
	return testutil.NewAdminFake(t)
}

// adminConfig renders a provider block pointed at an Admin API fake plus one resource body.
func adminConfig(adminURL, body string) string {
	return providerBlock(map[string]string{
		"admin_url":           adminURL,
		"environment_api_key": "key_environment",
	}) + body
}

func TestDeploymentCreateHTTP(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri      = "https://orders.example.com"
    metadata = { team = "orders" }
  }
  force_destroy = true
}
`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("restate_deployment.test", "id", "dp_1"),
				resource.TestCheckResourceAttr("restate_deployment.test", "endpoint_type", "http"),
				resource.TestCheckResourceAttr("restate_deployment.test", "http.uri", "https://orders.example.com"),
				// http_version is the negotiated outcome, exported separately from the use_http_11
				// request so neither is mistaken for the other.
				resource.TestCheckResourceAttr("restate_deployment.test", "http.http_version", "HTTP/2"),
				resource.TestCheckResourceAttr("restate_deployment.test", "http.use_http_11", "false"),
				resource.TestCheckResourceAttr("restate_deployment.test", "http.protocol_type", "BidiStream"),
				resource.TestCheckResourceAttr("restate_deployment.test", "http.metadata.team", "orders"),
				resource.TestCheckResourceAttr("restate_deployment.test", "sdk_version", "typescript-1.9.0"),
				// Registration performs discovery, and what it found is exported without Terraform
				// taking ownership of it.
				resource.TestCheckResourceAttr("restate_deployment.test", "services.#", "1"),
				resource.TestCheckResourceAttr("restate_deployment.test", "services.0.name", "OrderService"),
				// The Lambda half of the schema must be absent, not empty.
				resource.TestCheckNoResourceAttr("restate_deployment.test", "lambda"),
			),
		}},
	})

	// dry_run is an action, not configuration, and can never produce a deployment for Terraform to
	// own, so it must never be sent.
	var body map[string]any
	admin.RequestsFor("/deployments")[0].DecodeBody(t, &body)
	if _, present := body["dry_run"]; present {
		t.Errorf("dry_run was sent: %v", body["dry_run"])
	}
}

// use_http_11 is a request-time choice the API does not report back. The provider keeps the
// configured value and exports the negotiated http_version separately, so a deployment registered
// this way converges instead of diffing against an outcome it cannot control.
func TestDeploymentUseHTTP11IsNotReadBackFromHTTPVersion(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri         = "https://orders.example.com"
    use_http_11 = true
  }
  force_destroy = true
}
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_deployment.test", "http.use_http_11", "true"),
					resource.TestCheckResourceAttr("restate_deployment.test", "http.http_version", "HTTP/1.1"),
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

func TestDeploymentCreateLambda(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  lambda = {
    arn             = "arn:aws:lambda:us-east-1:123456789012:function:reports:3"
    assume_role_arn = "arn:aws:iam::123456789012:role/restate-invoke"
  }
  force_destroy = true
}
`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("restate_deployment.test", "endpoint_type", "lambda"),
				resource.TestCheckResourceAttr("restate_deployment.test", "lambda.arn", "arn:aws:lambda:us-east-1:123456789012:function:reports:3"),
				resource.TestCheckResourceAttr("restate_deployment.test", "lambda.assume_role_arn", "arn:aws:iam::123456789012:role/restate-invoke"),
				// Compression is reported by the API but accepted by neither register nor update, so
				// it is an output only.
				resource.TestCheckResourceAttr("restate_deployment.test", "lambda.compression", "Zstd"),
				resource.TestCheckNoResourceAttr("restate_deployment.test", "http"),
			),
		}},
	})

	// An HTTP-only field in a Lambda registration would match neither member of the untagged union.
	var body map[string]any
	admin.RequestsFor("/deployments")[0].DecodeBody(t, &body)
	for _, field := range []string{"uri", "use_http_11", "compression"} {
		if _, present := body[field]; present {
			t.Errorf("%s was sent in a Lambda registration: %v", field, body[field])
		}
	}
}

// Exactly one endpoint kind must be configured; neither and both are configuration errors.
func TestDeploymentRequiresExactlyOneEndpointKind(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{"neither", `
resource "restate_deployment" "test" {
  force_destroy = true
}
`},
		{"both", `
resource "restate_deployment" "test" {
  http = {
    uri = "https://orders.example.com"
  }
  lambda = {
    arn = "arn:aws:lambda:us-east-1:123456789012:function:reports:3"
  }
  force_destroy = true
}
`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearProviderEnvironment(t)
			admin := adminFake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{{
					Config:      adminConfig(admin.URL, tt.body),
					ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Combination`),
				}},
			})

			if admin.DeploymentCount() != 0 {
				t.Error("a deployment was registered for an ambiguous endpoint kind")
			}
		})
	}
}

func TestDeploymentPlanIsEmptyAfterApply(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri      = "https://orders.example.com"
    metadata = { team = "orders" }
    additional_headers = {
      "x-api-key" = "downstream-secret"
    }
  }
  force_destroy = true
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

// The URI is one of the few things the Admin API's update request can change, so moving an endpoint
// must be an in-place update rather than a replacement.
func TestDeploymentURIChangeIsAppliedInPlace(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri = "https://orders.example.com"
  }
  force_destroy = true
}
`)},
			{
				Config: adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri = "https://orders-v2.example.com"
  }
  force_destroy = true
}
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_deployment.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_deployment.test", "http.uri", "https://orders-v2.example.com"),
					// The same deployment, not a replacement.
					resource.TestCheckResourceAttr("restate_deployment.test", "id", "dp_1"),
					checkFake(func() error {
						if got := admin.DeploymentCount(); got != 1 {
							return fmt.Errorf("DeploymentCount = %d, want 1: an in-place update must not register a second deployment", got)
						}
						return nil
					}),
				),
			},
		},
	})
}

// Headers are one of the few fields the update request accepts, so changing one is applied in place.
func TestDeploymentHeaderChangeIsAppliedInPlace(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri                = "https://orders.example.com"
    additional_headers = { "x-api-key" = "first" }
  }
  force_destroy = true
}
`)},
			{
				Config: adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri                = "https://orders.example.com"
    additional_headers = { "x-api-key" = "rotated" }
  }
  force_destroy = true
}
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_deployment.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("restate_deployment.test", "http.additional_headers.x-api-key", "rotated"),
			},
		},
	})
}

// The Admin API's update request accepts neither metadata nor authentication, so a change to either
// must replace the deployment rather than being silently dropped.
func TestDeploymentUnUpdatableFieldsRequireReplacement(t *testing.T) {
	for _, tt := range []struct {
		name   string
		before string
		after  string
	}{
		{
			name: "metadata",
			before: `
  http = {
    uri      = "https://orders.example.com"
    metadata = { team = "orders" }
  }
`,
			after: `
  http = {
    uri      = "https://orders.example.com"
    metadata = { team = "payments" }
  }
`,
		},
		{
			name: "authentication",
			before: `
  http = {
    uri = "https://orders.example.com"
  }
`,
			after: `
  http = {
    uri = "https://orders.example.com"
    auth = {
      google_id_token = {
        audience = "https://orders.example.com"
      }
    }
  }
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearProviderEnvironment(t)
			admin := adminFake(t)

			wrap := func(body string) string {
				return adminConfig(admin.URL, "resource \"restate_deployment\" \"test\" {"+body+"  force_destroy = true\n}\n")
			}

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{
					{
						Config: wrap(tt.before),
						Check:  resource.TestCheckResourceAttr("restate_deployment.test", "id", "dp_1"),
					},
					{
						Config: wrap(tt.after),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{
								plancheck.ExpectResourceAction("restate_deployment.test", plancheck.ResourceActionDestroyBeforeCreate),
							},
						},
						// A genuinely new deployment, which is the honest outcome when the API cannot
						// apply the change.
						Check: resource.TestCheckResourceAttr("restate_deployment.test", "id", "dp_2"),
					},
				},
			})
		})
	}
}

// Switching endpoint kind is a different deployment, not an edit: the Admin API's register and update
// requests are separate union members, so there is no request that could turn one into the other.
func TestDeploymentEndpointKindChangeRequiresReplacement(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri = "https://orders.example.com"
  }
  force_destroy = true
}
`),
				Check: resource.TestCheckResourceAttr("restate_deployment.test", "endpoint_type", "http"),
			},
			{
				Config: adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  lambda = {
    arn = "arn:aws:lambda:us-east-1:123456789012:function:reports:3"
  }
  force_destroy = true
}
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_deployment.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_deployment.test", "endpoint_type", "lambda"),
					// The HTTP half of the schema must be gone, not left behind as stale state.
					resource.TestCheckNoResourceAttr("restate_deployment.test", "http"),
				),
			},
		},
	})
}

// Forced deletion is the only mode the Admin API implements and it can break in-flight invocations,
// so a destroy must refuse until it is opted into and applied.
func TestDeploymentDestroyRequiresForceDestroy(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	guarded := adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri = "https://orders.example.com"
  }
  force_destroy = false
}
`)
	permitted := adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri = "https://orders.example.com"
  }
  force_destroy = true
}
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: guarded},
			{
				Config:      guarded,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`(?s)requires force_destroy.*unrecoverable`),
			},
			{
				// The refusal must have happened before any request reached the API, and this is the
				// last moment that can be observed: the harness destroys the deployment for real once
				// this step's state is in place.
				PreConfig: func() {
					if got := admin.DeploymentCount(); got != 1 {
						t.Errorf("DeploymentCount = %d, want the guarded deployment to have survived", got)
					}
					for _, request := range admin.Requests() {
						if request.Method == "DELETE" {
							t.Error("a delete was sent while force_destroy was false")
						}
					}
				},
				Config: permitted,
			},
		},
	})

	if got := admin.DeploymentCount(); got != 0 {
		t.Errorf("DeploymentCount = %d after cleanup, want 0", got)
	}
}

// Header values are typically credentials, and it is not established whether the API echoes them
// back, redacts them, or omits them. They are preserved from configuration so a value the API
// declines to disclose cannot silently erase the configured credential.
func TestDeploymentPreservesConfiguredHeaders(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri                = "https://orders.example.com"
    additional_headers = { "x-api-key" = "the-real-key" }
  }
  force_destroy = true
}
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("restate_deployment.test", "http.additional_headers.x-api-key", "the-real-key"),
			},
			{
				PreConfig: func() {
					// The API now reports the header redacted, which must not overwrite state.
					admin.Deployment("dp_1").AdditionalHeaders = map[string]string{"x-api-key": "***"}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr("restate_deployment.test", "http.additional_headers.x-api-key", "the-real-key"),
			},
		},
	})
}

// A deployment deleted out of band must be removed from state so the next plan registers it again.
func TestDeploymentRefreshRemovesADeletedDeployment(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri = "https://orders.example.com"
  }
  force_destroy = true
}
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() { admin.DeleteDeployment("dp_1") },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_deployment.test", plancheck.ResourceActionCreate),
					},
				},
				Check: resource.TestCheckResourceAttr("restate_deployment.test", "id", "dp_2"),
			},
		},
	})
}

// An out-of-band URI change is real drift and must show as a plan to move it back.
func TestDeploymentRefreshDetectsAnOutOfBandURIChange(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri = "https://orders.example.com"
  }
  force_destroy = true
}
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() { admin.Deployment("dp_1").URI = "https://somewhere-else.example.com" },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_deployment.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("restate_deployment.test", "http.uri", "https://orders.example.com"),
			},
		},
	})
}

// Discovery calls the caller's own endpoint, so its failure must be reported in those terms rather
// than as an opaque API error.
func TestDeploymentReportsADiscoveryFailure(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)
	admin.DiscoveryError = "connection refused"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri = "https://unreachable.example.com"
  }
  force_destroy = true
}
`),
			// The framework wraps the message, so match phrases a line break cannot split.
			ExpectError: regexp.MustCompile(`(?s)Could not register the Restate deployment.*unreachable`),
		}},
	})
}

func TestDeploymentImport(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri      = "https://orders.example.com"
    metadata = { team = "orders" }
  }
  force_destroy = true
}
`)},
			{
				ResourceName:  "restate_deployment.test",
				ImportState:   true,
				ImportStateId: "dp_1",
				// force, breaking, overwrite and use_http_11 cannot be imported: the Admin API reports
				// none of them, so they take their schema defaults and the next plan is empty for a
				// configuration that does not set them.
				//
				// force_destroy is ignored here for the same reason -- it has no server-side
				// representation -- and this configuration sets it so the harness can clean up.
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "force_destroy"},
			},
		},
	})
}

// The deployment lookup data source reads a deployment the configuration does not manage, exposing
// both endpoint kinds' attributes with the other kind's left null.
func TestDeploymentDataSource(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_deployment" "test" {
  http = {
    uri = "https://orders.example.com"
  }
  force_destroy = true
}

data "restate_deployment" "looked_up" {
  id = restate_deployment.test.id
}
`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.restate_deployment.looked_up", "endpoint_type", "http"),
				resource.TestCheckResourceAttr("data.restate_deployment.looked_up", "uri", "https://orders.example.com"),
				resource.TestCheckResourceAttr("data.restate_deployment.looked_up", "sdk_version", "typescript-1.9.0"),
				// The Lambda attributes belong to the other kind, so they are null rather than a
				// plausible-looking empty string.
				resource.TestCheckNoResourceAttr("data.restate_deployment.looked_up", "arn"),
				resource.TestCheckNoResourceAttr("data.restate_deployment.looked_up", "compression"),
			),
		}},
	})
}
