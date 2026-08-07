package applemaps

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultMaxRetries is how many extra attempts a retryable failure gets.
	defaultMaxRetries = 2

	// retryBaseDelay is the first backoff interval; each subsequent retry
	// doubles it.
	retryBaseDelay = 200 * time.Millisecond

	// maxResponseBytes bounds a successful response body. Apple's largest
	// responses are directions with full polylines, which stay well under this.
	maxResponseBytes = 8 << 20
)

// Options configures a Client.
type Options struct {
	// TeamID is the Apple Developer team ID (JWT iss).
	TeamID string
	// KeyID is the MapKit key ID (JWT kid).
	KeyID string
	// PrivateKey is the key from the .p8 file, as returned by ParsePrivateKey.
	PrivateKey *ecdsa.PrivateKey

	// BaseURL defaults to DefaultBaseURL.
	BaseURL string
	// HTTPClient defaults to a client with a 15 second timeout.
	HTTPClient *http.Client
	// MaxRetries bounds retries of retryable failures. Zero means
	// defaultMaxRetries; a negative value disables retrying.
	MaxRetries int
	// Lang is the BCP 47 language applied to requests that do not set one.
	// Empty means Apple's default of en-US.
	Lang string
}

// Client calls the Apple Maps Server API.
//
// A Client is safe for concurrent use.
type Client struct {
	tokens     *TokenSource
	baseURL    string
	httpClient *http.Client
	maxRetries int
	lang       string

	// sleep is injectable so backoff is testable without real delays.
	sleep func(context.Context, time.Duration) error
}

// New returns a Client that manages its own TokenSource.
func New(opts Options) (*Client, error) {
	tokens, err := NewTokenSource(TokenSourceConfig{
		TeamID:     opts.TeamID,
		KeyID:      opts.KeyID,
		PrivateKey: opts.PrivateKey,
		BaseURL:    opts.BaseURL,
		HTTPClient: opts.HTTPClient,
	})
	if err != nil {
		return nil, err
	}
	return newWithTokenSource(tokens, opts), nil
}

func newWithTokenSource(tokens *TokenSource, opts Options) *Client {
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	maxRetries := opts.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultMaxRetries
	}
	if maxRetries < 0 {
		maxRetries = 0
	}

	return &Client{
		tokens:     tokens,
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		httpClient: httpClient,
		maxRetries: maxRetries,
		lang:       opts.Lang,
		sleep:      sleepContext,
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// get issues an authenticated GET and decodes a JSON response into out.
//
// Two distinct retry behaviours apply, and they are deliberately not merged. A
// 401 is retried exactly once after invalidating the cached token, because the
// likely cause is a token revoked before its stated expiry and one fresh
// exchange either fixes it or proves the credentials wrong. A 5xx is retried
// with backoff, because the likely cause is transient. A 429 is retried by
// neither: the quota is daily, so no amount of waiting inside one request helps.
func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	err := c.attempt(ctx, path, params, out)

	var authErr *AuthError
	if errors.As(err, &authErr) {
		c.tokens.Invalidate()
		return c.attempt(ctx, path, params, out)
	}
	return err
}

// attempt performs one logical request, retrying retryable status codes.
func (c *Client) attempt(ctx context.Context, path string, params url.Values, out any) error {
	var lastErr error

	for i := 0; i <= c.maxRetries; i++ {
		if i > 0 {
			// Exponential backoff: 200ms, 400ms, 800ms...
			delay := retryBaseDelay << (i - 1)
			if err := c.sleep(ctx, delay); err != nil {
				return err
			}
		}

		lastErr = c.once(ctx, path, params, out)
		if lastErr == nil {
			return nil
		}

		var apiErr *APIError
		if !errors.As(lastErr, &apiErr) || !retryable(apiErr.StatusCode) {
			return lastErr
		}
	}
	return lastErr
}

// once performs a single HTTP round trip.
func (c *Client) once(ctx context.Context, path string, params url.Values, out any) error {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return err
	}

	endpoint := c.baseURL + path
	if encoded := params.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("applemaps: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("applemaps: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		if readErr != nil {
			return fmt.Errorf("applemaps: %s: HTTP %d and unreadable body: %w", path, resp.StatusCode, readErr)
		}
		return newAPIError(resp.StatusCode, body)
	}

	if out == nil {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("applemaps: %s: read response: %w", path, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("applemaps: %s: decode response: %w", path, err)
	}
	return nil
}

// applyLang sets the lang parameter, preferring an explicit per-request value
// over the client default. Neither being set leaves the parameter off entirely
// so Apple applies its own default.
func (c *Client) applyLang(params url.Values, lang string) {
	if lang == "" {
		lang = c.lang
	}
	if lang != "" {
		params.Set("lang", lang)
	}
}

// formatCoord renders a coordinate component the way Apple's examples do, with
// no trailing zeros and no exponent.
func formatCoord(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// formatLocation renders a "latitude,longitude" pair, the form used by
// searchLocation, userLocation, loc, origin, and destination.
func formatLocation(lat, lng float64) string {
	return formatCoord(lat) + "," + formatCoord(lng)
}

// formatRegion renders a bounding box.
//
// The component order is north, east, south, west — not the south-west /
// north-east ordering that MapRegion's own field documentation describes. Apple
// specifies this ordering for the searchRegion query parameter specifically, and
// getting it wrong silently biases results toward the wrong area rather than
// producing an error.
func formatRegion(r MapRegion) string {
	return strings.Join([]string{
		formatCoord(r.NorthLatitude),
		formatCoord(r.EastLongitude),
		formatCoord(r.SouthLatitude),
		formatCoord(r.WestLongitude),
	}, ",")
}

// setCategories sets a comma-separated PoiCategory list, omitting the parameter
// entirely when the list is empty.
func setCategories(params url.Values, key string, categories []PoiCategory) {
	if len(categories) == 0 {
		return
	}
	parts := make([]string, len(categories))
	for i, c := range categories {
		parts[i] = string(c)
	}
	params.Set(key, strings.Join(parts, ","))
}

// setStrings sets a comma-separated string list, omitting the parameter when the
// list is empty.
func setStrings(params url.Values, key string, values []string) {
	if len(values) == 0 {
		return
	}
	params.Set(key, strings.Join(values, ","))
}
