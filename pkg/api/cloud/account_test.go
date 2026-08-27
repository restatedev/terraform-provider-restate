package cloud_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

func TestDescribeAccount(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeAccount", testutil.JSON(`{
	  "environments":[{"environmentId":"env_1","region":"us-east-1","name":"prod","tierId":"BUSINESS"}],
	  "billingMode":"account",
	  "subscription":{"status":"ACTIVE","currentPeriodStart":1756252800,"currentPeriodEnd":1758931200}
	}`))

	got, err := newClient(t, server).DescribeAccount(context.Background())
	if err != nil {
		t.Fatalf("DescribeAccount: %v", err)
	}

	if got.ID != testAccountID {
		t.Errorf("ID = %q, want the configured account", got.ID)
	}
	if got.BillingMode != cloud.BillingModeAccount {
		t.Errorf("BillingMode = %q", got.BillingMode)
	}
	if got.Subscription == nil {
		t.Fatal("Subscription is nil")
	}
	if got.Subscription.Status != cloud.SubscriptionActive {
		t.Errorf("Subscription.Status = %q", got.Subscription.Status)
	}
	if got.Subscription.CurrentPeriodStart != 1756252800 {
		t.Errorf("CurrentPeriodStart = %d", got.Subscription.CurrentPeriodStart)
	}
	if len(got.Environments) != 1 || got.Environments[0].ID != "env_1" {
		t.Errorf("Environments = %+v", got.Environments)
	}
}

// Only `environments` is required by the contract. A free account with no subscription must decode
// cleanly, and the absent subscription must be nil rather than a zero-valued struct that a caller
// could mistake for a real one.
func TestDescribeAccountToleratesAnAccountWithNoSubscription(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeAccount", testutil.JSON(`{"environments":[]}`))

	got, err := newClient(t, server).DescribeAccount(context.Background())
	if err != nil {
		t.Fatalf("DescribeAccount: %v", err)
	}
	if got.Subscription != nil {
		t.Errorf("Subscription = %+v, want nil", got.Subscription)
	}
	if got.BillingMode != "" {
		t.Errorf("BillingMode = %q, want empty", got.BillingMode)
	}
}

func TestValidateCredentialsSucceedsOnAReadableAccount(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeAccount", testutil.JSON(`{"environments":[]}`))

	if err := newClient(t, server).ValidateCredentials(context.Background()); err != nil {
		t.Fatalf("ValidateCredentials: %v", err)
	}
	// Validation must be exactly one read-only round trip, not a probe sequence.
	if count := server.CallCount("DescribeAccount"); count != 1 {
		t.Errorf("DescribeAccount called %d times, want 1", count)
	}
}

// "HTTP 403" alone does not tell an operator whether the key is wrong or is pointed at the wrong
// account. The diagnostic has to name both possibilities and the role the key needs.
func TestValidateCredentialsExplainsAnAuthorizationFailure(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantText []string
	}{
		{"rejected credential", http.StatusUnauthorized, []string{"rejected", "revoked"}},
		{"wrong account", http.StatusForbidden, []string{testAccountID, "rst:role::ManagementApiAccess", "another one"}},
		{"account not visible", http.StatusNotFound, []string{testAccountID, "rst:role::ManagementApiAccess"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := testutil.NewAPIServer(t)
			server.On("DescribeAccount", testutil.Error(tt.status, "denied"))

			err := newClient(t, server).ValidateCredentials(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tt.wantText {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q is missing %q", err, want)
				}
			}
		})
	}
}

// A credential-validation failure must not swallow the underlying API error, or the request ID an
// operator needs for a support request is lost.
func TestValidateCredentialsPreservesTheUnderlyingAPIError(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeAccount", testutil.Response{
		Status: http.StatusForbidden,
		Body:   `{"code":403,"message":"denied"}`,
		Header: map[string]string{"X-Amzn-Requestid": "req-99"},
	})

	err := newClient(t, server).ValidateCredentials(context.Background())
	apiErr, ok := api.AsError(err)
	if !ok {
		t.Fatalf("the underlying API error was lost: %v", err)
	}
	if apiErr.RequestID != "req-99" {
		t.Errorf("RequestID = %q, want req-99", apiErr.RequestID)
	}
}

// An unrelated failure must pass through untouched rather than be misreported as a credential
// problem.
func TestValidateCredentialsPassesThroughAnUnrelatedFailure(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeAccount", testutil.Error(http.StatusBadGateway, "gateway error"))

	err := newClient(t, server).ValidateCredentials(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "rst:role::ManagementApiAccess") {
		t.Errorf("a gateway error was misreported as a credential problem: %v", err)
	}
}

func TestListRegions(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("ListRegions", testutil.JSON(`{"regions":[
	  {"regionName":"us-east-1","regionDisplayName":"US East (N. Virginia)"},
	  {"regionName":"eu-central-1"}
	]}`))

	got, err := newClient(t, server).ListRegions(context.Background())
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	want := []cloud.Region{
		{Name: "us-east-1", DisplayName: "US East (N. Virginia)"},
		// The display name is optional in the contract, so an absent one is empty, not an error.
		{Name: "eu-central-1", DisplayName: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d regions, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("region %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestGetAccountLimits(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("GetAccountLimits", testutil.JSON(`{
	  "freeEnvironments":{"usage":1,"limit":3},
	  "tierLimits":{"BUSINESS":{"usage":2,"limit":10}}
	}`))

	got, err := newClient(t, server).GetAccountLimits(context.Background())
	if err != nil {
		t.Fatalf("GetAccountLimits: %v", err)
	}
	if got.FreeEnvironments != (cloud.Limit{Usage: 1, Limit: 3}) {
		t.Errorf("FreeEnvironments = %+v", got.FreeEnvironments)
	}
	if got.TierLimits["BUSINESS"] != (cloud.Limit{Usage: 2, Limit: 10}) {
		t.Errorf("TierLimits = %+v", got.TierLimits)
	}
}

// Only freeEnvironments is required. An account with no per-tier limits must decode cleanly and
// report nil rather than an empty map that suggests limits were fetched and found empty.
func TestGetAccountLimitsToleratesAbsentTierLimits(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("GetAccountLimits", testutil.JSON(`{"freeEnvironments":{"usage":0,"limit":1}}`))

	got, err := newClient(t, server).GetAccountLimits(context.Background())
	if err != nil {
		t.Fatalf("GetAccountLimits: %v", err)
	}
	if got.TierLimits != nil {
		t.Errorf("TierLimits = %+v, want nil", got.TierLimits)
	}
}

// The Cloud API repeats the HTTP status in its error body's `code` field. Echoing it back alongside
// "HTTP 403" would be noise, but a distinct code must be preserved.
func TestErrorCodeIsOmittedWhenItOnlyRepeatsTheStatus(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeAccount", testutil.Response{
		Status: http.StatusForbidden,
		Body:   `{"code":403,"message":"denied"}`,
	})

	_, err := newClient(t, server).DescribeAccount(context.Background())
	apiErr, ok := api.AsError(err)
	if !ok {
		t.Fatalf("not an API error: %v", err)
	}
	if apiErr.Code != "" {
		t.Errorf("Code = %q, want it omitted when it repeats the status", apiErr.Code)
	}
	if apiErr.Message != "denied" {
		t.Errorf("Message = %q", apiErr.Message)
	}
}

func TestErrorCodeIsPreservedWhenItDiffersFromTheStatus(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeAccount", testutil.Response{
		Status: http.StatusForbidden,
		Body:   `{"code":4031,"message":"key revoked"}`,
	})

	_, err := newClient(t, server).DescribeAccount(context.Background())
	apiErr, ok := api.AsError(err)
	if !ok {
		t.Fatalf("not an API error: %v", err)
	}
	if apiErr.Code != "4031" {
		t.Errorf("Code = %q, want 4031", apiErr.Code)
	}
}
