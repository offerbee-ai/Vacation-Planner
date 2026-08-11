package applemaps

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// The nested shape is what the live API actually sends, despite Apple's published
// ErrorResponse documenting a flat one. Decoding only the documented form yielded
// empty error messages against the real service.
func TestErrorResponseDecodesNestedWireFormat(t *testing.T) {
	const body = `{"error":{"message":"transportType invalid","details":["a","b"]}}`

	var got ErrorResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Message != "transportType invalid" {
		t.Errorf("message: got %q, want %q", got.Message, "transportType invalid")
	}
	if len(got.Details) != 2 {
		t.Errorf("details: got %v, want 2 entries", got.Details)
	}
}

func TestErrorResponseDecodesFlatDocumentedFormat(t *testing.T) {
	const body = `{"message":"Quota exceeded","details":["daily limit"]}`

	var got ErrorResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Message != "Quota exceeded" {
		t.Errorf("message: got %q", got.Message)
	}
	if len(got.Details) != 1 || got.Details[0] != "daily limit" {
		t.Errorf("details: got %v", got.Details)
	}
}

func TestErrorResponseNestedWinsOverFlat(t *testing.T) {
	const body = `{"message":"outer","error":{"message":"inner"}}`

	var got ErrorResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Message != "inner" {
		t.Errorf("message: got %q, want the nested value", got.Message)
	}
}

func TestErrorResponseEmptyDetailsArray(t *testing.T) {
	// Exactly what the live API returned for an invalid transportType.
	const body = `{"error":{"message":"transportType invalid","details":[]}}`

	var got ErrorResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Message != "transportType invalid" {
		t.Errorf("message: got %q", got.Message)
	}
	if len(got.Details) != 0 {
		t.Errorf("details: got %v, want empty", got.Details)
	}
}

// An APIError's message is what an operator reads in a log line, so it has to
// carry the real reason regardless of which shape Apple used.
func TestNewAPIErrorExtractsNestedMessage(t *testing.T) {
	err := newAPIError(http.StatusBadRequest, []byte(`{"error":{"message":"transportType invalid","details":[]}}`))

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %T, want *APIError", err)
	}
	if apiErr.Message != "transportType invalid" {
		t.Errorf("message: got %q, want the nested message rather than the raw body", apiErr.Message)
	}
	if !strings.Contains(err.Error(), "transportType invalid") {
		t.Errorf("Error(): got %q", err.Error())
	}
}

func TestNewAPIErrorNestedQuotaAndAuth(t *testing.T) {
	t.Run("nested 429 still classifies as quota", func(t *testing.T) {
		err := newAPIError(http.StatusTooManyRequests, []byte(`{"error":{"message":"Quota exceeded"}}`))
		var quotaErr *QuotaError
		if !errors.As(err, &quotaErr) {
			t.Fatalf("got %T, want *QuotaError", err)
		}
		if quotaErr.Message != "Quota exceeded" {
			t.Errorf("message: got %q", quotaErr.Message)
		}
	})

	t.Run("nested 401 still classifies as auth", func(t *testing.T) {
		err := newAPIError(http.StatusUnauthorized, []byte(`{"error":{"message":"Invalid token"}}`))
		var authErr *AuthError
		if !errors.As(err, &authErr) {
			t.Fatalf("got %T, want *AuthError", err)
		}
		if authErr.Message != "Invalid token" {
			t.Errorf("message: got %q", authErr.Message)
		}
	})
}

func TestAPIErrorMessageFormatting(t *testing.T) {
	withoutDetails := &APIError{StatusCode: 500, Message: "boom"}
	if got, want := withoutDetails.Error(), "applemaps: HTTP 500: boom"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	withDetails := &APIError{StatusCode: 400, Message: "bad", Details: []string{"x", "y"}}
	if got, want := withDetails.Error(), "applemaps: HTTP 400: bad (x; y)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNotFoundErrorMessage(t *testing.T) {
	err := &NotFoundError{Query: "37.5,-122.5"}
	if got, want := err.Error(), `applemaps: no results for "37.5,-122.5"`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRetryable(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusOK, false},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		// A daily quota cannot be waited out inside one request; retrying only
		// spends more of an exhausted budget.
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
	}
	for _, tc := range tests {
		if got := retryable(tc.status); got != tc.want {
			t.Errorf("retryable(%d): got %v, want %v", tc.status, got, tc.want)
		}
	}
}
