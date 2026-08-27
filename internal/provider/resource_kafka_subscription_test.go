package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
)

const kafkaClusterBody = `
resource "restate_kafka_cluster" "test" {
  name = "orders-cluster"

  properties = {
    "bootstrap.servers" = "broker-1:9092"
    "security.protocol" = "SASL_SSL"
    "sasl.username"     = "restate"
    "sasl.password"     = "the-real-password"
  }
}
`

func TestKafkaClusterCreate(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, kafkaClusterBody),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("restate_kafka_cluster.test", "id", "orders-cluster"),
				resource.TestCheckResourceAttr("restate_kafka_cluster.test", "name", "orders-cluster"),
				resource.TestCheckResourceAttr("restate_kafka_cluster.test", "created_at", "2026-08-27T10:00:00Z"),
				// The API redacts secret values on read, and the configured value is what state must
				// keep: the API is not a source of truth for something it will not disclose.
				resource.TestCheckResourceAttr("restate_kafka_cluster.test", "properties.sasl.password", "the-real-password"),
				resource.TestCheckResourceAttr("restate_kafka_cluster.test", "properties.sasl.username", "restate"),
				resource.TestCheckResourceAttr("restate_kafka_cluster.test", "properties.bootstrap.servers", "broker-1:9092"),
				// The real credential must be what was actually stored, not the placeholder. This runs
				// inside the step, because the harness destroys everything before a post-run assertion.
				checkFake(func() error {
					if got := admin.KafkaCluster("orders-cluster").Properties["sasl.password"]; got != "the-real-password" {
						return fmt.Errorf("stored password = %q, want the configured value", got)
					}
					return nil
				}),
			),
		}},
	})
}

func TestKafkaClusterCreateMismatchRetainsRemoteNameForCleanup(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)
	admin.CreatedKafkaNameOverride = "server-returned-cluster"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config:      adminConfig(admin.URL, kafkaClusterBody),
			ExpectError: regexp.MustCompile(`(?s)did not apply the requested value.*server-returned-cluster`),
		}},
	})

	if got := admin.KafkaClusterCount(); got != 0 {
		t.Errorf("KafkaClusterCount = %d, want 0: failed create should retain the remote name for cleanup", got)
	}
}

// This is the regression the redaction handling exists for: a refresh must neither erase the
// credential nor produce a permanent diff.
func TestKafkaClusterRefreshDoesNotDiffOnRedactedSecrets(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, kafkaClusterBody)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr("restate_kafka_cluster.test", "properties.sasl.password", "the-real-password"),
			},
		},
	})
}

// The provider must not depend on recognising a particular redaction marker: the contract documents
// that sensitive values are redacted but does not specify how, so a server using a different
// placeholder must still converge.
func TestKafkaClusterConvergesWithAnUnrecognisedRedactionMarker(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)
	admin.RedactionMarker = "xxxxxxxx"

	config := adminConfig(admin.URL, kafkaClusterBody)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr("restate_kafka_cluster.test", "properties.sasl.password", "the-real-password"),
			},
		},
	})
}

// A non-secret property changed outside Terraform is real drift and must be reported, or an
// out-of-band change to the broker list would be invisible.
func TestKafkaClusterRefreshDetectsNonSecretDrift(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, kafkaClusterBody)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					admin.KafkaCluster("orders-cluster").Properties["bootstrap.servers"] = "someone-elses-broker:9092"
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_kafka_cluster.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("restate_kafka_cluster.test", "properties.bootstrap.servers", "broker-1:9092"),
			},
		},
	})
}

