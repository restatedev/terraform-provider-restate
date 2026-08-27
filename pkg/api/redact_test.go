package api

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestRedactStringRemovesBearerCredentials(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		secret string
	}{
		{"authorization header value", "unexpected header Authorization: Bearer key_live_abcdef1234567890", "key_live_abcdef1234567890"},
		{"bare restate key prefix", "invalid credential key_live_abcdef1234567890 supplied", "key_live_abcdef1234567890"},
		{"legacy token prefix", "token rst_abcdef1234567890 is expired", "rst_abcdef1234567890"},
		{"lowercase bearer", "got bearer key_live_abcdef1234567890", "key_live_abcdef1234567890"},
		{"url userinfo", `Post "https://user:sup3rsecret@api.us.restate.cloud/cloud": EOF`, "sup3rsecret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactString(tt.in)
			if strings.Contains(got, tt.secret) {
				t.Errorf("RedactString(%q) = %q, which still contains the secret", tt.in, got)
			}
			if !strings.Contains(got, Redacted) {
				t.Errorf("RedactString(%q) = %q, want the %s placeholder", tt.in, got, Redacted)
			}
		})
	}
}

// Redaction must not mangle ordinary diagnostic text, or operators will stop trusting the output.
func TestRedactStringLeavesInnocuousTextAlone(t *testing.T) {
	for _, s := range []string{
		"",
		"environment env_1a2b3c is PENDING",
		"deployment dp_1a2b3c not found",
		`Post "https://api.us.restate.cloud/cloud/acc_1/DescribeEnvironment": EOF`,
		"tier BUSINESS is not available in region us-east-1",
	} {
		if got := RedactString(s); got != s {
			t.Errorf("RedactString(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestRedactErrorRewritesAURLError(t *testing.T) {
	original := &url.Error{
		Op:  "Post",
		URL: "https://user:sup3rsecret@api.us.restate.cloud/cloud",
		Err: errors.New("dial tcp: i/o timeout"),
	}

	redacted := redactError(original)
	if strings.Contains(redacted.Error(), "sup3rsecret") {
		t.Errorf("redactError left the credential in place: %v", redacted)
	}

	// The original must be untouched: the caller may still hold and inspect it.
	if !strings.Contains(original.URL, "sup3rsecret") {
		t.Error("redactError mutated the error it was given")
	}

	// The cause must remain reachable, so callers can still classify the failure.
	var asURLError *url.Error
	if !errors.As(redacted, &asURLError) {
		t.Fatal("the redacted error is no longer a *url.Error")
	}
	if asURLError.Op != "Post" {
		t.Errorf("Op = %q, want Post", asURLError.Op)
	}
}

func TestRedactErrorPreservesAnErrorWithNothingToRedact(t *testing.T) {
	original := errors.New("connection refused")
	if got := redactError(original); got != original {
		t.Errorf("redactError returned a new error for clean input: %v", got)
	}
	if redactError(nil) != nil {
		t.Error("redactError(nil) is not nil")
	}
}

func TestRedactErrorRewritesAWrappedMessage(t *testing.T) {
	err := fmt.Errorf("sending request: %w", errors.New("Bearer key_live_abcdef1234567890 rejected"))
	if got := redactError(err); strings.Contains(got.Error(), "key_live_abcdef1234567890") {
		t.Errorf("redactError left the credential in place: %v", got)
	}
}

func TestIsSensitivePropertyName(t *testing.T) {
	sensitive := []string{
		"sasl.password",
		"SASL.PASSWORD",
		"ssl.key.password",
		"ssl.keystore.password",
		"sasl.oauthbearer.client.secret",
		"Authorization",
		"x-api-key",
		"X-Api-Token",
		"ssl.key.pem",
		// A librdkafka property that does not exist today but matches the substring policy: a
		// refreshed contract introducing it must be redacted by default, not leak until noticed.
		"sasl.future.password.v2",
	}
	for _, name := range sensitive {
		if !IsSensitivePropertyName(name) {
			t.Errorf("IsSensitivePropertyName(%q) = false, want true", name)
		}
	}

	safe := []string{
		"bootstrap.servers",
		"metadata.broker.list",
		"security.protocol",
		"sasl.mechanism",
		"group.id",
		"Content-Type",
	}
	for _, name := range safe {
		if IsSensitivePropertyName(name) {
			t.Errorf("IsSensitivePropertyName(%q) = true, want false", name)
		}
	}
}
