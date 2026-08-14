package iowrappers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/weihesdlegend/Vacation-planner/POI"
	"github.com/weihesdlegend/Vacation-planner/applemaps"
)

// AppleGeocodeRouter serves geocoding from Apple and everything else from Google.
//
// The split is a property of this type rather than a rule spread across
// PoiSearcher: Apple can answer address questions completely, and cannot answer
// place questions at all, so NearbySearch has no routing decision to make.
type AppleGeocodeRouter struct {
	apple  Geocoder
	google SearchClient
	// quota may be nil, which disables the pre-emptive check.
	quota *QuotaCounter
}

func NewAppleGeocodeRouter(apple Geocoder, google SearchClient, quota *QuotaCounter) *AppleGeocodeRouter {
	return &AppleGeocodeRouter{apple: apple, google: google, quota: quota}
}

// useApple reports whether Apple should be tried, logging the reason when not.
func (r *AppleGeocodeRouter) useApple(ctx context.Context, method string) bool {
	if r.apple == nil {
		return false
	}
	if r.quota != nil && r.quota.OverThreshold(ctx) {
		Logger.Infow("applemaps: routing to Google", "method", method, "reason", "daily quota threshold reached")
		return false
	}
	return true
}

// logFallback records why an Apple attempt was abandoned. A fallback costs one
// Apple call plus one Google call, so a silent one hides real spend and makes
// the Apple hit rate unmeasurable.
func logFallback(method string, err error) {
	reason := "error"
	var quotaErr *applemaps.QuotaError
	var notFound *applemaps.NotFoundError
	switch {
	case errors.As(err, &quotaErr):
		reason = "quota exhausted"
	case errors.As(err, &notFound):
		reason = "no match"
	}
	Logger.Infow("applemaps: falling back to Google", "method", method, "reason", reason, "error", err)
}

func (r *AppleGeocodeRouter) Geocode(ctx context.Context, query *GeocodeQuery) (float64, float64, error) {
	if r.useApple(ctx, "Geocode") {
		// Apple's adapter mutates the query it is given, so it gets a copy: a
		// failed attempt must not leave half-corrected fields for Google.
		attempt := *query
		lat, lng, err := r.apple.Geocode(ctx, &attempt)
		if err == nil {
			*query = attempt
			return lat, lng, nil
		}
		logFallback("Geocode", err)
	}
	return r.google.Geocode(ctx, query)
}

func (r *AppleGeocodeRouter) ReverseGeocode(ctx context.Context, latitude, longitude float64) (*GeocodeQuery, error) {
	if r.useApple(ctx, "ReverseGeocode") {
		query, err := r.apple.ReverseGeocode(ctx, latitude, longitude)
		if err == nil {
			return query, nil
		}
		logFallback("ReverseGeocode", err)
	}
	return r.google.ReverseGeocode(ctx, latitude, longitude)
}

// NearbySearch always goes to Google. See the type comment.
func (r *AppleGeocodeRouter) NearbySearch(ctx context.Context, request *PlaceSearchRequest) ([]POI.Place, error) {
	return r.google.NearbySearch(ctx, request)
}

// AppleMapsSettings is the deployment-time configuration for Apple Maps.
type AppleMapsSettings struct {
	// Enabled is false by default. Nothing Apple-derived is requested or cached
	// until it is deliberately turned on, which is also what keeps the
	// unresolved question of Apple's caching terms off the merge path.
	Enabled bool
	// TeamID is the Apple Developer team ID, the JWT iss claim.
	TeamID string
	// KeyID is the MapKit key ID, the JWT kid header.
	KeyID string
	// PrivateKey is the .p8 contents, raw PEM or base64-encoded PEM.
	PrivateKey string
	// QuotaThreshold is the fraction of the daily allowance at which Apple stops
	// being used. Zero means the QuotaConfig default.
	QuotaThreshold float64
	// ExternalAllowance is the calls per day assumed spent by MapKit JS on the
	// same team, which this service cannot observe.
	ExternalAllowance int
}

// EnableAppleMaps routes geocoding through Apple, keeping Google as the
// fallback and as the only place-search provider.
//
// It returns an error rather than logging and continuing so the caller can
// decide, but the caller in main deliberately continues: a missing or malformed
// credential should cost the quota saving, not the service.
func (s *PoiSearcher) EnableAppleMaps(settings AppleMapsSettings) error {
	if !settings.Enabled {
		return nil
	}

	quota := NewQuotaCounter(s.redisClient, QuotaConfig{
		Threshold:         settings.QuotaThreshold,
		ExternalAllowance: settings.ExternalAllowance,
	})

	// The counter lives in the transport so token exchanges and retries are
	// counted alongside the calls a search makes directly.
	appleClient, err := CreateAppleMapsClient(AppleMapsConfig{
		TeamID:     settings.TeamID,
		KeyID:      settings.KeyID,
		PrivateKey: settings.PrivateKey,
		HTTPClient: &http.Client{
			Timeout:   15 * time.Second,
			Transport: quota.Transport(nil),
		},
	})
	if err != nil {
		return err
	}

	s.searcher = NewAppleGeocodeRouter(appleClient, s.searcher, quota)
	Logger.Infow("applemaps: geocoding enabled", "team", settings.TeamID)
	return nil
}