// An update replaces the whole property map, so a property dropped from the configuration is removed
// from the cluster.
func TestKafkaClusterUpdateReplacesTheWholePropertyMap(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: adminConfig(admin.URL, kafkaClusterBody)},
			{
				Config: adminConfig(admin.URL, `
resource "restate_kafka_cluster" "test" {
  name = "orders-cluster"

  properties = {
    "bootstrap.servers" = "broker-1:9092"
  }
}
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_kafka_cluster.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_kafka_cluster.test", "properties.%", "1"),
					checkFake(func() error {
						if _, present := admin.KafkaCluster("orders-cluster").Properties["sasl.password"]; present {
							return fmt.Errorf("a property dropped from the configuration was left on the cluster")
						}
						return nil
					}),
				),
			},
		},
	})
}

// The API addresses updates by name, so a rename is a different cluster and must replace.
func TestKafkaClusterRenameRequiresReplacement(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: adminConfig(admin.URL, kafkaClusterBody)},
			{
				Config: adminConfig(admin.URL, `
resource "restate_kafka_cluster" "test" {
  name = "renamed-cluster"

  properties = {
    "bootstrap.servers" = "broker-1:9092"
  }
}
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_kafka_cluster.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("restate_kafka_cluster.test", "id", "renamed-cluster"),
					checkFake(func() error {
						if admin.KafkaCluster("orders-cluster") != nil {
							return fmt.Errorf("the old cluster survived the rename")
						}
						return nil
					}),
				),
			},
		},
	})
}

// "properties must identify the brokers" is a better message than whatever the server returns, and it
// costs no round trip.
func TestKafkaClusterRejectsPropertiesWithNoBrokers(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_kafka_cluster" "test" {
  name = "orders-cluster"

  properties = {
    "security.protocol" = "SASL_SSL"
  }
}
`),
			ExpectError: regexp.MustCompile(`(?s)Invalid Kafka cluster properties.*bootstrap.servers`),
		}},
	})

	if admin.KafkaClusterCount() != 0 {
		t.Error("a cluster was created for properties that identify no brokers")
	}
}

// Terraform orders the teardown when a subscription references the cluster, so the ordinary case
// needs no force at all.
func TestKafkaClusterDeleteIsOrderedAfterItsSubscriptions(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, kafkaClusterBody+`
resource "restate_subscription" "test" {
  source = "kafka://${restate_kafka_cluster.test.name}/orders"
  sink   = "service://OrderService/handleOrder"
}
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{Config: config, Destroy: true},
		},
	})

	// Both are gone, and the cluster delete never needed the force parameter: the subscription was
	// destroyed first because it referenced the cluster.
	if got := admin.KafkaClusterCount(); got != 0 {
		t.Errorf("KafkaClusterCount = %d, want 0", got)
	}
	if got := admin.SubscriptionCount(); got != 0 {
		t.Errorf("SubscriptionCount = %d, want 0", got)
	}
	for _, request := range admin.Requests() {
		if request.Method == http.MethodDelete && strings.Contains(request.Path, "/kafka-clusters/") {
			if strings.Contains(request.Query, "force=true") {
				t.Errorf("the cluster delete sent force=true without being asked to")
			}
		}
	}
}

// A cluster with a subscription attached that Terraform does not know about cannot be deleted safely,
// and the refusal must say what to do about it.
func TestKafkaClusterDeleteExplainsADependencyConflict(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, kafkaClusterBody)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					// A subscription created outside Terraform, so nothing orders the teardown and the
					// cluster delete runs straight into the server's dependency check.
					createSubscriptionOutOfBand(t, admin, "kafka://orders-cluster/orders")
				},
				Config:  config,
				Destroy: true,
				// The framework wraps the message, so match phrases a line break cannot split.
				ExpectError: regexp.MustCompile(`(?s)still has subscriptions referencing it.*orphans them`),
			},
			{
				// Remove the orphan so the harness's own destroy can succeed. Leaving it would make
				// this test fail on cleanup for the very reason it is asserting.
				PreConfig: func() { admin.DeleteSubscription("sub_1") },
				Config:    config,
			},
		},
	})
}

