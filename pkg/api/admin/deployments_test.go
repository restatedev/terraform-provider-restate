package admin_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin"
)

const httpDeploymentJSON = `{
  "id": "dp_1a2b3c",
  "uri": "https://orders.example.com",
  "protocol_type": "BidiStream",
  "http_version": "HTTP/2",
  "created_at": "2026-08-27T10:00:00Z",
  "min_protocol_version": 5,
  "max_protocol_version": 6,
  "sdk_version": "typescript-1.9.0",
  "additional_headers": {"x-api-key": "downstream-secret"},
  "metadata": {"team": "orders"},
  "services": [{"name": "OrderService", "revision": 3}]
}`

const lambdaDeploymentJSON = `{
  "id": "dp_lambda",
  "arn": "arn:aws:lambda:us-east-1:123456789012:function:orders:1",
  "assume_role_arn": "arn:aws:iam::123456789012:role/restate-invoke",
  "compression": "Zstd",
  "created_at": "2026-08-27T10:00:00Z",
  "min_protocol_version": 5,
  "max_protocol_version": 6,
  "services": [{"name": "OrderService", "revision": 1}]
}`

// The two deployment response variants have no discriminator on the wire, so the facade tells them
// apart by the field only one of them carries.
func TestGetDeploymentResolvesTheHTTPVariant(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments/dp_1a2b3c", testutil.JSON(httpDeploymentJSON))

	got, err := newClient(t, server).Deployments().Get(context.Background(), "dp_1a2b3c")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Kind != admin.EndpointHTTP {
		t.Errorf("Kind = %q, want http", got.Kind)
	}
	if got.URI != "https://orders.example.com" {
		t.Errorf("URI = %q", got.URI)
	}
	if got.HTTPVersion != "HTTP/2" {
		t.Errorf("HTTPVersion = %q", got.HTTPVersion)
	}
	if got.ProtocolType != admin.ProtocolBidiStream {
		t.Errorf("ProtocolType = %q", got.ProtocolType)
	}
	if got.SDKVersion != "typescript-1.9.0" {
		t.Errorf("SDKVersion = %q", got.SDKVersion)
	}
	if got.AdditionalHeaders["x-api-key"] != "downstream-secret" {
		t.Errorf("AdditionalHeaders = %v", got.AdditionalHeaders)
	}
	if got.Metadata["team"] != "orders" {
		t.Errorf("Metadata = %v", got.Metadata)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "OrderService" || got.Services[0].Revision != 3 {
		t.Errorf("Services = %+v", got.Services)
	}
	// Lambda-only fields must stay empty on an HTTP deployment.
	if got.ARN != "" || got.Compression != "" {
		t.Errorf("Lambda fields were populated on an HTTP deployment: %+v", got)
	}
}

func TestGetDeploymentResolvesTheLambdaVariant(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments/dp_lambda", testutil.JSON(lambdaDeploymentJSON))

	got, err := newClient(t, server).Deployments().Get(context.Background(), "dp_lambda")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Kind != admin.EndpointLambda {
		t.Errorf("Kind = %q, want lambda", got.Kind)
	}
	if got.ARN != "arn:aws:lambda:us-east-1:123456789012:function:orders:1" {
		t.Errorf("ARN = %q", got.ARN)
	}
	if got.AssumeRoleARN != "arn:aws:iam::123456789012:role/restate-invoke" {
		t.Errorf("AssumeRoleARN = %q", got.AssumeRoleARN)
	}
	if got.Compression != admin.LambdaCompressionZstd {
		t.Errorf("Compression = %q", got.Compression)
	}
	// HTTP-only fields must stay empty on a Lambda deployment.
	if got.URI != "" || got.HTTPVersion != "" || got.ProtocolType != "" {
		t.Errorf("HTTP fields were populated on a Lambda deployment: %+v", got)
	}
}

// A deployment with neither uri nor arn matches neither union member. Guessing would put a
// half-populated resource into state.
func TestGetDeploymentRejectsAnUnresolvableVariant(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments/dp_1", testutil.JSON(`{"id":"dp_1","created_at":"now"}`))

	_, err := newClient(t, server).Deployments().Get(context.Background(), "dp_1")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "neither a uri nor an arn") {
		t.Errorf("error does not explain the failure: %v", err)
	}
}

