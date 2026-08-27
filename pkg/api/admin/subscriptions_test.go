package admin_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin"
)

func TestValidateSubscriptionURIsAcceptsTheDocumentedForms(t *testing.T) {
	warnings, err := admin.ValidateSubscriptionURIs(
		"kafka://orders-cluster/orders",
		"service://OrderService/handleOrder",
	)
	if err != nil {
		t.Fatalf("ValidateSubscriptionURIs: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %+v, want none for the documented forms", warnings)
	}
}

// A newer server may advertise a scheme this snapshot has never heard of. Warning keeps the
// provider from being the reason a supported configuration cannot be expressed.
func TestValidateSubscriptionURIsWarnsRatherThanRejectingAnUnknownScheme(t *testing.T) {
	warnings, err := admin.ValidateSubscriptionURIs(
		"pubsub://project/topic",
		"service://OrderService/handleOrder",
	)
	if err != nil {
		t.Fatalf("an unknown scheme was rejected outright: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v, want exactly one", warnings)
	}
	if warnings[0].Field != "source" {
		t.Errorf("warning field = %q", warnings[0].Field)
	}
	for _, want := range []string{"pubsub", "newer Restate server may support it"} {
		if !strings.Contains(warnings[0].Message, want) {
			t.Errorf("warning %q is missing %q", warnings[0].Message, want)
		}
	}
}

func TestValidateSubscriptionURIsWarnsOnAMalformedDocumentedScheme(t *testing.T) {
	tests := []struct {
		name         string
		source, sink string
		wantField    string
	}{
		{"kafka source with no topic", "kafka://orders-cluster", "service://S/h", "source"},
		{"kafka source with an empty topic", "kafka://orders-cluster/", "service://S/h", "source"},
		{"service sink with no handler", "kafka://c/t", "service://OrderService", "sink"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings, err := admin.ValidateSubscriptionURIs(tt.source, tt.sink)
			if err != nil {
				t.Fatalf("ValidateSubscriptionURIs: %v", err)
			}
			if len(warnings) != 1 || warnings[0].Field != tt.wantField {
				t.Fatalf("warnings = %+v, want one for %s", warnings, tt.wantField)
			}
			if !strings.Contains(warnings[0].Message, "documented form") {
				t.Errorf("warning does not name the documented form: %q", warnings[0].Message)
			}
		})
	}
}