func TestKafkaClusterForceDestroySendsForce(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, `
resource "restate_kafka_cluster" "test" {
  name = "orders-cluster"

  properties = {
    "bootstrap.servers" = "broker-1:9092"
  }

  force_destroy = true
}
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("restate_kafka_cluster.test", "force_destroy", "true"),
			},
			{
				PreConfig: func() { createSubscriptionOutOfBand(t, admin, "kafka://orders-cluster/orders") },
				Config:    config,
				Destroy:   true,
			},
		},
	})

	// Forced deletion removes the cluster despite the attached subscription, orphaning it. That is the
	// documented consequence of opting in.
	if got := admin.KafkaClusterCount(); got != 0 {
		t.Errorf("KafkaClusterCount = %d, want 0", got)
	}
	if got := admin.SubscriptionCount(); got != 1 {
		t.Errorf("SubscriptionCount = %d, want the orphaned subscription to remain", got)
	}
}

// A cluster deleted out of band must be removed from state so the next plan recreates it.
func TestKafkaClusterRefreshRemovesADeletedCluster(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, kafkaClusterBody)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() { admin.DeleteKafkaCluster("orders-cluster") },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_kafka_cluster.test", plancheck.ResourceActionCreate),
					},
				},
			},
		},
	})
}

// Import cannot recover a redacted property, so state holds the API's placeholder and the provider
// warns. Asserting the placeholder is what proves the honest behaviour.
func TestKafkaClusterImportCannotRecoverSecrets(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: adminConfig(admin.URL, kafkaClusterBody)},
			{
				ResourceName:  "restate_kafka_cluster.test",
				ImportState:   true,
				ImportStateId: "orders-cluster",
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("got %d imported instances, want 1", len(states))
					}
					state := states[0]
					if got := state.Attributes["name"]; got != "orders-cluster" {
						return fmt.Errorf("name = %q", got)
					}
					if got := state.Attributes["properties.bootstrap.servers"]; got != "broker-1:9092" {
						return fmt.Errorf("properties.bootstrap.servers = %q, want the real value", got)
					}
					// An import has nothing to reconcile a redacted value against, so the placeholder
					// is what lands in state -- and the provider warns about it.
					if got := state.Attributes["properties.sasl.password"]; got != "***" {
						return fmt.Errorf("properties.sasl.password = %q, want the API's placeholder", got)
					}
					return nil
				},
			},
		},
	})
}

// The Kafka lookup exposes which subscriptions reference a cluster, which is what makes a safe
// teardown order expressible.
func TestKafkaClusterDataSourceListsSubscriptions(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, kafkaClusterBody+`
resource "restate_subscription" "test" {
  source = "kafka://${restate_kafka_cluster.test.name}/orders"
  sink   = "service://OrderService/handleOrder"
}

data "restate_kafka_cluster" "looked_up" {
  name = restate_kafka_cluster.test.name

  depends_on = [restate_subscription.test]
}
`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.restate_kafka_cluster.looked_up", "subscription_ids.#", "1"),
				// The data source has no configured value to reconcile a redacted property against, so
				// it reports the API's placeholder rather than pretending to know the credential.
				resource.TestCheckResourceAttr("data.restate_kafka_cluster.looked_up", "properties.sasl.password", "***"),
				resource.TestCheckResourceAttr("data.restate_kafka_cluster.looked_up", "properties.bootstrap.servers", "broker-1:9092"),
			),
		}},
	})
}

const subscriptionBody = `
resource "restate_subscription" "test" {
  source = "kafka://orders-cluster/orders"
  sink   = "service://OrderService/handleOrder"

  options = {
    "auto.offset.reset" = "earliest"
  }
}
`

func TestSubscriptionCreate(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, subscriptionBody),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("restate_subscription.test", "id", "sub_1"),
				resource.TestCheckResourceAttr("restate_subscription.test", "source", "kafka://orders-cluster/orders"),
				resource.TestCheckResourceAttr("restate_subscription.test", "sink", "service://OrderService/handleOrder"),
				resource.TestCheckResourceAttr("restate_subscription.test", "options.auto.offset.reset", "earliest"),
			),
		}},
	})
}

func TestSubscriptionPlanIsEmptyAfterApply(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, subscriptionBody)

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

// A subscription with no options must keep a null map rather than gaining an empty one, or it would
// never converge.
func TestSubscriptionWithNoOptionsConverges(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, `
resource "restate_subscription" "test" {
  source = "kafka://orders-cluster/orders"
  sink   = "service://OrderService/handleOrder"
}
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckNoResourceAttr("restate_subscription.test", "options"),
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

// The Admin API has no subscription update endpoint, so every change must replace the subscription.
func TestSubscriptionChangesRequireReplacement(t *testing.T) {
	for _, tt := range []struct {
		name  string
		after string
	}{
		{"source", `
resource "restate_subscription" "test" {
  source = "kafka://orders-cluster/orders-v2"
  sink   = "service://OrderService/handleOrder"

  options = {
    "auto.offset.reset" = "earliest"
  }
}
`},
		{"sink", `
resource "restate_subscription" "test" {
  source = "kafka://orders-cluster/orders"
  sink   = "service://OrderService/handleOrderV2"

  options = {
    "auto.offset.reset" = "earliest"
  }
}
`},
		{"options", `
resource "restate_subscription" "test" {
  source = "kafka://orders-cluster/orders"
  sink   = "service://OrderService/handleOrder"

  options = {
    "auto.offset.reset" = "latest"
  }
}
`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearProviderEnvironment(t)
			admin := adminFake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protocolProviders(),
				Steps: []resource.TestStep{
					{
						Config: adminConfig(admin.URL, subscriptionBody),
						Check:  resource.TestCheckResourceAttr("restate_subscription.test", "id", "sub_1"),
					},
					{
						Config: adminConfig(admin.URL, tt.after),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{
								plancheck.ExpectResourceAction("restate_subscription.test", plancheck.ResourceActionDestroyBeforeCreate),
							},
						},
						// A genuinely new subscription, which is the honest outcome when the API has no
						// way to change one.
						Check: resource.TestCheckResourceAttr("restate_subscription.test", "id", "sub_2"),
					},
				},
			})
		})
	}
}

