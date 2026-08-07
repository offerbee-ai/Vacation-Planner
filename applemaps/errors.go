package applemaps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// maxErrorBodyBytes bounds how much of an error body is read. Apple's error
// bodies are small; a cap keeps a misbehaving proxy from streaming an unbounded
// response into memory.
const maxErrorBodyBytes = 64 << 10

// APIError is a non-2xx response from the Apple Maps Server API.
type APIError struct {
	StatusCode int
	Message    string
	Details    []string
}

func (e *APIError) Error() string {
	if len(e.Details) == 0 {
		return fmt.Sprintf("applemaps: HTTP %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("applemaps: HTTP %d: %s (%s)", e.StatusCode, e.Message, strings.Join(e.Details, "; "))
}

// AuthError is a 401. It means the access token is missing, expired, or invalid.
// The client retries once on its own after re-exchanging; an AuthError reaching
// a caller means the second attempt failed too, so the credentials themselves
// are suspect.
type AuthError struct{ *APIError }

func (e *AuthError) Unwrap() error { return e.APIError }

// QuotaError is a 429: the daily service call quota for this developer team is
// exhausted. Apple applies the quota per team across both the Server API and
// MapKit JS, and returns 429 from every endpoint including /v1/token once it is
// hit.
//
// Callers routing between providers should test for this specifically rather
// than treating it as a generic failure — it is not retryable within the same
// UTC day, so backing off and retrying will not help.
type QuotaError struct{ *APIError }

func (e *QuotaError) Unwrap() error { return e.APIError }

// NotFoundError reports that a request succeeded but matched nothing. Apple
// returns HTTP 200 with an empty results array for a geocode that resolves to
// no place, which is a different condition from a transport or auth failure and
// would otherwise surface as an indistinguishable zero value.
type NotFoundError struct {
	// Query is the input that matched nothing, for use in the error message.
	Query string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("applemaps: no results for %q", e.Query)
}

// newAPIError converts a non-2xx response into the most specific error type
// available. A body that is not valid JSON still produces a usable message
// rather than an empty one.
func newAPIError(statusCode int, body []byte) error {
	var parsed ErrorResponse
	// A decode failure is not itself an error worth reporting: it only means the
	// body was not Apple's documented error shape, and the raw body is used
	// instead.
	_ = json.Unmarshal(body, &parsed)

	err := &APIError{
		StatusCode: statusCode,
		Message:    parsed.Message,
		Details:    parsed.Details,
	}
	if err.Message == "" {
		if raw := strings.TrimSpace(string(body)); raw != "" {
			err.Message = raw
		} else {
			err.Message = http.StatusText(statusCode)
		}
	}

	switch statusCode {
	case http.StatusUnauthorized:
		return &AuthError{APIError: err}
	case http.StatusTooManyRequests:
		return &QuotaError{APIError: err}
	default:
		return err
	}
}

// retryable reports whether a failed request is worth sending again.
//
// 429 is deliberately excluded: the quota resets daily, so retrying inside one
// request's lifetime cannot succeed and only burns further calls against a quota
// that is already exhausted.
func retryable(statusCode int) bool {
	return statusCode >= http.StatusInternalServerError
}