// A URI with no scheme cannot be accepted by any server version, so it is a hard error rather than
// a warning.
func TestValidateSubscriptionURIsRejectsUnusableValues(t *testing.T) {
	tests := []struct {
		name         string
		source, sink string
	}{
		{"empty source", "", "service://S/h"},
		{"empty sink", "kafka://c/t", ""},
		{"source with no scheme", "orders-cluster/orders", "service://S/h"},
		{"sink with no scheme", "kafka://c/t", "OrderService/handle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := admin.ValidateSubscriptionURIs(tt.source, tt.sink); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

const subscriptionJSON = `{
  "id": "sub_1a2b3c",
  "source": "kafka://orders-cluster/orders",
  "sink": "service://OrderService/handleOrder",
  "options": {"auto.offset.reset": "earliest"}
}`

func TestCreateSubscription(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/subscriptions", testutil.JSON(subscriptionJSON))

	got, err := newClient(t, server).Subscriptions().Create(context.Background(), admin.CreateSubscription{
		Source:  "kafka://orders-cluster/orders",
		Sink:    "service://OrderService/handleOrder",
		Options: map[string]string{"auto.offset.reset": "earliest"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID != "sub_1a2b3c" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.Options["auto.offset.reset"] != "earliest" {
		t.Errorf("Options = %v", got.Options)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["source"] != "kafka://orders-cluster/orders" {
		t.Errorf("source = %v", body["source"])
	}
	if body["sink"] != "service://OrderService/handleOrder" {
		t.Errorf("sink = %v", body["sink"])
	}
}

func TestCreateSubscriptionOmitsAbsentOptions(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/subscriptions", testutil.JSON(subscriptionJSON))

	if _, err := newClient(t, server).Subscriptions().Create(context.Background(), admin.CreateSubscription{
		Source: "kafka://c/t", Sink: "service://S/h",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if _, present := body["options"]; present {
		t.Errorf("options was sent despite not being configured: %v", body["options"])
	}
}

// An unknown scheme is a warning, not a rejection, so the request must still be sent unchanged.
func TestCreateSubscriptionSendsAnUnknownSchemeUnchanged(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/subscriptions", testutil.JSON(subscriptionJSON))

	if _, err := newClient(t, server).Subscriptions().Create(context.Background(), admin.CreateSubscription{
		Source: "pubsub://project/topic", Sink: "service://S/h",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["source"] != "pubsub://project/topic" {
		t.Errorf("source = %v, want it sent unchanged", body["source"])
	}
}

func TestCreateSubscriptionRejectsAnUnusableURI(t *testing.T) {
	server := testutil.NewAPIServer(t)
	if _, err := newClient(t, server).Subscriptions().Create(context.Background(), admin.CreateSubscription{
		Source: "", Sink: "service://S/h",
	}); err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent with no source")
	}
}

func TestCreateSubscriptionRejectsAResponseWithNoIdentifier(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/subscriptions", testutil.JSON(`{"id":"","source":"kafka://c/t","sink":"service://S/h","options":{}}`))

	if _, err := newClient(t, server).Subscriptions().Create(context.Background(), admin.CreateSubscription{
		Source: "kafka://c/t", Sink: "service://S/h",
	}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestGetSubscription(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/subscriptions/sub_1a2b3c", testutil.JSON(subscriptionJSON))

	got, err := newClient(t, server).Subscriptions().Get(context.Background(), "sub_1a2b3c")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Source != "kafka://orders-cluster/orders" {
		t.Errorf("Source = %q", got.Source)
	}
}

func TestGetSubscriptionRequiresAnID(t *testing.T) {
	server := testutil.NewAPIServer(t)
	if _, err := newClient(t, server).Subscriptions().Get(context.Background(), ""); err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent without an identifier")
	}
}

func TestListSubscriptions(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/subscriptions", testutil.JSON(`{"subscriptions":[`+subscriptionJSON+`]}`))

	got, err := newClient(t, server).Subscriptions().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "sub_1a2b3c" {
		t.Errorf("subscriptions = %+v", got)
	}
	if query := server.LastRequest().Query; query != "" {
		t.Errorf("query = %q, want no filters on an unfiltered list", query)
	}
}

func TestListSubscriptionsFiltered(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/subscriptions", testutil.JSON(`{"subscriptions":[]}`))

	if _, err := newClient(t, server).Subscriptions().ListFiltered(context.Background(), "kafka://c/t", ""); err != nil {
		t.Fatalf("ListFiltered: %v", err)
	}

	query := server.LastRequest().Query
	if !strings.Contains(query, "source=kafka") {
		t.Errorf("query = %q, want the source filter", query)
	}
	// An empty filter must be omitted rather than sent as an empty parameter, which the server
	// could read as "match the empty sink".
	if strings.Contains(query, "sink=") {
		t.Errorf("query = %q, want the empty sink filter omitted", query)
	}
}

// Options are typed as required in the response but a server may omit them; an absent map must be
// nil rather than a decode failure.
func TestGetSubscriptionToleratesAbsentOptions(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/subscriptions/sub_1", testutil.JSON(`{"id":"sub_1","source":"kafka://c/t","sink":"service://S/h"}`))

	got, err := newClient(t, server).Subscriptions().Get(context.Background(), "sub_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Options != nil {
		t.Errorf("Options = %v, want nil", got.Options)
	}
}

func TestDeleteSubscription(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/subscriptions/sub_1", testutil.Status(http.StatusAccepted))

	if err := newClient(t, server).Subscriptions().Delete(context.Background(), "sub_1", struct{}{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if server.LastRequest().Method != http.MethodDelete {
		t.Errorf("method = %q", server.LastRequest().Method)
	}
}

func TestDeleteSubscriptionTreatsNotFoundAsSuccess(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/subscriptions/sub_1", testutil.Response{Status: http.StatusNotFound, Body: `{"message":"not found"}`})

	if err := newClient(t, server).Subscriptions().Delete(context.Background(), "sub_1", struct{}{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDeleteSubscriptionRequiresAnID(t *testing.T) {
	server := testutil.NewAPIServer(t)
	if err := newClient(t, server).Subscriptions().Delete(context.Background(), "", struct{}{}); err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent without an identifier")
	}
}
