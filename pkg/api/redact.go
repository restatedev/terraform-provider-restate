package api

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Redacted is the placeholder substituted for a secret. It is deliberately not a run of asterisks, so
// a reader who finds it in a log can search for it and land here.
const Redacted = "[REDACTED]"

// bearerPattern matches a bearer credential wherever it might be echoed into a message: an
// Authorization header value, and Restate's own key prefixes.
//
// The prefixes are shared by key IDs and key secrets, so a long key ID quoted by the *server* is
// redacted too. That is the safe direction to err in; the provider's own diagnostics name key IDs
// directly and do not pass through here.
var bearerPattern = regexp.MustCompile(`(?i)\b(bearer\s+|key_|rst_|rrk_)[A-Za-z0-9._~+/=-]{8,}`)

// RedactString removes credentials from text about to be logged or put in a diagnostic.
//
// This is defence in depth, not the primary control: the provider does not put credentials into
// messages, and errors carry no response body or request headers. It exists because a third-party
// transport error can quote a URL, and a server can echo a submitted value back in its message.
func RedactString(s string) string {
	if s == "" {
		return s
	}
	s = bearerPattern.ReplaceAllString(s, "$1"+Redacted)
	return redactURLUserinfo(s)
}

// urlWithUserinfo matches a URL carrying credentials in its authority, which net/url and net/http
// both include verbatim in error strings.
var urlWithUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/\s@]+)@`)

func redactURLUserinfo(s string) string {
	return urlWithUserinfo.ReplaceAllString(s, "${1}"+Redacted+"@")
}

// redactError rewrites an error so it can be wrapped into a diagnostic. Go's HTTP stack puts the
// request URL into transport errors, and a *url.Error also exposes it as a field.
func redactError(err error) error {
	if err == nil {
		return nil
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// Rebuild rather than mutate: the caller may still hold the original.
		return &url.Error{
			Op:  urlErr.Op,
			URL: RedactString(urlErr.URL),
			Err: redactError(urlErr.Err),
		}
	}

	message := err.Error()
	if redacted := RedactString(message); redacted != message {
		return errors.New(redacted)
	}
	return err
}

// SensitivePropertyNames are the map keys, from either contract, whose values the provider must not
// trust an API to have disclosed truthfully -- Kafka connection properties and deployment headers.
//
// Matching is case-insensitive and substring-based on purpose: librdkafka has many variants
// (`sasl.password`, `ssl.key.password`, `ssl.keystore.password`), and a new one in a refreshed
// contract should be caught by default rather than leak until someone notices. The cost is the
// occasional false positive, such as an OAuth *endpoint* matching "token".
var SensitivePropertyNames = []string{
	"password",
	"secret",
	"token",
	"authorization",
	"credential",
	"private",
	"apikey",
	"api-key",
	"api_key",
	"sasl.username",
	"ssl.key.pem",
	"ssl.certificate.pem",
}

// IsSensitivePropertyName reports whether a map key's value should be redacted from output.
func IsSensitivePropertyName(name string) bool {
	lower := strings.ToLower(name)
	for _, needle := range SensitivePropertyNames {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
