package applemaps

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// DefaultBaseURL is the Apple Maps Server API root.
	DefaultBaseURL = "https://maps-api.apple.com"

	tokenPath = "/v1/token"

	// authJWTTTL is how long the locally signed auth JWT claims to be valid. It
	// is only ever presented once, immediately, to /v1/token, so it needs no
	// generous window. Apple rejects an auth JWT whose exp is more than 7 days
	// out; 20 minutes stays far inside that and limits the value of a captured
	// token.
	authJWTTTL = 20 * time.Minute

	// tokenRefreshMargin is how long before expiry a cached access token is
	// treated as stale. Apple issues 30-minute tokens, so 5 minutes leaves room
	// for a slow request to complete on a token that was valid when it started.
	tokenRefreshMargin = 5 * time.Minute

	// defaultTokenTTL stands in when a token response states no usable lifetime.
	// Apple is observed to issue 1800 seconds.
	defaultTokenTTL = 30 * time.Minute
)

// ParsePrivateKey parses the ECDSA private key from an Apple Maps .p8 file.
//
// It accepts either raw PEM ("-----BEGIN PRIVATE KEY-----...") or a base64
// encoding of that PEM. Both forms are supported because raw newlines survive
// heroku config:set and Docker --env-file but are flattened by many .env
// loaders, which would otherwise turn a correct key into an unparseable one at
// deploy time.
func ParsePrivateKey(value string) (*ecdsa.PrivateKey, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, errors.New("applemaps: private key is empty")
	}

	if !strings.HasPrefix(trimmed, "-----BEGIN") {
		// Whitespace is stripped before decoding so a base64 blob wrapped across
		// lines by a config system still parses.
		compact := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, trimmed)

		decoded, err := base64.StdEncoding.DecodeString(compact)
		if err != nil {
			return nil, fmt.Errorf("applemaps: private key is neither PEM nor base64-encoded PEM: %w", err)
		}
		trimmed = string(decoded)
	}

	// jwt/v5 tries x509.ParseECPrivateKey and falls back to
	// x509.ParsePKCS8PrivateKey, which is the encoding Apple's .p8 uses.
	key, err := jwt.ParseECPrivateKeyFromPEM([]byte(trimmed))
	if err != nil {
		return nil, fmt.Errorf("applemaps: parse private key: %w", err)
	}
	return key, nil
}

// TokenSourceConfig configures a TokenSource.
type TokenSourceConfig struct {
	// TeamID is the Apple Developer team ID, used as the JWT iss claim.
	TeamID string
	// KeyID is the MapKit key ID, used as the JWT kid header.
	KeyID string
	// PrivateKey is the key from the .p8 file, as returned by ParsePrivateKey.
	PrivateKey *ecdsa.PrivateKey
	// BaseURL defaults to DefaultBaseURL. Tests point it at an httptest server.
	BaseURL string
	// HTTPClient defaults to a client with a 10 second timeout.
	HTTPClient *http.Client
}

// TokenSource issues and caches Apple Maps access tokens.
//
// Apple's auth is a two-hop exchange: a JWT signed locally with the .p8 key is
// presented to /v1/token, which returns a short-lived access token used on every
// other endpoint. TokenSource owns that second token's lifetime.
//
// A TokenSource is safe for concurrent use.
type TokenSource struct {
	teamID     string
	keyID      string
	key        *ecdsa.PrivateKey
	baseURL    string
	httpClient *http.Client

	// now is injectable so expiry behaviour is testable without sleeping.
	now func() time.Time

	mu     sync.Mutex
	token  string
	expiry time.Time
	// refreshAt is when the cached token stops being handed out. It is computed
	// once, at exchange time, rather than derived from expiry on every read,
	// because the margin it subtracts depends on the lifetime Apple stated for
	// that particular token.
	refreshAt time.Time
	// generation identifies the current cached token. It increments on every
	// successful exchange so a caller holding a token that failed can ask for it
	// to be discarded without discarding whatever replaced it.
	generation uint64
}