func TestGetDeploymentResolvesAuthentication(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments/dp_1", testutil.JSON(`{
	  "id":"dp_1","uri":"https://svc.run.app","protocol_type":"BidiStream","http_version":"HTTP/2",
	  "created_at":"now","min_protocol_version":5,"max_protocol_version":6,
	  "auth":{"GoogleIdToken":{"audience":"https://svc.run.app","impersonate_service_account":"restate@project.iam.gserviceaccount.com"}},
	  "services":[]
	}`))

	got, err := newClient(t, server).Deployments().Get(context.Background(), "dp_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Auth == nil || got.Auth.GoogleIDToken == nil {
		t.Fatalf("Auth = %+v, want Google ID token authentication", got.Auth)
	}
	if got.Auth.GoogleIDToken.Audience != "https://svc.run.app" {
		t.Errorf("Audience = %q", got.Auth.GoogleIDToken.Audience)
	}
	if got.Auth.GoogleIDToken.ImpersonateServiceAccount != "restate@project.iam.gserviceaccount.com" {
		t.Errorf("ImpersonateServiceAccount = %q", got.Auth.GoogleIDToken.ImpersonateServiceAccount)
	}
}

// The contract types auth as nullable. An explicit null means "no authentication", not a malformed
// response.
func TestGetDeploymentToleratesNullAuthentication(t *testing.T) {
	for _, auth := range []string{`"auth":null,`, ``} {
		server := testutil.NewAPIServer(t)
		server.UseFullPath = true
		server.On("/deployments/dp_1", testutil.JSON(`{
		  "id":"dp_1","uri":"https://svc","protocol_type":"BidiStream","http_version":"HTTP/2",
		  "created_at":"now","min_protocol_version":5,"max_protocol_version":6,`+auth+`
		  "services":[]
		}`))

		got, err := newClient(t, server).Deployments().Get(context.Background(), "dp_1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Auth != nil {
			t.Errorf("Auth = %+v, want nil", got.Auth)
		}
	}
}

func TestGetDeploymentRequiresAnID(t *testing.T) {
	server := testutil.NewAPIServer(t)
	if _, err := newClient(t, server).Deployments().Get(context.Background(), ""); err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent without an identifier")
	}
}

// A deployment ID goes into the URL path and must be escaped, not concatenated.
func TestDeploymentIDIsEscapedIntoThePath(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.NotFoundResponse = &testutil.Response{Status: http.StatusOK, Body: httpDeploymentJSON}

	if _, err := newClient(t, server).Deployments().Get(context.Background(), "dp_with/slash and space"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	request := server.LastRequest()
	if !strings.Contains(request.RawPath, "%2F") || !strings.Contains(request.RawPath, "%20") {
		t.Errorf("raw path %q does not show the identifier escaped", request.RawPath)
	}
}

func TestListDeploymentsResolvesMixedVariants(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments", testutil.JSON(`{"deployments":[`+httpDeploymentJSON+`,`+lambdaDeploymentJSON+`]}`))

	got, err := newClient(t, server).Deployments().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d deployments, want 2", len(got))
	}
	if got[0].Kind != admin.EndpointHTTP || got[1].Kind != admin.EndpointLambda {
		t.Errorf("kinds = %q, %q", got[0].Kind, got[1].Kind)
	}
}

func TestListDeploymentsOnAnEmptyEnvironment(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments", testutil.JSON(`{"deployments":[]}`))

	got, err := newClient(t, server).Deployments().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d deployments, want 0", len(got))
	}
}

func TestCreateHTTPDeployment(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments", testutil.JSON(`{
	  "id":"dp_new","min_protocol_version":5,"max_protocol_version":6,"sdk_version":"typescript-1.9.0",
	  "services":[{"name":"OrderService","ty":"Service","revision":1,"deployment_id":"dp_new","handlers":[]}]
	}`))

	got, err := newClient(t, server).Deployments().Create(context.Background(), admin.RegisterInput{
		HTTP: &admin.RegisterHTTPDeployment{
			URI:               "https://orders.example.com",
			UseHTTP11:         true,
			AdditionalHeaders: map[string]string{"x-api-key": "secret"},
			Metadata:          map[string]string{"team": "orders"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID != "dp_new" {
		t.Errorf("ID = %q", got.ID)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "OrderService" {
		t.Errorf("Services = %+v", got.Services)
	}
	if got.SDKVersion != "typescript-1.9.0" {
		t.Errorf("SDKVersion = %q", got.SDKVersion)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["uri"] != "https://orders.example.com" {
		t.Errorf("uri = %v", body["uri"])
	}
	if body["use_http_11"] != true {
		t.Errorf("use_http_11 = %v", body["use_http_11"])
	}
	// A Lambda-only field must never appear in an HTTP registration, or the untagged union would
	// match neither member.
	if _, present := body["arn"]; present {
		t.Error("arn was sent in an HTTP registration")
	}
	// dry_run is an action, not configuration, and can never produce a deployment for Terraform to
	// own.
	if _, present := body["dry_run"]; present {
		t.Errorf("dry_run was sent: %v", body["dry_run"])
	}
}

// The contract defaults `force` to true. Sending an explicit false would be a behaviour change, and
// sending nothing is what leaves the server's own default in charge, so an unset flag is omitted.
func TestCreateDeploymentOmitsUnsetFlags(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments", testutil.JSON(`{"id":"dp_new","services":[]}`))

	if _, err := newClient(t, server).Deployments().Create(context.Background(), admin.RegisterInput{
		HTTP: &admin.RegisterHTTPDeployment{URI: "https://orders.example.com"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	for _, field := range []string{"force", "breaking", "use_http_11", "additional_headers", "metadata", "auth"} {
		if _, present := body[field]; present {
			t.Errorf("%s was sent despite not being configured: %v", field, body[field])
		}
	}
}

func TestCreateDeploymentSendsExplicitDestructiveFlags(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments", testutil.JSON(`{"id":"dp_new","services":[]}`))

	if _, err := newClient(t, server).Deployments().Create(context.Background(), admin.RegisterInput{
		HTTP: &admin.RegisterHTTPDeployment{URI: "https://orders.example.com", Force: true, Breaking: true},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["force"] != true || body["breaking"] != true {
		t.Errorf("force = %v, breaking = %v", body["force"], body["breaking"])
	}
}

// A nil header map means "do not send this field"; an empty map means "no headers". The distinction
// is load-bearing, because sending an empty object would clear headers the server already has.
func TestCreateDeploymentDistinguishesNilFromEmptyMaps(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments", testutil.JSON(`{"id":"dp_new","services":[]}`))

	if _, err := newClient(t, server).Deployments().Create(context.Background(), admin.RegisterInput{
		HTTP: &admin.RegisterHTTPDeployment{
			URI:               "https://orders.example.com",
			AdditionalHeaders: map[string]string{},
		},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	headers, present := body["additional_headers"]
	if !present {
		t.Fatal("an explicitly empty header map was omitted")
	}
	if decoded, ok := headers.(map[string]any); !ok || len(decoded) != 0 {
		t.Errorf("additional_headers = %v, want an empty object", headers)
	}
}

func TestCreateLambdaDeployment(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments", testutil.JSON(`{"id":"dp_new","services":[]}`))

	if _, err := newClient(t, server).Deployments().Create(context.Background(), admin.RegisterInput{
		Lambda: &admin.RegisterLambdaDeployment{
			ARN:           "arn:aws:lambda:us-east-1:123456789012:function:orders:1",
			AssumeRoleARN: "arn:aws:iam::123456789012:role/restate-invoke",
		},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["arn"] != "arn:aws:lambda:us-east-1:123456789012:function:orders:1" {
		t.Errorf("arn = %v", body["arn"])
	}
	if body["assume_role_arn"] != "arn:aws:iam::123456789012:role/restate-invoke" {
		t.Errorf("assume_role_arn = %v", body["assume_role_arn"])
	}
	// An HTTP-only field must never appear in a Lambda registration.
	if _, present := body["uri"]; present {
		t.Error("uri was sent in a Lambda registration")
	}
	// The contract accepts compression on neither register nor update, so it must not be sent.
	if _, present := body["compression"]; present {
		t.Errorf("compression was sent: %v", body["compression"])
	}
}

func TestCreateDeploymentSendsAuthentication(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments", testutil.JSON(`{"id":"dp_new","services":[]}`))

	if _, err := newClient(t, server).Deployments().Create(context.Background(), admin.RegisterInput{
		HTTP: &admin.RegisterHTTPDeployment{
			URI:  "https://svc.run.app",
			Auth: &admin.HTTPAuth{GoogleIDToken: &admin.GoogleIDTokenAuth{Audience: "https://svc.run.app"}},
		},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body struct {
		Auth struct {
			GoogleIDToken struct {
				Audience                  string `json:"audience"`
				ImpersonateServiceAccount string `json:"impersonate_service_account"`
			} `json:"GoogleIdToken"`
		} `json:"auth"`
	}
	server.LastRequest().DecodeBody(t, &body)
	if body.Auth.GoogleIDToken.Audience != "https://svc.run.app" {
		t.Errorf("audience = %q", body.Auth.GoogleIDToken.Audience)
	}
	// An unset optional inside the auth object must be omitted, not sent as an empty string.
	if body.Auth.GoogleIDToken.ImpersonateServiceAccount != "" {
		t.Errorf("impersonate_service_account = %q, want it omitted", body.Auth.GoogleIDToken.ImpersonateServiceAccount)
	}
}

func TestCreateDeploymentRequiresExactlyOneEndpointKind(t *testing.T) {
	tests := []struct {
		name  string
		input admin.RegisterInput
	}{
		{"neither", admin.RegisterInput{}},
		{"both", admin.RegisterInput{
			HTTP:   &admin.RegisterHTTPDeployment{URI: "https://svc"},
			Lambda: &admin.RegisterLambdaDeployment{ARN: "arn:aws:lambda:::function:f"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := testutil.NewAPIServer(t)
			if _, err := newClient(t, server).Deployments().Create(context.Background(), tt.input); err == nil {
				t.Fatal("expected an error")
			}
			if len(server.Requests()) != 0 {
				t.Error("a request was sent for an ambiguous endpoint kind")
			}
		})
	}
}

func TestCreateDeploymentRequiresTheVariantsIdentifier(t *testing.T) {
	server := testutil.NewAPIServer(t)
	deployments := newClient(t, server).Deployments()

	if _, err := deployments.Create(context.Background(), admin.RegisterInput{HTTP: &admin.RegisterHTTPDeployment{}}); err == nil {
		t.Error("expected an error for an HTTP deployment with no URI")
	}
	if _, err := deployments.Create(context.Background(), admin.RegisterInput{Lambda: &admin.RegisterLambdaDeployment{}}); err == nil {
		t.Error("expected an error for a Lambda deployment with no ARN")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent despite invalid arguments")
	}
}

func TestCreateDeploymentRejectsAResponseWithNoIdentifier(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments", testutil.JSON(`{"id":"","services":[]}`))

	if _, err := newClient(t, server).Deployments().Create(context.Background(), admin.RegisterInput{
		HTTP: &admin.RegisterHTTPDeployment{URI: "https://svc"},
	}); err == nil {
		t.Fatal("expected an error")
	}
}

// Registration performs discovery against the user's own endpoint, which can fail for reasons that
// have nothing to do with Restate. The server's explanation has to survive into the diagnostic.
func TestCreateDeploymentSurfacesADiscoveryFailure(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments", testutil.Response{
		Status: http.StatusBadRequest,
		Body:   `{"message":"failed to discover deployment: connection refused","restate_code":"META0003"}`,
	})

	_, err := newClient(t, server).Deployments().Create(context.Background(), admin.RegisterInput{
		HTTP: &admin.RegisterHTTPDeployment{URI: "https://unreachable.example.com"},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "failed to discover deployment") {
		t.Errorf("error does not carry the server's explanation: %v", err)
	}
	if !strings.Contains(err.Error(), "META0003") {
		t.Errorf("error does not carry the Restate error code: %v", err)
	}
}

func TestUpdateDeploymentRereadsTheResult(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments/dp_1a2b3c",
		testutil.Status(http.StatusOK),    // the PATCH
		testutil.JSON(httpDeploymentJSON), // the subsequent GET
	)

	got, err := newClient(t, server).Deployments().Update(context.Background(), "dp_1a2b3c", admin.UpdateInput{
		HTTP: &admin.UpdateHTTPDeployment{URI: "https://orders.example.com", Overwrite: true},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.ID != "dp_1a2b3c" {
		t.Errorf("ID = %q", got.ID)
	}

	requests := server.Requests()
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want a PATCH followed by a GET", len(requests))
	}
	if requests[0].Method != http.MethodPatch {
		t.Errorf("first request = %s, want PATCH", requests[0].Method)
	}
	// Re-reading means state describes what a later refresh would see, so an update cannot leave
	// state disagreeing with the read path.
	if requests[1].Method != http.MethodGet {
		t.Errorf("second request = %s, want GET", requests[1].Method)
	}

	var body map[string]any
	requests[0].DecodeBody(t, &body)
	if body["overwrite"] != true {
		t.Errorf("overwrite = %v", body["overwrite"])
	}
	// The contract's update request accepts neither field, so neither may be sent; changes to them
	// must replace the deployment instead.
	for _, field := range []string{"auth", "metadata"} {
		if _, present := body[field]; present {
			t.Errorf("%s was sent in an update, which the contract cannot apply: %v", field, body[field])
		}
	}
}

func TestUpdateDeploymentOmitsUnsetFields(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments/dp_1", testutil.Status(http.StatusOK), testutil.JSON(httpDeploymentJSON))

	if _, err := newClient(t, server).Deployments().Update(context.Background(), "dp_1", admin.UpdateInput{
		HTTP: &admin.UpdateHTTPDeployment{Overwrite: true},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	var body map[string]any
	server.Requests()[0].DecodeBody(t, &body)
	for _, field := range []string{"uri", "use_http_11", "additional_headers"} {
		if _, present := body[field]; present {
			t.Errorf("%s was sent despite not being configured: %v", field, body[field])
		}
	}
}

func TestUpdateLambdaDeployment(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments/dp_lambda", testutil.Status(http.StatusOK), testutil.JSON(lambdaDeploymentJSON))

	if _, err := newClient(t, server).Deployments().Update(context.Background(), "dp_lambda", admin.UpdateInput{
		Lambda: &admin.UpdateLambdaDeployment{ARN: "arn:aws:lambda:us-east-1:123456789012:function:orders:2"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	var body map[string]any
	server.Requests()[0].DecodeBody(t, &body)
	if body["arn"] != "arn:aws:lambda:us-east-1:123456789012:function:orders:2" {
		t.Errorf("arn = %v", body["arn"])
	}
}

func TestUpdateDeploymentValidatesItsArguments(t *testing.T) {
	server := testutil.NewAPIServer(t)
	deployments := newClient(t, server).Deployments()

	if _, err := deployments.Update(context.Background(), "", admin.UpdateInput{HTTP: &admin.UpdateHTTPDeployment{}}); err == nil {
		t.Error("expected an error for a missing identifier")
	}
	if _, err := deployments.Update(context.Background(), "dp_1", admin.UpdateInput{}); err == nil {
		t.Error("expected an error for an unspecified endpoint kind")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent despite invalid arguments")
	}
}

// Forced deletion is the only mode the contract implements and it can break in-flight invocations,
// so it has to be asked for.
func TestDeleteDeploymentRefusesWithoutAnExplicitOptIn(t *testing.T) {
	server := testutil.NewAPIServer(t)

	err := newClient(t, server).Deployments().Delete(context.Background(), "dp_1", admin.DeleteDeploymentOptions{})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "unrecoverable") {
		t.Errorf("error does not explain the risk: %v", err)
	}
	if len(server.Requests()) != 0 {
		t.Error("a delete was sent without the opt-in")
	}
}

func TestDeleteDeploymentSendsForceWhenOptedIn(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments/dp_1", testutil.Status(http.StatusAccepted))

	if err := newClient(t, server).Deployments().Delete(context.Background(), "dp_1", admin.DeleteDeploymentOptions{Force: true}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	request := server.LastRequest()
	if request.Method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", request.Method)
	}
	if request.Query != "force=true" {
		t.Errorf("query = %q, want force=true", request.Query)
	}
}

func TestDeleteDeploymentTreatsNotFoundAsSuccess(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments/dp_1", testutil.Response{
		Status: http.StatusNotFound,
		Body:   `{"message":"deployment not found"}`,
	})

	if err := newClient(t, server).Deployments().Delete(context.Background(), "dp_1", admin.DeleteDeploymentOptions{Force: true}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// Deployment headers carry downstream API keys. No failure path may put one into an error string.
func TestDeploymentErrorsNeverContainHeaderValues(t *testing.T) {
	const secret = "key_live_downstream_api_credential"
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments", testutil.Response{
		Status: http.StatusBadRequest,
		Body:   `{"message":"rejected header Authorization: Bearer ` + secret + `"}`,
	})

	_, err := newClient(t, server).Deployments().Create(context.Background(), admin.RegisterInput{
		HTTP: &admin.RegisterHTTPDeployment{
			URI:               "https://svc",
			AdditionalHeaders: map[string]string{"authorization": "Bearer " + secret},
		},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaked a header value: %v", err)
	}
	if !strings.Contains(err.Error(), api.Redacted) {
		t.Errorf("error is missing the redaction placeholder: %v", err)
	}
}

// A caller mutating its map after the call must not be able to change what was sent, nor what the
// returned value reports.
func TestDeploymentMapsAreCopied(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.UseFullPath = true
	server.On("/deployments", testutil.JSON(`{"id":"dp_new","services":[]}`))

	headers := map[string]string{"x-api-key": "original"}
	if _, err := newClient(t, server).Deployments().Create(context.Background(), admin.RegisterInput{
		HTTP: &admin.RegisterHTTPDeployment{URI: "https://svc", AdditionalHeaders: headers},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	headers["x-api-key"] = "mutated"

	var body struct {
		AdditionalHeaders map[string]string `json:"additional_headers"`
	}
	server.LastRequest().DecodeBody(t, &body)
	if body.AdditionalHeaders["x-api-key"] != "original" {
		t.Errorf("sent header = %q, want the value at call time", body.AdditionalHeaders["x-api-key"])
	}
}
