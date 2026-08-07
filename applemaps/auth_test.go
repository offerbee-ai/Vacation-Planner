package applemaps

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Every test in this file generates its own P-256 key. The real .p8 must never
// appear in a fixture: Apple allows exactly one download of it and there is no
// way to reissue the same key.
func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func testKeyPEM(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	// PKCS#8 is the encoding Apple's .p8 files use.
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// tokenServer returns a server that answers /v1/token with the given token and
// TTL, plus a counter of how many exchanges it served.
func tokenServer(t *testing.T, accessToken string, expiresIn int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != tokenPath {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"accessToken":%q,"expiresInSeconds":%d}`, accessToken, expiresIn)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newTestTokenSource(t *testing.T, baseURL string, key *ecdsa.PrivateKey) *TokenSource {
	t.Helper()
	ts, err := NewTokenSource(TokenSourceConfig{
		TeamID:     "TEAM123456",
		KeyID:      "KEY7890123",
		PrivateKey: key,
		BaseURL:    baseURL,
	})
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	return ts
}

func TestAuthJWTStructure(t *testing.T) {
	key := testKey(t)
	var captured string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"accessToken":"at","expiresInSeconds":1800}`)
	}))
	defer srv.Close()

	ts := newTestTokenSource(t, srv.URL, key)
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}

	const prefix = "Bearer "
	if len(captured) <= len(prefix) || captured[:len(prefix)] != prefix {
		t.Fatalf("Authorization header: got %q, want %q prefix", captured, prefix)
	}
	raw := captured[len(prefix):]

	// The signature must verify against the public half of the signing key,
	// which is what proves we signed with ES256 over the right bytes.
	parsed, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return &key.PublicKey, nil })
	if err != nil {
		t.Fatalf("parse auth JWT: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("auth JWT did not verify")
	}

	if got := parsed.Method.Alg(); got != "ES256" {
		t.Errorf("alg: got %q, want ES256", got)
	}
	if got := parsed.Header["kid"]; got != "KEY7890123" {
		t.Errorf("kid header: got %v, want KEY7890123", got)
	}
	if got := parsed.Header["typ"]; got != "JWT" {
		t.Errorf("typ header: got %v, want JWT", got)
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims: got %T", parsed.Claims)
	}
	if got := claims["iss"]; got != "TEAM123456" {
		t.Errorf("iss claim: got %v, want TEAM123456", got)
	}
	iat, ok := claims["iat"].(float64)
	if !ok {
		t.Fatal("iat claim missing")
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		t.Fatal("exp claim missing")
	}
	if wantTTL := authJWTTTL.Seconds(); exp-iat != wantTTL {
		t.Errorf("exp-iat: got %v seconds, want %v", exp-iat, wantTTL)
	}
}

func TestTokenExchangeStoresTokenAndExpiry(t *testing.T) {
	srv, calls := tokenServer(t, "access-token-1", 1800)
	ts := newTestTokenSource(t, srv.URL, testKey(t))

	start := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	ts.now = func() time.Time { return start }

	got, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "access-token-1" {
		t.Errorf("token: got %q, want access-token-1", got)
	}
	if calls.Load() != 1 {
		t.Errorf("exchanges: got %d, want 1", calls.Load())
	}
	if want := start.Add(1800 * time.Second); !ts.expiry.Equal(want) {
		t.Errorf("expiry: got %v, want %v", ts.expiry, want)
	}
}

func TestTokenIsCachedInsideValidityWindow(t *testing.T) {
	srv, calls := tokenServer(t, "cached", 1800)
	ts := newTestTokenSource(t, srv.URL, testKey(t))

	start := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	current := start
	ts.now = func() time.Time { return current }

	for range 5 {
		if _, err := ts.Token(context.Background()); err != nil {
			t.Fatalf("Token: %v", err)
		}
		// Advance well inside the window: 1800s TTL less a 300s margin leaves
		// 25 minutes of reuse.
		current = current.Add(2 * time.Minute)
	}

	if calls.Load() != 1 {
		t.Errorf("exchanges: got %d, want 1 — token should have been reused", calls.Load())
	}
}

func TestTokenRefreshesInsideMargin(t *testing.T) {
	srv, calls := tokenServer(t, "refreshed", 1800)
	ts := newTestTokenSource(t, srv.URL, testKey(t))

	start := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	current := start
	ts.now = func() time.Time { return current }

	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("first Token: %v", err)
	}

	// Move to 4 minutes before expiry: inside the 5 minute margin, so the token
	// counts as stale even though Apple would still accept it.
	current = start.Add(1800*time.Second - 4*time.Minute)
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("second Token: %v", err)
	}

	if calls.Load() != 2 {
		t.Errorf("exchanges: got %d, want 2", calls.Load())
	}
}

