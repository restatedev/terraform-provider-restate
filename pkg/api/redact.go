package api

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Redacted is the placeholder substituted for a secret. It is deliberately not a run of asterisks:
// a reader who finds it in a log should be able to search for it and land here.
const Redacted = "[REDACTED]"

// bearerPattern matches a bearer credential wherever it might be echoed into a message: an
// Authorization header value, and Restate's own key prefixes.
var bearerPattern = regexp.MustCompile(`(?i)\b(bearer\s+|key_|rst_)[A-Za-z0-9._~+/=-]{8,}`)

// RedactString removes credentials from text that is about to be logged or put in a diagnostic.
//
// This is a defence in depth, not the primary control: the provider does not put credentials into
// messages in the first place, and errors deliberately carry no response body or request headers.
// It exists because a third-party transport error can quote a URL, and because a server can echo a
// submitted value back in its message.
func RedactString(s string) string {
	if s == "" {
		return s
	}
	s = bearerPattern.ReplaceAllString(s, "$1"+Redacted)
	return redactURLUserinfo(s)
}

// urlWithUserinfo matches a URL carrying credentials in its authority, which Go's net/url and
// net/http both include verbatim in error strings.
var urlWithUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/\s@]+)@`)

func redactURLUserinfo(s string) string {
	return urlWithUserinfo.ReplaceAllString(s, "${1}"+Redacted+"@")
}

// redactError rewrites an error's message so it can be wrapped into a diagnostic. Go's HTTP stack
// puts the request URL into transport errors, and a *url.Error additionally exposes it as a field.
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

// SensitivePropertyNames are the map keys, from either contract, whose values must never reach a
// log or diagnostic. Kafka connection properties are librdkafka names; the header names cover the
// deployment `additional_headers` map.
//
// Matching is case-insensitive and substring-based on purpose: librdkafka has many variants
// (`sasl.password`, `ssl.key.password`, `ssl.keystore.password`) and a new one appearing in a
// refreshed contract should be caught by default rather than leak until someone notices.
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

// RedactMap returns a copy of properties with sensitive values replaced. Keys are preserved: which
// properties are configured is useful diagnostic information, and their names are not secret.
func RedactMap(properties map[string]string) map[string]string {
	if properties == nil {
		return nil
	}
	redacted := make(map[string]string, len(properties))
	for name, value := range properties {
		if IsSensitivePropertyName(name) && value != "" {
			redacted[name] = Redacted
			continue
		}
		redacted[name] = value
	}
	return redacted
}
