package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestErrorStringIncludesTheContextAnOperatorNeeds(t *testing.T) {
	err := &Error{
		Service:    ServiceCloud,
		Operation:  "DescribeEnvironment",
		StatusCode: http.StatusForbidden,
		Code:       "403",
		Message:    "principal is not authorized",
		RequestID:  "req-abc123",
		Attempts:   2,
	}

	got := err.Error()
	for _, want := range []string{
		"Restate Cloud Management API",
		"DescribeEnvironment",
		"HTTP 403",
		"principal is not authorized",
		"code 403",
		"request req-abc123",
		"2 attempts",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("error string %q is missing %q", got, want)
		}
	}
}

func TestErrorStringForATransportFailure(t *testing.T) {
	err := &Error{
		Service:   ServiceAdmin,
		Operation: "GetDeployment",
		Message:   "could not reach the API",
		Err:       errors.New("dial tcp: connection refused"),
	}

	got := err.Error()
	if strings.Contains(got, "HTTP 0") {
		t.Errorf("error string reports a bogus status: %q", got)
	}
	for _, want := range []string{"request failed", "could not reach the API", "connection refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("error string %q is missing %q", got, want)
		}
	}
}

func TestErrorUnwrapsToTheUnderlyingCause(t *testing.T) {
	cause := errors.New("boom")
	err := &Error{Service: ServiceAdmin, Err: cause}
	if !errors.Is(err, cause) {
		t.Error("errors.Is could not find the wrapped cause")
	}
}

func TestStatusClassifiers(t *testing.T) {
	tests := []struct {
		status int
		check  func(error) bool
		name   string
		want   bool
	}{
		{http.StatusNotFound, IsNotFound, "IsNotFound/404", true},
		{http.StatusForbidden, IsNotFound, "IsNotFound/403", false},
		{http.StatusUnauthorized, IsUnauthorized, "IsUnauthorized/401", true},
		{http.StatusForbidden, IsUnauthorized, "IsUnauthorized/403", false},
		{http.StatusForbidden, IsForbidden, "IsForbidden/403", true},
		{http.StatusUnauthorized, IsForbidden, "IsForbidden/401", false},
		{http.StatusConflict, IsConflict, "IsConflict/409", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &Error{StatusCode: tt.status}
			if got := tt.check(err); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// The classifiers are how "404 during refresh removes the resource from state" is decided, so they
// have to see through the wrapping a caller adds on the way up.
func TestStatusClassifiersSeeThroughWrapping(t *testing.T) {
	err := fmt.Errorf("describing environment env_1: %w", &Error{StatusCode: http.StatusNotFound})
	if !IsNotFound(err) {
		t.Error("IsNotFound did not see through fmt.Errorf wrapping")
	}
}

func TestClassifiersRejectAnUnrelatedError(t *testing.T) {
	err := errors.New("not an API error")
	for name, check := range map[string]func(error) bool{
		"IsNotFound": IsNotFound, "IsUnauthorized": IsUnauthorized,
		"IsForbidden": IsForbidden, "IsConflict": IsConflict,
	} {
		if check(err) {
			t.Errorf("%s matched a non-API error", name)
		}
	}
	if _, ok := AsError(err); ok {
		t.Error("AsError extracted an API error from an unrelated error")
	}
	if _, ok := AsError(nil); ok {
		t.Error("AsError extracted an API error from nil")
	}
}
