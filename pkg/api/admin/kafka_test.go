package admin_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin"
)

func TestValidateKafkaProperties(t *testing.T) {
	valid := []map[string]string{
		{"bootstrap.servers": "broker:9092"},
		{"metadata.broker.list": "broker:9092"},
		{"bootstrap.servers": "broker:9092", "security.protocol": "SASL_SSL"},
	}
	for _, properties := range valid {
		if err := admin.ValidateKafkaProperties(properties); err != nil {
			t.Errorf("ValidateKafkaProperties(%v): %v", properties, err)
		}
	}

	invalid := []map[string]string{
		nil,
		{},
		{"security.protocol": "SASL_SSL"},
		// A present but empty value cannot identify brokers either.
		{"bootstrap.servers": ""},
	}
	for _, properties := range invalid {
		if err := admin.ValidateKafkaProperties(properties); err == nil {
			t.Errorf("ValidateKafkaProperties(%v) was accepted", properties)
		}
	}
}

func TestIsRedactionMarker(t *testing.T) {
	markers := []string{"***", "*****", "[REDACTED]", "<redacted>", "redacted", "REDACTED", "  ***  "}
	for _, value := range markers {
		if !admin.IsRedactionMarker(value) {
			t.Errorf("IsRedactionMarker(%q) = false, want true", value)
		}
	}

	real := []string{
		"", "broker-1:9092", "SASL_SSL", "PLAIN", "svc-restate",
		// A short run of asterisks could plausibly be a real value; requiring three keeps the
		// heuristic from mangling one.
		"**",
		"pass***word",
	}
	for _, value := range real {
		if admin.IsRedactionMarker(value) {
			t.Errorf("IsRedactionMarker(%q) = true, want false", value)
		}
	}
}

func TestMergeRedactedProperties(t *testing.T) {
	tests := []struct {
		name       string
		remote     map[string]string
		configured map[string]string
		want       map[string]string
	}{
		{
			name: "a redacted secret keeps the configured value",
			remote: map[string]string{
				"bootstrap.servers": "broker:9092",
				"sasl.password":     "***",
			},
			configured: map[string]string{
				"bootstrap.servers": "broker:9092",
				"sasl.password":     "the-real-password",
			},
			want: map[string]string{
				"bootstrap.servers": "broker:9092",
				"sasl.password":     "the-real-password",
			},
		},
		{
			// The contract does not specify the marker, so a sensitive property keeps its configured
			// value even when the value read back does not look like a placeholder. The API is not a
			// source of truth for a value it will not disclose.
			name: "an unrecognised marker on a sensitive property still keeps the configured value",
			remote: map[string]string{
				"bootstrap.servers": "broker:9092",
				"sasl.password":     "xxxxxxxx",
			},
			configured: map[string]string{
				"bootstrap.servers": "broker:9092",
				"sasl.password":     "the-real-password",
			},
			want: map[string]string{
				"bootstrap.servers": "broker:9092",
				"sasl.password":     "the-real-password",
			},
		},
		{
			// Drift on a non-secret property is real and must be reported, or an out-of-band change
			// to the broker list would be invisible.
			name:       "a changed non-secret property takes the API's value",
			remote:     map[string]string{"bootstrap.servers": "new-broker:9092"},
			configured: map[string]string{"bootstrap.servers": "old-broker:9092"},
			want:       map[string]string{"bootstrap.servers": "new-broker:9092"},
		},
		{
			name:       "a property removed remotely is dropped",
			remote:     map[string]string{"bootstrap.servers": "broker:9092"},
			configured: map[string]string{"bootstrap.servers": "broker:9092", "sasl.password": "secret"},
			want:       map[string]string{"bootstrap.servers": "broker:9092"},
		},
		{
			name:       "a property added remotely is kept",
			remote:     map[string]string{"bootstrap.servers": "broker:9092", "security.protocol": "SASL_SSL"},
			configured: map[string]string{"bootstrap.servers": "broker:9092"},
			want:       map[string]string{"bootstrap.servers": "broker:9092", "security.protocol": "SASL_SSL"},
		},
		{
			// On import there is nothing configured, so the redacted values are all that is
			// available. The caller has to tell the user to supply them.
			name:       "with nothing configured the API's values pass through",
			remote:     map[string]string{"bootstrap.servers": "broker:9092", "sasl.password": "***"},
			configured: nil,
			want:       map[string]string{"bootstrap.servers": "broker:9092", "sasl.password": "***"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := admin.MergeRedactedProperties(tt.remote, tt.configured)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for name, value := range tt.want {
				if got[name] != value {
					t.Errorf("%s = %q, want %q", name, got[name], value)
				}
			}
		})
	}
}

