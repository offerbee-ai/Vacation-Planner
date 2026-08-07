package applemaps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// testClient wires a Client to a handler, with the token endpoint already
// answered so tests can focus on the endpoint under test. Backoff is stubbed out
// so retry tests do not spend real time.
func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			fmt.Fprint(w, `{"accessToken":"test-access-token","expiresInSeconds":1800}`)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	client, err := New(Options{
		TeamID:     "TEAM123456",
		KeyID:      "KEY7890123",
		PrivateKey: testKey(t),
		BaseURL:    srv.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.sleep = func(context.Context, time.Duration) error { return nil }
	return client, srv
}

func TestGetSetsBearerTokenFromTokenSource(t *testing.T) {
	var gotAuth string
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{}`)
	})

	var out struct{}
	if err := client.get(context.Background(), "/v1/anything", nil, &out); err != nil {
		t.Fatalf("get: %v", err)
	}
	if want := "Bearer test-access-token"; gotAuth != want {
		t.Errorf("Authorization: got %q, want %q", gotAuth, want)
	}
}

func TestGetRetriesOnceAfter401(t *testing.T) {
	var tokenCalls, endpointCalls atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			n := tokenCalls.Add(1)
			fmt.Fprintf(w, `{"accessToken":"token-%d","expiresInSeconds":1800}`, n)
			return
		}
		// First call rejects the token; the second accepts it. This is the
		// revoked-early case the retry exists for.
		if endpointCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Invalid token"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	client, err := New(Options{
		TeamID: "T", KeyID: "K", PrivateKey: testKey(t), BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.sleep = func(context.Context, time.Duration) error { return nil }

	var out struct{ OK bool }
	if err := client.get(context.Background(), "/v1/thing", nil, &out); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !out.OK {
		t.Error("expected the retry's body to be decoded")
	}
	if got := endpointCalls.Load(); got != 2 {
		t.Errorf("endpoint calls: got %d, want 2", got)
	}
	// The point of invalidating is that the retry uses a *freshly exchanged*
	// token, not the rejected one.
	if got := tokenCalls.Load(); got != 2 {
		t.Errorf("token exchanges: got %d, want 2 — the 401 should have forced a re-exchange", got)
	}
}

func TestGetReturnsAfterSecond401(t *testing.T) {
	var endpointCalls atomic.Int64
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		endpointCalls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"Invalid token"}`)
	})

	err := client.get(context.Background(), "/v1/thing", nil, nil)
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("got %T (%v), want *AuthError", err, err)
	}
	// Exactly two: the original and one retry. A loop here would hammer Apple
	// with a bad credential.
	if got := endpointCalls.Load(); got != 2 {
		t.Errorf("endpoint calls: got %d, want 2", got)
	}
}

func TestGetDoesNotRetryQuotaErrors(t *testing.T) {
	var calls atomic.Int64
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"message":"Quota exceeded"}`)
	})

	err := client.get(context.Background(), "/v1/thing", nil, nil)
	var quotaErr *QuotaError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("got %T (%v), want *QuotaError", err, err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls: got %d, want 1 — a daily quota cannot be waited out mid-request", got)
	}
}

func TestGetRetriesServerErrorsUpToMax(t *testing.T) {
	var calls atomic.Int64
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"boom"}`)
	})

	err := client.get(context.Background(), "/v1/thing", nil, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	// One initial attempt plus defaultMaxRetries.
	if want := int64(1 + defaultMaxRetries); calls.Load() != want {
		t.Errorf("calls: got %d, want %d", calls.Load(), want)
	}
}

func TestGetSucceedsOnRetryAfterServerError(t *testing.T) {
	var calls atomic.Int64
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})

	var out struct{ OK bool }
	if err := client.get(context.Background(), "/v1/thing", nil, &out); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !out.OK {
		t.Error("expected success on the second attempt")
	}
}

func TestGetDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int64
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"bad parameter"}`)
	})

	err := client.get(context.Background(), "/v1/thing", nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d", apiErr.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls: got %d, want 1 — a bad request will not fix itself", got)
	}
}

func TestMaxRetriesDisabled(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			fmt.Fprint(w, `{"accessToken":"t","expiresInSeconds":1800}`)
			return
		}
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := New(Options{
		TeamID: "T", KeyID: "K", PrivateKey: testKey(t),
		BaseURL: srv.URL, MaxRetries: -1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := client.get(context.Background(), "/v1/thing", nil, nil); err == nil {
		t.Fatal("want an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls: got %d, want 1 with retries disabled", got)
	}
}

func TestGetHonoursContextCancellationDuringBackoff(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	// Restore real backoff so cancellation has something to interrupt.
	client.sleep = sleepContext

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := client.get(ctx, "/v1/thing", nil, nil); err == nil {
		t.Fatal("want an error for a cancelled context")
	}
}

func TestGetDecodeFailureIsReported(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{not json`)
	})

	var out struct{}
	err := client.get(context.Background(), "/v1/thing", nil, &out)
	if err == nil {
		t.Fatal("want a decode error")
	}
}

