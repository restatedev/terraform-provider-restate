package admin_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin"
)

const serviceJSON = `{
  "name": "OrderService",
  "ty": "VirtualObject",
  "revision": 3,
  "deployment_id": "dp_1a2b3c",
  "documentation": "Handles orders.",
  "public": true,
  "idempotency_retention": "1day",
  "journal_retention": "7days",
  "inactivity_timeout": "1m",
  "abort_timeout": "1m",
  "enable_lazy_state": false,
  "metadata": {"team": "orders"},
  "retry_policy": {
    "initial_interval": "50ms",
    "max_interval": "10s",
    "max_attempts": 5,
    "exponentiation_factor": 2.0,
    "on_max_attempts": "pause"
  },
  "handlers": [{
    "name": "handleOrder",
    "ty": "Exclusive",
    "input_description": "one of ['application/json']",
    "output_description": "one of ['application/json']",
    "public": true,
    "journal_retention": "7days"
  }]
}`

func TestGetService(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/services/OrderService", testutil.JSON(serviceJSON))

	got, err := newClient(t, server).Services().Get(context.Background(), "OrderService")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Name != "OrderService" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Type != admin.ServiceTypeVirtualObject {
		t.Errorf("Type = %q", got.Type)
	}
	if got.Revision != 3 {
		t.Errorf("Revision = %d", got.Revision)
	}
	if got.DeploymentID != "dp_1a2b3c" {
		t.Errorf("DeploymentID = %q", got.DeploymentID)
	}
	if got.Public == nil || !*got.Public {
		t.Errorf("Public = %v", got.Public)
	}
	if got.JournalRetention != "7days" {
		t.Errorf("JournalRetention = %q", got.JournalRetention)
	}
	if got.RetryPolicy == nil || got.RetryPolicy.MaxAttempts == nil || *got.RetryPolicy.MaxAttempts != 5 {
		t.Errorf("RetryPolicy = %+v", got.RetryPolicy)
	}
	if got.RetryPolicy.OnMaxAttempts != "pause" {
		t.Errorf("OnMaxAttempts = %q", got.RetryPolicy.OnMaxAttempts)
	}
	if len(got.Handlers) != 1 || got.Handlers[0].Name != "handleOrder" {
		t.Fatalf("Handlers = %+v", got.Handlers)
	}
	if got.Handlers[0].Type != admin.HandlerExclusive {
		t.Errorf("handler Type = %q", got.Handlers[0].Type)
	}
}

// A pointer field must distinguish "the server reported false" from "the server reported nothing",
// because Terraform has to tell an explicit false from an absent value.
func TestGetServiceDistinguishesFalseFromAbsent(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/services/A", testutil.JSON(`{"name":"A","ty":"Service","revision":1,"deployment_id":"dp_1","public":false,"handlers":[]}`))
	server.On("/services/B", testutil.JSON(`{"name":"B","ty":"Service","revision":1,"deployment_id":"dp_1","handlers":[]}`))

	services := newClient(t, server).Services()

	explicit, err := services.Get(context.Background(), "A")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if explicit.Public == nil {
		t.Fatal("an explicit false was reported as absent")
	}
	if *explicit.Public {
		t.Error("Public = true, want false")
	}

	absent, err := services.Get(context.Background(), "B")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if absent.Public != nil {
		t.Errorf("Public = %v, want nil for an unreported field", absent.Public)
	}
}

func TestGetServiceRequiresAName(t *testing.T) {
	server := testutil.NewAPIServer(t)
	if _, err := newClient(t, server).Services().Get(context.Background(), ""); err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent without a name")
	}
}

func TestListServices(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/services", testutil.JSON(`{"services":[`+serviceJSON+`]}`))

	got, err := newClient(t, server).Services().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "OrderService" {
		t.Errorf("services = %+v", got)
	}
}