// Merging must be stable: applying it to its own output cannot change the result, or a refresh loop
// would oscillate.
func TestMergeRedactedPropertiesIsIdempotent(t *testing.T) {
	remote := map[string]string{"bootstrap.servers": "broker:9092", "sasl.password": "***"}
	configured := map[string]string{"bootstrap.servers": "broker:9092", "sasl.password": "real"}

	once := admin.MergeRedactedProperties(remote, configured)
	twice := admin.MergeRedactedProperties(remote, once)

	if len(once) != len(twice) {
		t.Fatalf("first pass %v, second pass %v", once, twice)
	}
	for name, value := range once {
		if twice[name] != value {
			t.Errorf("%s = %q on the second pass, want %q", name, twice[name], value)
		}
	}
}

func TestMergeRedactedPropertiesHandlesNil(t *testing.T) {
	if got := admin.MergeRedactedProperties(nil, map[string]string{"a": "b"}); got != nil {
		t.Errorf("got %v, want nil when the API reported no properties", got)
	}
}

const kafkaClusterJSON = `{
  "name": "orders-cluster",
  "properties": {
    "bootstrap.servers": "broker-1:9092,broker-2:9092",
    "security.protocol": "SASL_SSL",
    "sasl.mechanism": "PLAIN",
    "sasl.password": "***"
  },
  "created_at": "2026-08-27T10:00:00Z",
  "subscriptions": []
}`