// A URI with no scheme cannot be accepted by any server version, so it is a hard error.
func TestSubscriptionRejectsAnUnusableURI(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_subscription" "test" {
  source = "orders-cluster/orders"
  sink   = "service://OrderService/handleOrder"
}
`),
			ExpectError: regexp.MustCompile(`(?s)Invalid subscription source or sink.*no scheme`),
		}},
	})

	if admin.SubscriptionCount() != 0 {
		t.Error("a subscription was created for a URI with no scheme")
	}
}

// A scheme this contract snapshot does not document is a warning, not a rejection: a newer Restate
// server may support it, and a closed enum would make the provider the reason a supported
// configuration cannot be expressed.
func TestSubscriptionSendsAnUndocumentedSchemeAnyway(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, `
resource "restate_subscription" "test" {
  source = "pubsub://project/topic"
  sink   = "service://OrderService/handleOrder"
}
`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("restate_subscription.test", "source", "pubsub://project/topic"),
				checkFake(func() error {
					if got := admin.Subscription("sub_1").Source; got != "pubsub://project/topic" {
						return fmt.Errorf("stored source = %q, want it sent unchanged", got)
					}
					return nil
				}),
			),
		}},
	})
}

func TestSubscriptionRefreshRemovesADeletedSubscription(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	config := adminConfig(admin.URL, subscriptionBody)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() { admin.DeleteSubscription("sub_1") },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("restate_subscription.test", plancheck.ResourceActionCreate),
					},
				},
				Check: resource.TestCheckResourceAttr("restate_subscription.test", "id", "sub_2"),
			},
		},
	})
}

func TestSubscriptionImport(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{
			{Config: adminConfig(admin.URL, subscriptionBody)},
			{
				ResourceName:      "restate_subscription.test",
				ImportState:       true,
				ImportStateId:     "sub_1",
				ImportStateVerify: true,
			},
		},
	})
}

func TestSubscriptionDataSource(t *testing.T) {
	clearProviderEnvironment(t)
	admin := adminFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protocolProviders(),
		Steps: []resource.TestStep{{
			Config: adminConfig(admin.URL, subscriptionBody+`
data "restate_subscription" "looked_up" {
  id = restate_subscription.test.id
}
`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.restate_subscription.looked_up", "source", "kafka://orders-cluster/orders"),
				resource.TestCheckResourceAttr("data.restate_subscription.looked_up", "sink", "service://OrderService/handleOrder"),
			),
		}},
	})
}

// createSubscriptionOutOfBand adds a subscription the provider does not know about, so a Kafka
// cluster delete has a dependency to trip over.
func createSubscriptionOutOfBand(t *testing.T, admin *testutil.AdminFake, source string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, admin.URL+"/subscriptions",
		strings.NewReader(fmt.Sprintf(`{"source":%q,"sink":"service://OrderService/handleOrder"}`, source)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("create subscription out of band: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		t.Fatalf("create subscription out of band: status %d", response.StatusCode)
	}
}
