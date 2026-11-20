// Package api holds the transport, error, and generic request machinery shared by the Restate Cloud
// Management and Admin API facades.
//
// This package and its subpackages are the only place in the provider that knows either wire
// contract, and they are deliberately independent of Terraform: they return typed domain values and
// typed errors, never diagnostics or framework values.
//
// Cloud and Admin authentication are separate by construction. Each facade builds its own client from
// its own credential, so neither key can reach the other API.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Service names the API an error came from, so a diagnostic makes the credential and endpoint at
// fault unambiguous.
type Service string

const (
	ServiceCloud Service = "Restate Cloud Management API"
	ServiceAdmin Service = "Restate Admin API"
)

// Error is a structured API error. Every non-2xx response and every unrecoverable transport failure
// is reported as one of these, so callers can branch on status without string matching.
//
// It deliberately carries no response body and no request headers: either can contain a bearer token,
// a Kafka credential, or a user payload. Only the server's own message field is retained.
type Error struct {
	// Service identifies which API produced the error.
	Service Service
	// Operation is the contract operation name, for example "DescribeEnvironment".
	Operation string
	// StatusCode is the HTTP status, or 0 when the request never produced a response.
	StatusCode int
	// Code is the error code the API's own error body reported, when it reports one.
	Code string
	// Message is the API's human-readable message, or a description of the transport failure.
	Message string
	// RequestID is the server's correlation identifier, when it sends one.
	RequestID string
	// Retryable records whether the provider considered this failure worth retrying. A retryable
	// error that still surfaces means the retry budget was exhausted.
	Retryable bool

	// Err is the underlying transport or decoding failure, if any.
	Err error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Service))
	if e.Operation != "" {
		fmt.Fprintf(&b, " %s", e.Operation)
	}
	b.WriteString(": ")

	switch {
	case e.StatusCode == 0:
		b.WriteString("request failed")
	default:
		fmt.Fprintf(&b, "HTTP %d %s", e.StatusCode, http.StatusText(e.StatusCode))
	}

	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.Code != "" {
		fmt.Fprintf(&b, " (code %s)", e.Code)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " [request %s]", e.RequestID)
	}
	if e.StatusCode == 0 && e.Err != nil {
		fmt.Fprintf(&b, ": %s", e.Err)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// AsError extracts a structured API error from err, if there is one.
func AsError(err error) (*Error, bool) {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// IsNotFound reports whether err is a 404. A 404 during refresh removes a resource from state and a
// 404 during delete is success, so this is where that decision is made once.
func IsNotFound(err error) bool { return hasStatus(err, http.StatusNotFound) }

// IsUnauthorized reports whether err is a 401: the credential is missing, malformed, or expired.
func IsUnauthorized(err error) bool { return hasStatus(err, http.StatusUnauthorized) }

// IsForbidden reports whether err is a 403: the credential is valid but not permitted. On the Cloud
// Management API that usually means the key is not account-scoped with
// `rst:role::ManagementApiAccess`, or belongs to a different account.
func IsForbidden(err error) bool { return hasStatus(err, http.StatusForbidden) }

// IsConflict reports whether err is a 409.
func IsConflict(err error) bool { return hasStatus(err, http.StatusConflict) }

// IsGone reports whether err is a 410: the object existed and has ended, for example an expired or
// deleted region grant.
func IsGone(err error) bool { return hasStatus(err, http.StatusGone) }

// IsBadRequest reports whether err is a 400.
func IsBadRequest(err error) bool { return hasStatus(err, http.StatusBadRequest) }

// IsTooManyRequests reports whether err is a 429: the account is at a limit the operation counts
// against, or the API is throttling it.
func IsTooManyRequests(err error) bool { return hasStatus(err, http.StatusTooManyRequests) }

// IsServiceUnavailable reports whether err is a 503.
func IsServiceUnavailable(err error) bool { return hasStatus(err, http.StatusServiceUnavailable) }

func hasStatus(err error, status int) bool {
	apiErr, ok := AsError(err)
	return ok && apiErr.StatusCode == status
}
