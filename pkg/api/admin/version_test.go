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

const testAPIKey = "key_environment_test_credential"

func newClient(t *testing.T, server *testutil.APIServer) *admin.Client {
	t.Helper()
	client, err := admin.New(admin.Config{
		BaseURL:           server.URL,
		APIKey:            testAPIKey,
		UserAgent:         "terraform-provider-restate/test",
		AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	return client
}

func TestNewValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name string
		cfg  admin.Config
		want string
	}{
		// The Admin URL has no default on purpose: deriving it from an environment ID would bake
		// today's hostname shape into the provider.
		{"missing base URL", admin.Config{APIKey: "k"}, "base URL is required"},
		{"missing key", admin.Config{BaseURL: "https://env.restate.cloud:9070"}, "environment API key is required"},
		{"unusable base URL", admin.Config{BaseURL: "://nope", APIKey: "k"}, "base URL"},
		{"plaintext remote base URL", admin.Config{BaseURL: "http://env.restate.cloud:9070", APIKey: "k"}, "allow_insecure_http"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := admin.New(tt.cfg)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// A self-hosted Restate on a private network is a legitimate target, but the opt-in must be
// explicit because a bearer token over plaintext is a credential leak.
func TestNewAllowsAnExplicitInsecureOverride(t *testing.T) {
	if _, err := admin.New(admin.Config{
		BaseURL:           "http://restate.internal:9070",
		APIKey:            "k",
		AllowInsecureHTTP: true,
	}); err != nil {
		t.Fatalf("admin.New: %v", err)
	}
}

const versionJSON = `{
  "version": "1.7.6",
  "min_admin_api_version": 2,
  "max_admin_api_version": 4,
  "ingress_endpoint": "https://env_1.env.us.restate.cloud",
  "features": {"invocation_status_killed": true, "vqueues": false}
}`

func TestServerInfo(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("version", testutil.JSON(versionJSON))

	got, err := newClient(t, server).ServerInfo(context.Background())
	if err != nil {
		t.Fatalf("ServerInfo: %v", err)
	}

	if got.Version != "1.7.6" {
		t.Errorf("Version = %q", got.Version)
	}
	if got.MinAdminAPIVersion != 2 || got.MaxAdminAPIVersion != 4 {
		t.Errorf("version range = %d..%d, want 2..4", got.MinAdminAPIVersion, got.MaxAdminAPIVersion)
	}
	if got.IngressEndpoint != "https://env_1.env.us.restate.cloud" {
		t.Errorf("IngressEndpoint = %q", got.IngressEndpoint)
	}
	if !got.Features["invocation_status_killed"] || got.Features["vqueues"] {
		t.Errorf("Features = %v", got.Features)
	}

	request := server.LastRequest()
	if request.Method != http.MethodGet || request.Path != "/version" {
		t.Errorf("request = %s %s, want GET /version", request.Method, request.Path)
	}
	if want := "Bearer " + testAPIKey; request.Authorization != want {
		t.Errorf("Authorization = %q, want the environment key", request.Authorization)
	}
}

// ingress_endpoint is nullable in the contract, and an absent one must not be a decode failure.
func TestServerInfoToleratesAnAbsentIngressEndpoint(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("version", testutil.JSON(`{"version":"1.7.6","min_admin_api_version":2,"max_admin_api_version":4,"features":{},"ingress_endpoint":null}`))

	got, err := newClient(t, server).ServerInfo(context.Background())
	if err != nil {
		t.Fatalf("ServerInfo: %v", err)
	}
	if got.IngressEndpoint != "" {
		t.Errorf("IngressEndpoint = %q, want empty", got.IngressEndpoint)
	}
}

// Pointing admin_url at something that is not a Restate Admin API is a common misconfiguration. A
// response with no version information at all must say so rather than be treated as compatible.
func TestServerInfoRejectsAResponseWithNoVersionInformation(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("version", testutil.JSON(`{}`))

	_, err := newClient(t, server).ServerInfo(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "may not be a Restate Admin API") {
		t.Errorf("error does not explain the likely cause: %v", err)
	}
}

func TestServerInfoRejectsAMalformedResponse(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("version", testutil.Response{Status: http.StatusOK, Body: `{"version": `})

	if _, err := newClient(t, server).ServerInfo(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCheckCompatibility(t *testing.T) {
	tests := []struct {
		name           string
		min, max       int32
		wantCompatible bool
		wantNegotiated int32
		wantWarning    bool
		wantProblem    string
	}{
		{
			name: "server matches the pinned contract",
			min:  2, max: 4,
			wantCompatible: true, wantNegotiated: 4,
		},
		{
			// The provider's lower bound: a server that accepts nothing older than v2 and nothing
			// newer either.
			name: "server at the lower boundary",
			min:  2, max: 2,
			wantCompatible: true, wantNegotiated: 2, wantWarning: true,
		},
		{
			name: "server below the supported range",
			min:  0, max: 1,
			wantProblem: "requires at least version 2",
		},
		{
			name: "server above the supported range",
			min:  5, max: 7,
			wantProblem: "Upgrade the provider",
		},
		{
			// Overlapping but extending past the snapshot: the provider negotiates down to its own
			// maximum, which it can speak, so this is compatible with no warning.
			name: "server range extends past the snapshot",
			min:  3, max: 9,
			wantCompatible: true, wantNegotiated: 4,
		},
		{
			name: "server reports an inverted range",
			min:  4, max: 2,
			wantProblem: "invalid Admin API version range",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := admin.CheckCompatibility(admin.ServerInfo{
				Version:            "1.7.6",
				MinAdminAPIVersion: tt.min,
				MaxAdminAPIVersion: tt.max,
			})

			if got.Compatible != tt.wantCompatible {
				t.Errorf("Compatible = %v, want %v (problem: %q)", got.Compatible, tt.wantCompatible, got.Problem)
			}
			if got.NegotiatedVersion != tt.wantNegotiated {
				t.Errorf("NegotiatedVersion = %d, want %d", got.NegotiatedVersion, tt.wantNegotiated)
			}
			if (got.Warning != "") != tt.wantWarning {
				t.Errorf("Warning = %q, want a warning: %v", got.Warning, tt.wantWarning)
			}
			if tt.wantProblem != "" && !strings.Contains(got.Problem, tt.wantProblem) {
				t.Errorf("Problem = %q, want it to mention %q", got.Problem, tt.wantProblem)
			}
			if tt.wantProblem == "" && got.Problem != "" {
				t.Errorf("Problem = %q, want none", got.Problem)
			}
		})
	}
}

// An incompatibility diagnostic has to name both sides' ranges, or the operator cannot tell which
// end to move.
func TestCompatibilityProblemNamesBothRanges(t *testing.T) {
	got := admin.CheckCompatibility(admin.ServerInfo{Version: "1.2.3", MinAdminAPIVersion: 0, MaxAdminAPIVersion: 1})
	for _, want := range []string{"1.2.3", "0 to 1", "version 2"} {
		if !strings.Contains(got.Problem, want) {
			t.Errorf("Problem %q is missing %q", got.Problem, want)
		}
	}
}

// A server that reports no version string must still produce a usable message rather than an
// awkward empty gap.
func TestCompatibilityHandlesAnUnreportedServerVersion(t *testing.T) {
	got := admin.CheckCompatibility(admin.ServerInfo{MinAdminAPIVersion: 0, MaxAdminAPIVersion: 1})
	if !strings.Contains(got.Problem, "version not reported") {
		t.Errorf("Problem = %q", got.Problem)
	}
}

func TestCheckServerCompatibility(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("version", testutil.JSON(versionJSON))

	got, err := newClient(t, server).CheckServerCompatibility(context.Background())
	if err != nil {
		t.Fatalf("CheckServerCompatibility: %v", err)
	}
	if !got.Compatible {
		t.Errorf("Compatible = false: %s", got.Problem)
	}
	if got.Server.Version != "1.7.6" {
		t.Errorf("Server.Version = %q", got.Server.Version)
	}
}

func TestHealth(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("health", testutil.Status(http.StatusOK))

	if err := newClient(t, server).Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if request := server.LastRequest(); request.Path != "/health" {
		t.Errorf("path = %q, want /health", request.Path)
	}
}

func TestHealthReportsAnUnhealthyServer(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("health", testutil.Status(http.StatusServiceUnavailable))

	if err := newClient(t, server).Health(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}

// The Admin API's error body differs from the Cloud API's, and both the message and the Restate
// error code have to survive into the diagnostic.
func TestAdminErrorBodyIsDecoded(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("version", testutil.Response{
		Status: http.StatusBadRequest,
		Body:   `{"message":"invalid request","restate_code":"META0003"}`,
	})

	_, err := newClient(t, server).ServerInfo(context.Background())
	apiErr, ok := api.AsError(err)
	if !ok {
		t.Fatalf("not an API error: %v", err)
	}
	if apiErr.Message != "invalid request" {
		t.Errorf("Message = %q", apiErr.Message)
	}
	if apiErr.Code != "META0003" {
		t.Errorf("Code = %q, want META0003", apiErr.Code)
	}
	if apiErr.Service != api.ServiceAdmin {
		t.Errorf("Service = %q, want the Admin API", apiErr.Service)
	}
}

// A gateway in front of an environment can substitute an HTML error page. That must not become a
// decode failure that hides which status was returned.
func TestAdminErrorToleratesANonJSONBody(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("version", testutil.Response{Status: http.StatusBadGateway, Body: "<html>502 Bad Gateway</html>"})

	_, err := newClient(t, server).ServerInfo(context.Background())
	apiErr, ok := api.AsError(err)
	if !ok {
		t.Fatalf("not an API error: %v", err)
	}
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
	}
}
