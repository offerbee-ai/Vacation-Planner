package iowrappers

import (
	"context"
	"errors"
	"testing"

	"github.com/weihesdlegend/Vacation-planner/POI"
	"github.com/weihesdlegend/Vacation-planner/applemaps"
)

// stubGeocoder stands in for Apple.
type stubGeocoder struct {
	calls int
	query *GeocodeQuery
	lat   float64
	lng   float64
	err   error
}

func (s *stubGeocoder) Geocode(_ context.Context, query *GeocodeQuery) (float64, float64, error) {
	s.calls++
	if s.err != nil {
		return 0, 0, s.err
	}
	if s.query != nil {
		*query = *s.query
	}
	return s.lat, s.lng, nil
}

func (s *stubGeocoder) ReverseGeocode(context.Context, float64, float64) (*GeocodeQuery, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.query, nil
}

func TestRouterUsesAppleAndSkipsGoogleOnSuccess(t *testing.T) {
	apple := &stubGeocoder{lat: 48.85, lng: 2.29, query: &GeocodeQuery{City: "Paris"}}
	google := &stubSearchClient{}
	router := NewAppleGeocodeRouter(apple, google, nil)

	lat, lng, err := router.Geocode(context.Background(), &GeocodeQuery{City: "Paris"})
	if err != nil {
		t.Fatalf("Geocode: %v", err)
	}
	if lat != 48.85 || lng != 2.29 {
		t.Errorf("coordinate: got %v,%v want 48.85,2.29", lat, lng)
	}
	if google.geocodeCalls != 0 {
		t.Errorf("google geocode calls: got %d, want 0", google.geocodeCalls)
	}
}

// Each of these is a distinct reason Apple cannot answer, and each must cost one
// Apple attempt and then a Google call rather than an error to the caller.
func TestRouterFallsBackToGoogle(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"transport or API error", errors.New("boom")},
		{"quota exhausted", &applemaps.QuotaError{APIError: &applemaps.APIError{StatusCode: 429, Message: "quota"}}},
		// Apple answers an unmatched geocode with HTTP 200 and an empty array;
		// the SDK converts that to NotFoundError. Google may still find it.
		{"no match", &applemaps.NotFoundError{Query: "nowhere"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			apple := &stubGeocoder{err: tc.err}
			google := &stubSearchClient{}
			router := NewAppleGeocodeRouter(apple, google, nil)

			if _, _, err := router.Geocode(context.Background(), &GeocodeQuery{City: "Paris"}); err != nil {
				t.Fatalf("Geocode: %v", err)
			}
			if apple.calls != 1 {
				t.Errorf("apple calls: got %d, want 1", apple.calls)
			}
			if google.geocodeCalls != 1 {
				t.Errorf("google geocode calls: got %d, want 1", google.geocodeCalls)
			}

			apple.calls, google.reverseGeocodeCalls = 0, 0
			if _, err := router.ReverseGeocode(context.Background(), 1, 2); err != nil {
				t.Fatalf("ReverseGeocode: %v", err)
			}
			if apple.calls != 1 || google.reverseGeocodeCalls != 1 {
				t.Errorf("reverse: apple=%d google=%d, want 1 and 1", apple.calls, google.reverseGeocodeCalls)
			}
		})
	}
}

// Apple has no opening hours at any tier and no join key back to a Google
// place_id, so a place it returned could never be enriched. There is no
// condition under which it serves a place search.
func TestRouterNeverSendsNearbySearchToApple(t *testing.T) {
	apple := &stubGeocoder{}
	google := &stubSearchClient{}
	router := NewAppleGeocodeRouter(apple, google, nil)

	if _, err := router.NearbySearch(context.Background(), &PlaceSearchRequest{
		PlaceCat: POI.PlaceCategoryEatery,
	}); err != nil {
		t.Fatalf("NearbySearch: %v", err)
	}
	if google.nearbySearchCalls != 1 {
		t.Errorf("google nearby calls: got %d, want 1", google.nearbySearchCalls)
	}
	if apple.calls != 0 {
		t.Errorf("apple calls: got %d, want 0 — Apple must never serve a place search", apple.calls)
	}
}