// NewTokenSource validates the credentials and returns a TokenSource. It makes
// no network call; the first exchange happens on the first Token call.
func NewTokenSource(cfg TokenSourceConfig) (*TokenSource, error) {
	if strings.TrimSpace(cfg.TeamID) == "" {
		return nil, errors.New("applemaps: TeamID is required")
	}
	if strings.TrimSpace(cfg.KeyID) == "" {
		return nil, errors.New("applemaps: KeyID is required")
	}
	if cfg.PrivateKey == nil {
		return nil, errors.New("applemaps: PrivateKey is required")
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	return &TokenSource{
		teamID:     cfg.TeamID,
		keyID:      cfg.KeyID,
		key:        cfg.PrivateKey,
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		httpClient: httpClient,
		now:        time.Now,
	}, nil
}

// Token returns a valid access token, exchanging or refreshing as needed.
//
// The lock is held across the exchange rather than only around the cache read.
// That serialises a cold burst into one HTTP call instead of one per caller,
// which matters because /v1/token consumes the same daily quota as every other
// endpoint — a 50-goroutine cold start would otherwise spend 50 calls to learn
// the same token.
func (ts *TokenSource) Token(ctx context.Context) (string, error) {
	token, _, err := ts.tokenWithGeneration(ctx)
	return token, err
}

// tokenWithGeneration returns a valid access token along with the generation that
// identifies it, for callers that may need to invalidate exactly that token.
func (ts *TokenSource) tokenWithGeneration(ctx context.Context) (string, uint64, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.token != "" && ts.now().Before(ts.refreshAt) {
		return ts.token, ts.generation, nil
	}
	token, err := ts.exchangeLocked(ctx)
	if err != nil {
		return "", 0, err
	}
	return token, ts.generation, nil
}

// Invalidate discards the cached token unconditionally, so the next Token call
// re-exchanges.
func (ts *TokenSource) Invalidate() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.clearLocked()
}

// invalidateGeneration discards the cached token only if it is still the one
// identified by generation.
//
// The unconditional Invalidate is wrong on the client's 401 path. A burst of N
// in-flight requests sharing one revoked token all get 401 and all want a refresh;
// serialised by the mutex, each would clear the token the previous goroutine had
// just fetched and exchange again — N calls against a quota shared with a
// production app, and a retry left holding a token another goroutine already
// discarded. Checking the generation makes every 401 after the first a no-op,
// because the refresh they were asking for has already happened.
func (ts *TokenSource) invalidateGeneration(generation uint64) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.generation != generation {
		return
	}
	ts.clearLocked()
}

// clearLocked drops the cached token. Callers must hold ts.mu.
func (ts *TokenSource) clearLocked() {
	ts.token = ""
	ts.expiry = time.Time{}
	ts.refreshAt = time.Time{}
	// The generation advances so an invalidateGeneration racing on the token just
	// dropped does not go on to clear its replacement.
	ts.generation++
}

// authJWT builds and signs the short-lived JWT that /v1/token accepts.
func (ts *TokenSource) authJWT() (string, error) {
	now := ts.now()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": ts.teamID,
		"iat": now.Unix(),
		"exp": now.Add(authJWTTTL).Unix(),
	})
	// Apple identifies which of a team's keys signed the JWT by the kid header;
	// without it the token is rejected as invalid.
	token.Header["kid"] = ts.keyID

	signed, err := token.SignedString(ts.key)
	if err != nil {
		return "", fmt.Errorf("applemaps: sign auth JWT: %w", err)
	}
	return signed, nil
}

// exchangeLocked performs the /v1/token call. Callers must hold ts.mu.
func (ts *TokenSource) exchangeLocked(ctx context.Context) (string, error) {
	authToken, err := ts.authJWT()
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.baseURL+tokenPath, nil)
	if err != nil {
		return "", fmt.Errorf("applemaps: build token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Accept", "application/json")

	resp, err := ts.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("applemaps: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		return "", fmt.Errorf("applemaps: read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", newAPIError(resp.StatusCode, body)
	}

	var parsed TokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("applemaps: decode token response: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", errors.New("applemaps: token response contained no access token")
	}

	now := ts.now()
	ttl := time.Duration(parsed.ExpiresInSeconds) * time.Second

	// A response that states no usable lifetime must not be taken at face value.
	// Trusting it would put expiry at or before now, which is inside the refresh
	// margin, so every subsequent call would treat the token as stale and
	// exchange again — turning one malformed field into a permanent doubling of
	// quota consumption on a budget shared with MapKit JS. Apple issues 1800
	// seconds; assume that and let the 401 path correct us if the token really
	// was shorter-lived.
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}

	// A stated lifetime shorter than twice the margin would leave no window to
	// hand the token out in. Halving it keeps the token cacheable while still
	// refreshing early, rather than subtracting a fixed margin that the lifetime
	// cannot cover.
	margin := tokenRefreshMargin
	if margin > ttl/2 {
		margin = ttl / 2
	}

	ts.token = parsed.AccessToken
	ts.expiry = now.Add(ttl)
	ts.refreshAt = now.Add(ttl - margin)
	ts.generation++
	return ts.token, nil
}
