package cloud_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

const (
	testAccountID = "acc_1a2b3c"
	testKey       = "key_management_test_credential"
)

// newClient wires a facade to a scripted test server. Polling is instant so lifecycle tests do not
// spend real time waiting.
func newClient(t *testing.T, server *testutil.APIServer) *cloud.Client {
	t.Helper()
	client, err := cloud.New(cloud.Config{
		BaseURL:           server.URL,
		AccountID:         testAccountID,
		ManagementAPIKey:  testKey,
		UserAgent:         "terraform-provider-restate/test",
		Poll:              api.PollConfig{Interval: time.Millisecond},
		AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatalf("cloud.New: %v", err)
	}
	return client
}

func TestNewValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name string
		cfg  cloud.Config
		want string
	}{
		{"missing account", cloud.Config{ManagementAPIKey: "k"}, "account ID is required"},
		{"blank account", cloud.Config{AccountID: "  ", ManagementAPIKey: "k"}, "account ID is required"},
		{"missing key", cloud.Config{AccountID: testAccountID}, "management API key is required"},
		{"blank key", cloud.Config{AccountID: testAccountID, ManagementAPIKey: " "}, "management API key is required"},
		{"unusable base URL", cloud.Config{AccountID: testAccountID, ManagementAPIKey: "k", BaseURL: "not a url"}, "must use http or https"},
		{"plaintext remote base URL", cloud.Config{AccountID: testAccountID, ManagementAPIKey: "k", BaseURL: "http://api.us.restate.cloud/cloud"}, "allow_insecure_http"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cloud.New(tt.cfg)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestNewDefaultsToTheCanonicalBaseURL(t *testing.T) {
	client, err := cloud.New(cloud.Config{AccountID: testAccountID, ManagementAPIKey: testKey})
	if err != nil {
		t.Fatalf("cloud.New: %v", err)
	}
	if got := client.BaseURL(); got != cloud.DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", got, cloud.DefaultBaseURL)
	}
	// The "/cloud" suffix is part of the endpoint and must survive normalization.
	if !strings.HasSuffix(client.BaseURL(), "/cloud") {
		t.Errorf("BaseURL %q lost its /cloud path prefix", client.BaseURL())
	}
}

func TestNewPreservesAnOverriddenBaseURLPath(t *testing.T) {
	client, err := cloud.New(cloud.Config{
		BaseURL:          "https://api.dev.restate.cloud/cloud/",
		AccountID:        testAccountID,
		ManagementAPIKey: testKey,
	})
	if err != nil {
		t.Fatalf("cloud.New: %v", err)
	}
	if got, want := client.BaseURL(), "https://api.dev.restate.cloud/cloud"; got != want {
		t.Errorf("BaseURL = %q, want %q", got, want)
	}
}

func TestRequestsCarryBearerAuthenticationAndTheAccountPath(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeEnvironment", testutil.JSON(activeEnvironmentJSON))

	if _, err := newClient(t, server).Environments().Get(context.Background(), "env_1a2b3c"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	request := server.LastRequest()
	if want := "Bearer " + testKey; request.Authorization != want {
		t.Errorf("Authorization = %q, want %q", request.Authorization, want)
	}
	if want := "/" + testAccountID + "/DescribeEnvironment"; request.Path != want {
		t.Errorf("path = %q, want %q", request.Path, want)
	}
	if request.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", request.Method)
	}
	if !strings.Contains(request.ContentType, "application/json") {
		t.Errorf("Content-Type = %q", request.ContentType)
	}
	if request.UserAgent != "terraform-provider-restate/test" {
		t.Errorf("User-Agent = %q", request.UserAgent)
	}
}

// An account ID is a server-assigned opaque string, but it lands in the URL path, so it must be
// escaped rather than concatenated.
func TestAccountIDIsEscapedIntoThePath(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeAccount", testutil.JSON(`{"environments":[]}`))

	client, err := cloud.New(cloud.Config{
		BaseURL:           server.URL,
		AccountID:         "acc_with space/and?query",
		ManagementAPIKey:  testKey,
		AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatalf("cloud.New: %v", err)
	}
	if _, err := client.DescribeAccount(context.Background()); err != nil {
		t.Fatalf("DescribeAccount: %v", err)
	}

	request := server.LastRequest()
	if strings.Contains(request.Query, "query") {
		t.Errorf("the account ID leaked into the query string: %q", request.Query)
	}
	if !strings.Contains(request.RawPath, "%20") {
		t.Errorf("raw path %q does not show the space escaped", request.RawPath)
	}
}

const activeEnvironmentJSON = `{
  "environmentId": "env_1a2b3c",
  "region": "us-east-1",
  "regionDisplayName": "US East (N. Virginia)",
  "name": "production",
  "status": "ACTIVE",
  "signingPublicKey": "publickeyv1_abc123",
  "apiKeys": [{"keyId": "key_1", "environmentId": "env_1a2b3c"}],
  "ingressBaseUrl": "https://env_1a2b3c.env.us.restate.cloud",
  "adminBaseUrl": "https://env_1a2b3c.env.us.restate.cloud:9070",
  "invocationMetricsUrl": "https://metrics.us.restate.cloud/env_1a2b3c",
  "tunnelBaseUrl": "https://tunnel.us.restate.cloud",
  "tunnelSrv": "_tunnel._tcp.us.restate.cloud",
  "proxyBaseUrl": "https://proxy.us.restate.cloud",
  "tierId": "BUSINESS"
}`

func TestGetEnvironmentMapsEveryField(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeEnvironment", testutil.JSON(activeEnvironmentJSON))

	got, err := newClient(t, server).Environments().Get(context.Background(), "env_1a2b3c")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	want := cloud.Environment{
		ID:                   "env_1a2b3c",
		Name:                 "production",
		Region:               "us-east-1",
		RegionDisplayName:    "US East (N. Virginia)",
		Status:               cloud.StatusActive,
		Tier:                 cloud.TierBusiness,
		SigningPublicKey:     "publickeyv1_abc123",
		IngressBaseURL:       "https://env_1a2b3c.env.us.restate.cloud",
		AdminBaseURL:         "https://env_1a2b3c.env.us.restate.cloud:9070",
		InvocationMetricsURL: "https://metrics.us.restate.cloud/env_1a2b3c",
		TunnelBaseURL:        "https://tunnel.us.restate.cloud",
		TunnelSRV:            "_tunnel._tcp.us.restate.cloud",
		ProxyBaseURL:         "https://proxy.us.restate.cloud",
		APIKeyIDs:            []string{"key_1"},
	}
	assertEnvironment(t, got, want)

	var body struct {
		EnvironmentID string `json:"environmentId"`
	}
	server.LastRequest().DecodeBody(t, &body)
	if body.EnvironmentID != "env_1a2b3c" {
		t.Errorf("request body environmentId = %q", body.EnvironmentID)
	}
}

// The contract marks regionDisplayName and invocationMetricsUrl optional. Absent must map to empty,
// not to a decode failure, and must be distinguishable from a present empty string only in that
// both are empty — which is the correct behaviour for a computed output.
func TestGetEnvironmentToleratesAbsentOptionalFields(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeEnvironment", testutil.JSON(`{
	  "environmentId": "env_1", "region": "us-east-1", "name": "prod",
	  "status": "PENDING", "signingPublicKey": "pk", "apiKeys": [],
	  "ingressBaseUrl": "https://i", "adminBaseUrl": "https://a",
	  "tunnelBaseUrl": "https://t", "tunnelSrv": "srv", "proxyBaseUrl": "https://p",
	  "tierId": "FREE"
	}`))

	got, err := newClient(t, server).Environments().Get(context.Background(), "env_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RegionDisplayName != "" {
		t.Errorf("RegionDisplayName = %q, want empty", got.RegionDisplayName)
	}
	if got.InvocationMetricsURL != "" {
		t.Errorf("InvocationMetricsURL = %q, want empty", got.InvocationMetricsURL)
	}
	// An environment with no keys must map to an empty slice rather than nil, so a caller
	// converting it to a Terraform list gets an empty list, not a null.
	if got.APIKeyIDs == nil || len(got.APIKeyIDs) != 0 {
		t.Errorf("APIKeyIDs = %#v, want an empty non-nil slice", got.APIKeyIDs)
	}
}

func TestGetEnvironmentRequiresAnID(t *testing.T) {
	server := testutil.NewAPIServer(t)
	if _, err := newClient(t, server).Environments().Get(context.Background(), ""); err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent despite the missing identifier")
	}
}

func TestGetExistingTreatsBothFormsOfAbsenceAsGone(t *testing.T) {
	tests := []struct {
		name     string
		response testutil.Response
	}{
		{"not found", testutil.Error(http.StatusNotFound, "environment not found")},
		{"reported as deleted", testutil.JSON(`{
		  "environmentId": "env_1", "region": "us-east-1", "name": "prod",
		  "status": "DELETED", "signingPublicKey": "pk", "apiKeys": [],
		  "ingressBaseUrl": "https://i", "adminBaseUrl": "https://a",
		  "tunnelBaseUrl": "https://t", "tunnelSrv": "srv", "proxyBaseUrl": "https://p",
		  "tierId": "FREE"
		}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := testutil.NewAPIServer(t)
			server.On("DescribeEnvironment", tt.response)

			_, err := newClient(t, server).Environments().GetExisting(context.Background(), "env_1")
			if !errors.Is(err, cloud.ErrEnvironmentGone) {
				t.Fatalf("err = %v, want ErrEnvironmentGone", err)
			}
			if !strings.Contains(err.Error(), "env_1") {
				t.Errorf("error does not name the environment: %v", err)
			}
		})
	}
}