func TestCreateKafkaCluster(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/kafka-clusters", testutil.JSON(kafkaClusterJSON))

	got, err := newClient(t, server).KafkaClusters().Create(context.Background(), admin.KafkaCluster{
		Name: "orders-cluster",
		Properties: map[string]string{
			"bootstrap.servers": "broker-1:9092,broker-2:9092",
			"sasl.password":     "the-real-password",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Name != "orders-cluster" {
		t.Errorf("Name = %q", got.Name)
	}
	// The create response is already redacted, so a caller must merge; the facade does not pretend
	// the response is authoritative.
	if got.Properties["sasl.password"] != "***" {
		t.Errorf("sasl.password = %q, want the API's redacted value passed through", got.Properties["sasl.password"])
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["name"] != "orders-cluster" {
		t.Errorf("name = %v", body["name"])
	}
	properties, _ := body["properties"].(map[string]any)
	if properties["sasl.password"] != "the-real-password" {
		t.Errorf("the real password was not sent: %v", properties["sasl.password"])
	}
}

func TestCreateKafkaClusterValidatesLocally(t *testing.T) {
	server := testutil.NewAPIServer(t)
	clusters := newClient(t, server).KafkaClusters()

	if _, err := clusters.Create(context.Background(), admin.KafkaCluster{Properties: map[string]string{"bootstrap.servers": "b:9092"}}); err == nil {
		t.Error("expected an error for a missing name")
	}
	if _, err := clusters.Create(context.Background(), admin.KafkaCluster{Name: "c"}); err == nil {
		t.Error("expected an error for properties that name no brokers")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent despite invalid arguments")
	}
}

func TestGetKafkaCluster(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/kafka-clusters/orders-cluster", testutil.JSON(kafkaClusterJSON))

	got, err := newClient(t, server).KafkaClusters().Get(context.Background(), "orders-cluster")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CreatedAt != "2026-08-27T10:00:00Z" {
		t.Errorf("CreatedAt = %q", got.CreatedAt)
	}
	// A plain read must not ask for subscriptions, which the server has to go and fetch.
	if query := server.LastRequest().Query; query != "" {
		t.Errorf("query = %q, want no include_subscriptions on a plain read", query)
	}
}

func TestGetKafkaClusterWithSubscriptions(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/kafka-clusters/orders-cluster", testutil.JSON(`{
	  "name":"orders-cluster","properties":{"bootstrap.servers":"b:9092"},"created_at":"now",
	  "subscriptions":[{"id":"sub_1","source":"kafka://orders-cluster/orders","sink":"service://OrderService/handle","options":{}}]
	}`))

	got, err := newClient(t, server).KafkaClusters().GetWithSubscriptions(context.Background(), "orders-cluster")
	if err != nil {
		t.Fatalf("GetWithSubscriptions: %v", err)
	}
	if len(got.Subscriptions) != 1 || got.Subscriptions[0].ID != "sub_1" {
		t.Errorf("Subscriptions = %+v", got.Subscriptions)
	}
	if server.LastRequest().Query != "include_subscriptions=true" {
		t.Errorf("query = %q", server.LastRequest().Query)
	}
}

// A cluster name is a hostname-shaped string, but it still lands in the URL path.
func TestKafkaClusterNameIsEscapedIntoThePath(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.NotFoundResponse = &testutil.Response{Status: http.StatusOK, Body: kafkaClusterJSON}

	if _, err := newClient(t, server).KafkaClusters().Get(context.Background(), "cluster with space"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(server.LastRequest().RawPath, "%20") {
		t.Errorf("raw path %q does not show the name escaped", server.LastRequest().RawPath)
	}
}

func TestListKafkaClusters(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/kafka-clusters", testutil.JSON(`{"clusters":[
	  {"name":"a","properties":{"bootstrap.servers":"b:9092"},"created_at":"now"},
	  {"name":"b","properties":{"metadata.broker.list":"c:9092"},"created_at":"now"}
	]}`))

	got, err := newClient(t, server).KafkaClusters().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("clusters = %+v", got)
	}
}

func TestUpdateKafkaCluster(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/kafka-clusters/orders-cluster", testutil.JSON(kafkaClusterJSON))

	if _, err := newClient(t, server).KafkaClusters().Update(context.Background(), "orders-cluster", map[string]string{
		"bootstrap.servers": "new-broker:9092",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	request := server.LastRequest()
	if request.Method != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", request.Method)
	}
	var body map[string]any
	request.DecodeBody(t, &body)
	properties, _ := body["properties"].(map[string]any)
	if properties["bootstrap.servers"] != "new-broker:9092" {
		t.Errorf("properties = %v", properties)
	}
	// Update addresses the cluster by name in the path, so the body must not try to rename it.
	if _, present := body["name"]; present {
		t.Errorf("name was sent in an update body: %v", body["name"])
	}
}

func TestUpdateKafkaClusterValidatesLocally(t *testing.T) {
	server := testutil.NewAPIServer(t)
	clusters := newClient(t, server).KafkaClusters()

	if _, err := clusters.Update(context.Background(), "", map[string]string{"bootstrap.servers": "b:9092"}); err == nil {
		t.Error("expected an error for a missing name")
	}
	if _, err := clusters.Update(context.Background(), "c", map[string]string{"security.protocol": "SASL_SSL"}); err == nil {
		t.Error("expected an error for properties that name no brokers")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent despite invalid arguments")
	}
}

// Forced deletion orphans subscriptions, so the default must leave the API's dependency check in
// charge rather than sending force unconditionally.
func TestDeleteKafkaClusterDefaultsToADependencySafeDelete(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/kafka-clusters/orders-cluster", testutil.Status(http.StatusAccepted))

	if err := newClient(t, server).KafkaClusters().Delete(context.Background(), "orders-cluster", admin.DeleteKafkaClusterOptions{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if query := server.LastRequest().Query; query != "" {
		t.Errorf("query = %q, want no force parameter by default", query)
	}
}

func TestDeleteKafkaClusterSendsForceWhenOptedIn(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/kafka-clusters/orders-cluster", testutil.Status(http.StatusAccepted))

	if err := newClient(t, server).KafkaClusters().Delete(context.Background(), "orders-cluster", admin.DeleteKafkaClusterOptions{Force: true}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if query := server.LastRequest().Query; query != "force=true" {
		t.Errorf("query = %q, want force=true", query)
	}
}

// A dependency conflict is the expected outcome of a safe delete with subscriptions attached, and
// the message has to say what to do about it.
func TestDeleteKafkaClusterExplainsADependencyConflict(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/kafka-clusters/orders-cluster", testutil.Response{
		Status: http.StatusConflict,
		Body:   `{"message":"cluster still has subscriptions"}`,
	})

	err := newClient(t, server).KafkaClusters().Delete(context.Background(), "orders-cluster", admin.DeleteKafkaClusterOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"orders-cluster", "delete those subscriptions first", "orphans them"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

func TestDeleteKafkaClusterTreatsNotFoundAsSuccess(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/kafka-clusters/gone", testutil.Response{Status: http.StatusNotFound, Body: `{"message":"not found"}`})

	if err := newClient(t, server).KafkaClusters().Delete(context.Background(), "gone", admin.DeleteKafkaClusterOptions{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDeleteKafkaClusterRequiresAName(t *testing.T) {
	server := testutil.NewAPIServer(t)
	if err := newClient(t, server).KafkaClusters().Delete(context.Background(), "", admin.DeleteKafkaClusterOptions{}); err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent without a name")
	}
}

// A caller mutating its property map after the call must not change what was sent.
func TestKafkaPropertiesAreCopiedBeforeSending(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/kafka-clusters", testutil.JSON(kafkaClusterJSON))

	properties := map[string]string{"bootstrap.servers": "original:9092"}
	if _, err := newClient(t, server).KafkaClusters().Create(context.Background(), admin.KafkaCluster{
		Name: "c", Properties: properties,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	properties["bootstrap.servers"] = "mutated:9092"

	var body struct {
		Properties map[string]string `json:"properties"`
	}
	server.LastRequest().DecodeBody(t, &body)
	if body.Properties["bootstrap.servers"] != "original:9092" {
		t.Errorf("sent value = %q, want the value at call time", body.Properties["bootstrap.servers"])
	}
}