func TestModifyServiceSendsOnlySetFields(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/services/OrderService", testutil.JSON(serviceJSON))

	public := false
	retention := "3days"
	if _, err := newClient(t, server).Services().Update(context.Background(), "OrderService", admin.ModifyService{
		Public:           &public,
		JournalRetention: &retention,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	request := server.LastRequest()
	if request.Method != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", request.Method)
	}

	var body map[string]any
	request.DecodeBody(t, &body)
	// An explicitly false boolean must be sent, or the change could not be applied.
	if body["public"] != false {
		t.Errorf("public = %v, want false", body["public"])
	}
	if body["journal_retention"] != "3days" {
		t.Errorf("journal_retention = %v", body["journal_retention"])
	}
	// The contract does not document what a JSON null means for these fields -- whether it restores
	// the SDK-discovered value or a server default -- so an unset field is omitted, never nulled.
	for _, field := range []string{"idempotency_retention", "workflow_completion_retention", "inactivity_timeout", "abort_timeout"} {
		if _, present := body[field]; present {
			t.Errorf("%s was sent despite not being set: %v", field, body[field])
		}
	}
}

// An empty modify request has undefined meaning in the contract, so it must not be sent; reading is
// the correct no-op.
func TestModifyServiceWithNothingSetReadsInstead(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/services/OrderService", testutil.JSON(serviceJSON))

	if _, err := newClient(t, server).Services().Update(context.Background(), "OrderService", admin.ModifyService{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := server.LastRequest().Method; got != http.MethodGet {
		t.Errorf("method = %q, want GET: an empty modify request must not be sent", got)
	}
}

func TestModifyServiceRequiresAName(t *testing.T) {
	server := testutil.NewAPIServer(t)
	public := true
	if _, err := newClient(t, server).Services().Update(context.Background(), "", admin.ModifyService{Public: &public}); err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent without a name")
	}
}

func TestModifyServiceIsEmpty(t *testing.T) {
	if !(admin.ModifyService{}).IsEmpty() {
		t.Error("a zero ModifyService is not reported as empty")
	}
	value := false
	if (admin.ModifyService{Public: &value}).IsEmpty() {
		t.Error("a ModifyService setting public to false is reported as empty")
	}
	retention := ""
	if (admin.ModifyService{JournalRetention: &retention}).IsEmpty() {
		t.Error("a ModifyService setting a retention to the empty string is reported as empty")
	}
}

// A service name can contain characters that must be escaped rather than concatenated into a path.
func TestServiceNameIsEscapedIntoThePath(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.NotFoundResponse = &testutil.Response{Status: http.StatusOK, Body: serviceJSON}

	if _, err := newClient(t, server).Services().Get(context.Background(), "Order Service/v2"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	raw := server.LastRequest().RawPath
	if !strings.Contains(raw, "%20") || !strings.Contains(raw, "%2F") {
		t.Errorf("raw path %q does not show the name escaped", raw)
	}
}

func TestGetHandler(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/services/OrderService/handlers/handleOrder", testutil.JSON(`{
	  "name":"handleOrder","ty":"Workflow",
	  "input_description":"one of ['application/json']","output_description":"none",
	  "documentation":"Runs the order workflow.","public":false
	}`))

	got, err := newClient(t, server).Services().GetHandler(context.Background(), "OrderService", "handleOrder")
	if err != nil {
		t.Fatalf("GetHandler: %v", err)
	}
	if got.Type != admin.HandlerWorkflow {
		t.Errorf("Type = %q", got.Type)
	}
	if got.Documentation != "Runs the order workflow." {
		t.Errorf("Documentation = %q", got.Documentation)
	}
	if got.Public == nil || *got.Public {
		t.Errorf("Public = %v, want an explicit false", got.Public)
	}
}

func TestGetHandlerRequiresBothNames(t *testing.T) {
	server := testutil.NewAPIServer(t)
	services := newClient(t, server).Services()

	if _, err := services.GetHandler(context.Background(), "", "h"); err == nil {
		t.Error("expected an error for a missing service name")
	}
	if _, err := services.GetHandler(context.Background(), "S", ""); err == nil {
		t.Error("expected an error for a missing handler name")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent despite invalid arguments")
	}
}

func TestListHandlers(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/services/OrderService/handlers", testutil.JSON(`{"handlers":[
	  {"name":"a","input_description":"x","output_description":"y"},
	  {"name":"b","input_description":"x","output_description":"y"}
	]}`))

	got, err := newClient(t, server).Services().ListHandlers(context.Background(), "OrderService")
	if err != nil {
		t.Fatalf("ListHandlers: %v", err)
	}
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("handlers = %+v", got)
	}
	// `ty` is optional in the contract, so an absent handler type must be empty, not a failure.
	if got[0].Type != "" {
		t.Errorf("Type = %q, want empty for an unreported handler type", got[0].Type)
	}
}

func TestListHandlersRequiresAServiceName(t *testing.T) {
	server := testutil.NewAPIServer(t)
	if _, err := newClient(t, server).Services().ListHandlers(context.Background(), ""); err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent without a name")
	}
}

// Workflow completion retention is only meaningful for a workflow service, and the service type is
// how that is diagnosed rather than guessed.
func TestWorkflowCompletionRetentionAppliesOnlyToWorkflows(t *testing.T) {
	if !admin.ServiceTypeWorkflow.SupportsWorkflowCompletionRetention() {
		t.Error("a workflow service does not support workflow completion retention")
	}
	for _, serviceType := range []admin.ServiceType{admin.ServiceTypeService, admin.ServiceTypeVirtualObject} {
		if serviceType.SupportsWorkflowCompletionRetention() {
			t.Errorf("%s supports workflow completion retention", serviceType)
		}
	}
}
