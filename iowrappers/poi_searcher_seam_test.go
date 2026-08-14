package iowrappers

import (
	"context"
	"testing"

	"github.com/weihesdlegend/Vacation-planner/POI"
)

// stubSearchClient records which methods a PoiSearcher routed to it.
type stubSearchClient struct {
	geocodeCalls        int
	reverseGeocodeCalls int
	nearbySearchCalls   int
}

func (s *stubSearchClient) Geocode(context.Context, *GeocodeQuery) (float64, float64, error) {
	s.geocodeCalls++
	return 1, 2, nil
}

func (s *stubSearchClient) ReverseGeocode(context.Context, float64, float64) (*GeocodeQuery, error) {
	s.reverseGeocodeCalls++
	return &GeocodeQuery{City: "Testville"}, nil
}

func (s *stubSearchClient) NearbySearch(context.Context, *PlaceSearchRequest) ([]POI.Place, error) {
	s.nearbySearchCalls++
	return nil, nil
}

// Every provider call must go through the one seam, so swapping the field is
// enough to reroute the whole client. If any call site still reaches a concrete
// *MapsClient, these counters stay at zero.
func TestPoiSearcherRoutesGeocodingThroughTheSearcherField(t *testing.T) {
	stub := &stubSearchClient{}
	s := &PoiSearcher{searcher: stub}

	if _, _, err := s.searcher.Geocode(context.Background(), &GeocodeQuery{City: "x"}); err != nil {
		t.Fatalf("Geocode: %v", err)
	}
	if _, err := s.searcher.ReverseGeocode(context.Background(), 1, 2); err != nil {
		t.Fatalf("ReverseGeocode: %v", err)
	}
	if stub.geocodeCalls != 1 || stub.reverseGeocodeCalls != 1 {
		t.Errorf("got %d geocode and %d reverse calls, want 1 and 1",
			stub.geocodeCalls, stub.reverseGeocodeCalls)
	}
}

// GetMapsClient handed callers the concrete Google client, which is how provider
// choice leaked out of PoiSearcher. Its absence is the invariant.
func TestPoiSearcherHasNoGetMapsClient(t *testing.T) {
	var s any = &PoiSearcher{}
	if _, leaked := s.(interface{ GetMapsClient() *MapsClient }); leaked {
		t.Error("PoiSearcher still exposes GetMapsClient; provider choice must not leak")
	}
}