func TestGetEncodesQueryParams(t *testing.T) {
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{}`)
	})

	params := url.Values{}
	params.Set("q", "eiffel tower")
	setCategories(params, "includePoiCategories", []PoiCategory{PoiCategoryRestaurant, PoiCategoryCafe})
	setStrings(params, "limitToCountries", []string{"US", "CA"})
	params.Set("searchLocation", formatLocation(37.78, -122.42))

	var out struct{}
	if err := client.get(context.Background(), "/v1/search", params, &out); err != nil {
		t.Fatalf("get: %v", err)
	}

	// Spaces must survive as a value, not be split into extra params.
	if got := gotQuery.Get("q"); got != "eiffel tower" {
		t.Errorf("q: got %q", got)
	}
	if got := gotQuery.Get("includePoiCategories"); got != "Restaurant,Cafe" {
		t.Errorf("includePoiCategories: got %q, want Restaurant,Cafe", got)
	}
	if got := gotQuery.Get("limitToCountries"); got != "US,CA" {
		t.Errorf("limitToCountries: got %q, want US,CA", got)
	}
	if got := gotQuery.Get("searchLocation"); got != "37.78,-122.42" {
		t.Errorf("searchLocation: got %q, want 37.78,-122.42", got)
	}
}

func TestFormatCoord(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{37.78, "37.78"},
		{-122.42, "-122.42"},
		{0, "0"},
		{48.85827172505176, "48.85827172505176"},
		// Must not become "1E-07": Apple parses decimal, not scientific notation.
		{0.0000001, "0.0000001"},
		{-0.5, "-0.5"},
	}
	for _, tc := range tests {
		if got := formatCoord(tc.in); got != tc.want {
			t.Errorf("formatCoord(%v): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Apple specifies searchRegion as north,east,south,west. MapRegion's own fields
// are documented as south-west / north-east corners, so the orderings differ and
// a mix-up would silently search the wrong box.
func TestFormatRegionUsesNorthEastSouthWestOrder(t *testing.T) {
	got := formatRegion(MapRegion{
		NorthLatitude: 38,
		EastLongitude: -122.1,
		SouthLatitude: 37.5,
		WestLongitude: -122.5,
	})
	if want := "38,-122.1,37.5,-122.5"; got != want {
		t.Errorf("formatRegion: got %q, want %q", got, want)
	}
}

func TestSetCategoriesAndStringsOmitEmpty(t *testing.T) {
	params := url.Values{}
	setCategories(params, "includePoiCategories", nil)
	setCategories(params, "excludePoiCategories", []PoiCategory{})
	setStrings(params, "limitToCountries", nil)

	if len(params) != 0 {
		t.Errorf("empty lists must not add parameters, got %v", params)
	}
}

func TestApplyLangPrefersRequestOverClientDefault(t *testing.T) {
	client := &Client{lang: "en-US"}

	t.Run("request value wins", func(t *testing.T) {
		params := url.Values{}
		client.applyLang(params, "fr-FR")
		if got := params.Get("lang"); got != "fr-FR" {
			t.Errorf("lang: got %q, want fr-FR", got)
		}
	})

	t.Run("falls back to client default", func(t *testing.T) {
		params := url.Values{}
		client.applyLang(params, "")
		if got := params.Get("lang"); got != "en-US" {
			t.Errorf("lang: got %q, want en-US", got)
		}
	})

	t.Run("omitted when neither is set", func(t *testing.T) {
		params := url.Values{}
		(&Client{}).applyLang(params, "")
		if _, ok := params["lang"]; ok {
			t.Error("lang must be absent so Apple applies its own default")
		}
	})
}

func TestNewValidatesCredentials(t *testing.T) {
	if _, err := New(Options{KeyID: "K", PrivateKey: testKey(t)}); err == nil {
		t.Error("want an error when TeamID is missing")
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	client, err := New(Options{TeamID: "T", KeyID: "K", PrivateKey: testKey(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.baseURL != DefaultBaseURL {
		t.Errorf("baseURL: got %q, want %q", client.baseURL, DefaultBaseURL)
	}
	if client.maxRetries != defaultMaxRetries {
		t.Errorf("maxRetries: got %d, want %d", client.maxRetries, defaultMaxRetries)
	}
	if client.httpClient == nil {
		t.Error("httpClient should default to non-nil")
	}
}