func TestListEnvironments(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("ListEnvironments", testutil.JSON(`{"environments":[
	  {"environmentId":"env_1","region":"us-east-1","name":"prod","tierId":"BUSINESS"},
	  {"environmentId":"env_2","region":"eu-central-1","name":"staging","tierId":"FREE"}
	]}`))

	got, err := newClient(t, server).Environments().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []cloud.EnvironmentSummary{
		{ID: "env_1", Name: "prod", Region: "us-east-1", Tier: cloud.TierBusiness},
		{ID: "env_2", Name: "staging", Region: "eu-central-1", Tier: cloud.TierFree},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d environments, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("environment %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListEnvironmentsOnAnEmptyAccount(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("ListEnvironments", testutil.JSON(`{"environments":[]}`))

	got, err := newClient(t, server).Environments().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d environments, want 0", len(got))
	}
}

func TestCreateEnvironmentSendsOnlyWhatWasAsked(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateEnvironment", testutil.JSON(`{"environmentId":"env_new","name":"production"}`))

	got, err := newClient(t, server).Environments().Create(context.Background(), cloud.CreateEnvironmentInput{
		Name: "production",
		Tier: cloud.TierBusiness,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID != "env_new" || got.Name != "production" {
		t.Errorf("created %+v", got)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["name"] != "production" {
		t.Errorf("name = %v", body["name"])
	}
	if body["tierId"] != "BUSINESS" {
		t.Errorf("tierId = %v", body["tierId"])
	}
	// An omitted region must be omitted from the wire, not sent as an empty string that the API
	// would have to interpret.
	if _, present := body["region"]; present {
		t.Errorf("region was sent despite not being configured: %v", body["region"])
	}
	// Supplying payment URLs is how a caller opts into browser checkout. The facade must never do
	// that on the caller's behalf.
	if _, present := body["payment"]; present {
		t.Error("a payment object was sent, which would opt into an interactive checkout flow")
	}
	if _, present := body["profileId"]; present {
		t.Error("a profileId was sent; custom profiles are deliberately unmanaged")
	}
}

func TestCreateEnvironmentSendsAnExplicitRegion(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateEnvironment", testutil.JSON(`{"environmentId":"env_new","name":"prod"}`))

	if _, err := newClient(t, server).Environments().Create(context.Background(), cloud.CreateEnvironmentInput{
		Name:   "prod",
		Region: "eu-central-1",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["region"] != "eu-central-1" {
		t.Errorf("region = %v", body["region"])
	}
	if _, present := body["tierId"]; present {
		t.Errorf("tierId was sent despite not being configured: %v", body["tierId"])
	}
}

func TestCreateEnvironmentRejectsAnUnrequestableTier(t *testing.T) {
	server := testutil.NewAPIServer(t)
	_, err := newClient(t, server).Environments().Create(context.Background(), cloud.CreateEnvironmentInput{
		Name: "prod",
		Tier: cloud.TierCustom,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent for an unrequestable tier")
	}
}

func TestCreateEnvironmentRequiresAName(t *testing.T) {
	server := testutil.NewAPIServer(t)
	if _, err := newClient(t, server).Environments().Create(context.Background(), cloud.CreateEnvironmentInput{}); err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent without a name")
	}
}

// Terraform cannot complete a browser payment flow. A checkout URL must be refused, and the
// environment ID must survive the refusal so an operator can clean up.
func TestCreateEnvironmentRefusesAnInteractiveCheckout(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateEnvironment", testutil.JSON(`{
	  "environmentId":"env_awaiting","name":"prod",
	  "checkoutUrl":"https://checkout.stripe.com/c/pay/cs_test_abc"
	}`))

	created, err := newClient(t, server).Environments().Create(context.Background(), cloud.CreateEnvironmentInput{
		Name: "prod",
		Tier: cloud.TierBusiness,
	})
	if !cloud.IsInteractiveCheckoutRequired(err) {
		t.Fatalf("err = %v, want an interactive-checkout refusal", err)
	}
	if created.ID != "env_awaiting" {
		t.Errorf("created.ID = %q, want the identifier preserved for cleanup", created.ID)
	}
	if !strings.Contains(err.Error(), "env_awaiting") {
		t.Errorf("error does not name the environment: %v", err)
	}
	// The checkout URL is a single-use payment link; it must not be echoed into diagnostics.
	if strings.Contains(err.Error(), "cs_test_abc") {
		t.Errorf("error leaked the checkout session URL: %v", err)
	}
}

// A success response missing the identifier would otherwise produce a resource with an empty ID
// that Terraform can neither refresh nor destroy.
func TestCreateEnvironmentRejectsAResponseWithNoIdentifier(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateEnvironment", testutil.JSON(`{"environmentId":"","name":"prod"}`))

	if _, err := newClient(t, server).Environments().Create(context.Background(), cloud.CreateEnvironmentInput{Name: "prod"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestRenameEnvironment(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("UpdateEnvironment", testutil.JSON(`{"environmentId":"env_1","name":"renamed"}`))

	if err := newClient(t, server).Environments().Rename(context.Background(), "env_1", "renamed"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["environmentId"] != "env_1" || body["name"] != "renamed" {
		t.Errorf("request body = %v", body)
	}
}

// If the API accepts a rename but applies something else, the next plan would show drift the
// provider could not explain. Fail at the point of the discrepancy instead.
func TestRenameEnvironmentDetectsAnUnappliedChange(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("UpdateEnvironment", testutil.JSON(`{"environmentId":"env_1","name":"something-else"}`))

	err := newClient(t, server).Environments().Rename(context.Background(), "env_1", "renamed")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "something-else") {
		t.Errorf("error does not report what was applied: %v", err)
	}
}

func TestRenameEnvironmentValidatesItsArguments(t *testing.T) {
	server := testutil.NewAPIServer(t)
	environments := newClient(t, server).Environments()
	if err := environments.Rename(context.Background(), "", "name"); err == nil {
		t.Error("expected an error for a missing environment ID")
	}
	if err := environments.Rename(context.Background(), "env_1", ""); err == nil {
		t.Error("expected an error for a missing name")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent despite invalid arguments")
	}
}

func TestSetTier(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("UpdateEnvironmentTier", testutil.JSON(`{}`))

	if err := newClient(t, server).Environments().SetTier(context.Background(), "env_1", cloud.TierPremium); err != nil {
		t.Fatalf("SetTier: %v", err)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["tierId"] != "PREMIUM" {
		t.Errorf("tierId = %v", body["tierId"])
	}
	if _, present := body["payment"]; present {
		t.Error("a payment object was sent, which would opt into an interactive checkout flow")
	}
}

func TestSetTierRefusesAnInteractiveCheckout(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("UpdateEnvironmentTier", testutil.JSON(`{"checkoutUrl":"https://checkout.stripe.com/c/pay/cs_test_abc"}`))

	err := newClient(t, server).Environments().SetTier(context.Background(), "env_1", cloud.TierPremium)
	if !cloud.IsInteractiveCheckoutRequired(err) {
		t.Fatalf("err = %v, want an interactive-checkout refusal", err)
	}
	if strings.Contains(err.Error(), "cs_test_abc") {
		t.Errorf("error leaked the checkout session URL: %v", err)
	}
}

func TestSetTierRejectsAnUnrequestableTier(t *testing.T) {
	server := testutil.NewAPIServer(t)
	if err := newClient(t, server).Environments().SetTier(context.Background(), "env_1", cloud.TierCustom); err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent for an unrequestable tier")
	}
}

func TestDeleteEnvironment(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DestroyEnvironment", testutil.JSON(`{"result":{"ok":true}}`))

	if err := newClient(t, server).Environments().Delete(context.Background(), "env_1", cloud.DestroyOptions{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if server.CallCount("DestroyEnvironment") != 1 {
		t.Errorf("DestroyEnvironment called %d times, want 1", server.CallCount("DestroyEnvironment"))
	}
}

// A destroy that finds nothing to destroy has converged. Reporting an error would strand the
// resource in state after a partially failed destroy.
func TestDeleteEnvironmentTreatsNotFoundAsSuccess(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DestroyEnvironment", testutil.Error(http.StatusNotFound, "environment not found"))

	if err := newClient(t, server).Environments().Delete(context.Background(), "env_1", cloud.DestroyOptions{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// The contract types the destroy result as either an object or a bare string. Neither shape carries
// information the provider acts on, so both must be accepted.
func TestDeleteEnvironmentAcceptsEitherResultShape(t *testing.T) {
	for _, body := range []string{`{"result":{"ok":true}}`, `{"result":"deleted"}`, `{}`, ``} {
		t.Run(body, func(t *testing.T) {
			server := testutil.NewAPIServer(t)
			server.On("DestroyEnvironment", testutil.Response{Status: http.StatusOK, Body: body})

			if err := newClient(t, server).Environments().Delete(context.Background(), "env_1", cloud.DestroyOptions{}); err != nil {
				t.Fatalf("Delete: %v", err)
			}
		})
	}
}

func TestWaitUntilActivePollsUntilTerminal(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeEnvironment",
		testutil.JSON(environmentWithStatus("PENDING")),
		testutil.JSON(environmentWithStatus("PENDING")),
		testutil.JSON(activeEnvironmentJSON),
	)

	got, err := newClient(t, server).Environments().WaitUntilActive(context.Background(), "env_1a2b3c")
	if err != nil {
		t.Fatalf("WaitUntilActive: %v", err)
	}
	if got.Status != cloud.StatusActive {
		t.Errorf("status = %q, want ACTIVE", got.Status)
	}
	// The connection metadata only appears once provisioning finishes, which is precisely why the
	// create response cannot be trusted as proof of readiness.
	if got.AdminBaseURL == "" {
		t.Error("the polled environment has no admin base URL")
	}
	if count := server.CallCount("DescribeEnvironment"); count != 3 {
		t.Errorf("DescribeEnvironment called %d times, want 3", count)
	}
}

func TestWaitUntilActiveFailsFastOnFailedProvisioning(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeEnvironment", testutil.JSON(environmentWithStatus("FAILED")))

	_, err := newClient(t, server).Environments().WaitUntilActive(context.Background(), "env_1a2b3c")
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, api.ErrPollTimeout) {
		t.Errorf("a terminal failure was reported as a timeout: %v", err)
	}
	// The operator needs the identifier to find and clean up the failed environment.
	if !strings.Contains(err.Error(), "env_1a2b3c") {
		t.Errorf("error does not name the environment: %v", err)
	}
	if count := server.CallCount("DescribeEnvironment"); count != 1 {
		t.Errorf("DescribeEnvironment called %d times, want 1: a FAILED status must not be waited out", count)
	}
}

// A newly created environment can briefly be invisible to describe. That is "not ready yet", not a
// reason to fail the apply.
func TestWaitUntilActiveToleratesAnInitialNotFound(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeEnvironment",
		testutil.Error(http.StatusNotFound, "environment not found"),
		testutil.JSON(activeEnvironmentJSON),
	)

	if _, err := newClient(t, server).Environments().WaitUntilActive(context.Background(), "env_1a2b3c"); err != nil {
		t.Fatalf("WaitUntilActive: %v", err)
	}
}

// An expired credential must not be reported as a provisioning timeout.
func TestWaitUntilActiveAbortsOnAnAuthenticationFailure(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeEnvironment", testutil.Error(http.StatusUnauthorized, "token expired"))

	_, err := newClient(t, server).Environments().WaitUntilActive(context.Background(), "env_1")
	if !api.IsUnauthorized(err) {
		t.Fatalf("err = %v, want the 401 to surface", err)
	}
	if count := server.CallCount("DescribeEnvironment"); count != 1 {
		t.Errorf("DescribeEnvironment called %d times, want 1", count)
	}
}

func TestWaitUntilActiveHonoursTheCallersDeadline(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeEnvironment", testutil.JSON(environmentWithStatus("PENDING")))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := newClient(t, server).Environments().WaitUntilActive(ctx, "env_1")
	if !errors.Is(err, api.ErrPollTimeout) {
		t.Fatalf("err = %v, want ErrPollTimeout", err)
	}
}

func TestWaitUntilDeletedAcceptsBothFormsOfAbsence(t *testing.T) {
	tests := []struct {
		name      string
		responses []testutil.Response
	}{
		{"reported as deleted", []testutil.Response{
			testutil.JSON(environmentWithStatus("PENDING")),
			testutil.JSON(environmentWithStatus("DELETED")),
		}},
		{"becomes not found", []testutil.Response{
			testutil.JSON(environmentWithStatus("PENDING")),
			testutil.Error(http.StatusNotFound, "environment not found"),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := testutil.NewAPIServer(t)
			server.On("DescribeEnvironment", tt.responses...)

			if err := newClient(t, server).Environments().WaitUntilDeleted(context.Background(), "env_1"); err != nil {
				t.Fatalf("WaitUntilDeleted: %v", err)
			}
		})
	}
}

func TestWaitUntilDeletedFailsFastOnAFailedDestroy(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeEnvironment", testutil.JSON(environmentWithStatus("FAILED")))

	err := newClient(t, server).Environments().WaitUntilDeleted(context.Background(), "env_1")
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, api.ErrPollTimeout) {
		t.Errorf("a FAILED destroy was reported as a timeout: %v", err)
	}
}

func environmentWithStatus(status string) string {
	return strings.Replace(activeEnvironmentJSON, `"status": "ACTIVE"`, `"status": "`+status+`"`, 1)
}

func assertEnvironment(t *testing.T, got, want cloud.Environment) {
	t.Helper()
	if len(got.APIKeyIDs) != len(want.APIKeyIDs) {
		t.Errorf("APIKeyIDs = %v, want %v", got.APIKeyIDs, want.APIKeyIDs)
	} else {
		for i := range want.APIKeyIDs {
			if got.APIKeyIDs[i] != want.APIKeyIDs[i] {
				t.Errorf("APIKeyIDs[%d] = %q, want %q", i, got.APIKeyIDs[i], want.APIKeyIDs[i])
			}
		}
	}
	got.APIKeyIDs, want.APIKeyIDs = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("environment =\n  %+v\nwant\n  %+v", got, want)
	}
}