// A cold TokenSource hit by many goroutines must spend one quota call, not one
// per goroutine.
func TestConcurrentTokenCallsExchangeOnce(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		// Hold the response briefly so callers genuinely overlap; without this
		// the first exchange could complete before the others even start, and
		// the test would pass without exercising the lock.
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, `{"accessToken":"shared","expiresInSeconds":1800}`)
	}))
	defer srv.Close()

	ts := newTestTokenSource(t, srv.URL, testKey(t))

	const goroutines = 50
	var wg sync.WaitGroup
	tokens := make([]string, goroutines)
	errs := make([]error, goroutines)

	wg.Add(goroutines)
	for i := range goroutines {
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = ts.Token(context.Background())
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if tokens[i] != "shared" {
			t.Errorf("goroutine %d token: got %q, want shared", i, tokens[i])
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("exchanges: got %d, want 1", got)
	}
}

func TestTokenExchangeErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		check      func(*testing.T, error)
	}{
		{
			name:       "401 is an auth error",
			statusCode: http.StatusUnauthorized,
			body:       `{"message":"Invalid token"}`,
			check: func(t *testing.T, err error) {
				var authErr *AuthError
				if !errors.As(err, &authErr) {
					t.Fatalf("got %T (%v), want *AuthError", err, err)
				}
				if authErr.Message != "Invalid token" {
					t.Errorf("message: got %q", authErr.Message)
				}
				// Unwrapping to *APIError keeps status-code checks working for
				// callers that do not care which specific kind it is.
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Error("AuthError should unwrap to *APIError")
				}
			},
		},
		{
			name:       "429 is a quota error",
			statusCode: http.StatusTooManyRequests,
			body:       `{"message":"Quota exceeded","details":["daily limit"]}`,
			check: func(t *testing.T, err error) {
				var quotaErr *QuotaError
				if !errors.As(err, &quotaErr) {
					t.Fatalf("got %T (%v), want *QuotaError", err, err)
				}
				if len(quotaErr.Details) != 1 || quotaErr.Details[0] != "daily limit" {
					t.Errorf("details: got %v", quotaErr.Details)
				}
			},
		},
		{
			name:       "500 is a plain API error",
			statusCode: http.StatusInternalServerError,
			body:       `{"message":"boom"}`,
			check: func(t *testing.T, err error) {
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("got %T, want *APIError", err)
				}
				var authErr *AuthError
				var quotaErr *QuotaError
				if errors.As(err, &authErr) || errors.As(err, &quotaErr) {
					t.Error("500 must not classify as auth or quota")
				}
			},
		},
		{
			name:       "non-JSON body still yields a message",
			statusCode: http.StatusBadGateway,
			body:       "<html>gateway down</html>",
			check: func(t *testing.T, err error) {
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("got %T, want *APIError", err)
				}
				if apiErr.Message != "<html>gateway down</html>" {
					t.Errorf("message: got %q, want the raw body", apiErr.Message)
				}
			},
		},
		{
			name:       "empty body falls back to the status text",
			statusCode: http.StatusServiceUnavailable,
			body:       "",
			check: func(t *testing.T, err error) {
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("got %T, want *APIError", err)
				}
				if apiErr.Message != http.StatusText(http.StatusServiceUnavailable) {
					t.Errorf("message: got %q", apiErr.Message)
				}
			},
		},
		{
			name:       "200 with no access token is an error",
			statusCode: http.StatusOK,
			body:       `{"expiresInSeconds":1800}`,
			check: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("want an error for a token response with no token")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			ts := newTestTokenSource(t, srv.URL, testKey(t))
			_, err := ts.Token(context.Background())
			if err == nil {
				t.Fatal("want an error")
			}
			tc.check(t, err)
		})
	}
}

func TestInvalidateForcesReExchange(t *testing.T) {
	srv, calls := tokenServer(t, "tok", 1800)
	ts := newTestTokenSource(t, srv.URL, testKey(t))

	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	ts.Invalidate()
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("Token after Invalidate: %v", err)
	}

	if calls.Load() != 2 {
		t.Errorf("exchanges: got %d, want 2", calls.Load())
	}
}

func TestTokenRespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		fmt.Fprint(w, `{"accessToken":"late","expiresInSeconds":1800}`)
	}))
	defer srv.Close()

	ts := newTestTokenSource(t, srv.URL, testKey(t))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := ts.Token(ctx); err == nil {
		t.Fatal("want an error when the context expires")
	}
}

func TestNewTokenSourceValidation(t *testing.T) {
	key := testKey(t)
	tests := []struct {
		name string
		cfg  TokenSourceConfig
	}{
		{"missing team ID", TokenSourceConfig{KeyID: "K", PrivateKey: key}},
		{"blank team ID", TokenSourceConfig{TeamID: "   ", KeyID: "K", PrivateKey: key}},
		{"missing key ID", TokenSourceConfig{TeamID: "T", PrivateKey: key}},
		{"missing private key", TokenSourceConfig{TeamID: "T", KeyID: "K"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewTokenSource(tc.cfg); err == nil {
				t.Error("want a validation error")
			}
		})
	}

	t.Run("defaults are applied", func(t *testing.T) {
		ts, err := NewTokenSource(TokenSourceConfig{TeamID: "T", KeyID: "K", PrivateKey: key})
		if err != nil {
			t.Fatalf("NewTokenSource: %v", err)
		}
		if ts.baseURL != DefaultBaseURL {
			t.Errorf("baseURL: got %q, want %q", ts.baseURL, DefaultBaseURL)
		}
		if ts.httpClient == nil {
			t.Error("httpClient should default to non-nil")
		}
	})

	t.Run("trailing slash is trimmed from base URL", func(t *testing.T) {
		ts, err := NewTokenSource(TokenSourceConfig{
			TeamID: "T", KeyID: "K", PrivateKey: key,
			BaseURL: "https://example.test/",
		})
		if err != nil {
			t.Fatalf("NewTokenSource: %v", err)
		}
		if ts.baseURL != "https://example.test" {
			t.Errorf("baseURL: got %q", ts.baseURL)
		}
	})
}

func TestParsePrivateKey(t *testing.T) {
	key := testKey(t)
	keyPEM := testKeyPEM(t, key)

	t.Run("raw PEM", func(t *testing.T) {
		got, err := ParsePrivateKey(keyPEM)
		if err != nil {
			t.Fatalf("ParsePrivateKey: %v", err)
		}
		if !got.Equal(key) {
			t.Error("parsed key differs from the original")
		}
	})

	t.Run("PEM with surrounding whitespace", func(t *testing.T) {
		if _, err := ParsePrivateKey("\n  " + keyPEM + "  \n"); err != nil {
			t.Fatalf("ParsePrivateKey: %v", err)
		}
	})

	t.Run("base64-encoded PEM", func(t *testing.T) {
		got, err := ParsePrivateKey(base64.StdEncoding.EncodeToString([]byte(keyPEM)))
		if err != nil {
			t.Fatalf("ParsePrivateKey: %v", err)
		}
		if !got.Equal(key) {
			t.Error("parsed key differs from the original")
		}
	})

	// This is the case that motivates accepting base64 at all: config systems
	// that wrap long values across lines.
	t.Run("base64 wrapped across lines", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString([]byte(keyPEM))
		var wrapped string
		for i := 0; i < len(encoded); i += 64 {
			end := min(i+64, len(encoded))
			wrapped += encoded[i:end] + "\n"
		}
		if _, err := ParsePrivateKey(wrapped); err != nil {
			t.Fatalf("ParsePrivateKey: %v", err)
		}
	})

	t.Run("rejects empty input", func(t *testing.T) {
		if _, err := ParsePrivateKey("   \n  "); err == nil {
			t.Error("want an error for empty input")
		}
	})

	t.Run("rejects garbage without panicking", func(t *testing.T) {
		for _, input := range []string{
			"not a key at all",
			"-----BEGIN PRIVATE KEY-----\nnot base64\n-----END PRIVATE KEY-----",
			base64.StdEncoding.EncodeToString([]byte("still not a key")),
		} {
			if _, err := ParsePrivateKey(input); err == nil {
				t.Errorf("want an error for %q", input)
			}
		}
	})

	// An RSA key in a .p8 would sign with RS256, not ES256, and Apple would
	// reject the resulting JWT. Failing at parse time gives a clearer error than
	// a 401 later.
	t.Run("rejects a non-EC key", func(t *testing.T) {
		block := &pem.Block{Type: "PRIVATE KEY", Bytes: []byte("bogus der")}
		if _, err := ParsePrivateKey(string(pem.EncodeToMemory(block))); err == nil {
			t.Error("want an error for a non-EC key")
		}
	})
}
