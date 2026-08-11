package applemaps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

func TestGeocodeEncodesParams(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, whiteHouseGeocodeResponse)
	})

	places, err := client.Geocode(context.Background(), GeocodeRequest{
		Q:                "1600 Pennsylvania Ave NW",
		LimitToCountries: []string{"US", "CA"},
		Lang:             "en-GB",
		SearchLocation:   &Location{Latitude: 38.9, Longitude: -77.03},
		SearchRegion:     &MapRegion{NorthLatitude: 39, EastLongitude: -77, SouthLatitude: 38, WestLongitude: -78},
		UserLocation:     &Location{Latitude: 40.7, Longitude: -74},
	})
	if err != nil {
		t.Fatalf("Geocode: %v", err)
	}

	if gotPath != geocodePath {
		t.Errorf("path: got %q, want %q", gotPath, geocodePath)
	}
	checks := map[string]string{
		"q":                "1600 Pennsylvania Ave NW",
		"limitToCountries": "US,CA",
		"lang":             "en-GB",
		"searchLocation":   "38.9,-77.03",
		"searchRegion":     "39,-77,38,-78",
		"userLocation":     "40.7,-74",
	}
	for key, want := range checks {
		if got := gotQuery.Get(key); got != want {
			t.Errorf("%s: got %q, want %q", key, got, want)
		}
	}

	if len(places) != 1 {
		t.Fatalf("places: got %d, want 1", len(places))
	}
	if places[0].Coordinate.Latitude != 38.8976635 {
		t.Errorf("latitude: got %v", places[0].Coordinate.Latitude)
	}
}

func TestGeocodeOmitsUnsetOptionalParams(t *testing.T) {
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		fmt.Fprint(w, whiteHouseGeocodeResponse)
	})

	if _, err := client.Geocode(context.Background(), GeocodeRequest{Q: "somewhere"}); err != nil {
		t.Fatalf("Geocode: %v", err)
	}

	for _, key := range []string{"limitToCountries", "searchLocation", "searchRegion", "userLocation", "lang"} {
		if _, present := gotQuery[key]; present {
			t.Errorf("%s should be absent when unset, got %q", key, gotQuery.Get(key))
		}
	}
}

// Apple answers an unresolvable address with HTTP 200 and an empty array. Left
// as-is that is indistinguishable from a successful lookup, so it becomes an
// error here.
func TestGeocodeEmptyResultsIsNotFound(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"results":[]}`)
	})

	_, err := client.Geocode(context.Background(), GeocodeRequest{Q: "nowhere at all"})
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("got %T (%v), want *NotFoundError", err, err)
	}
	if notFound.Query != "nowhere at all" {
		t.Errorf("query: got %q", notFound.Query)
	}
}

func TestGeocodeRequiresQ(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request should be sent without Q")
	})
	if _, err := client.Geocode(context.Background(), GeocodeRequest{}); err == nil {
		t.Error("want an error when Q is empty")
	}
}

func TestReverseGeocode(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, whiteHouseGeocodeResponse)
	})

	places, err := client.ReverseGeocode(context.Background(), ReverseGeocodeRequest{
		Latitude:  37.3316851,
		Longitude: -122.0300674,
		Lang:      "fr-FR",
	})
	if err != nil {
		t.Fatalf("ReverseGeocode: %v", err)
	}

	if gotPath != reverseGeocodePath {
		t.Errorf("path: got %q, want %q", gotPath, reverseGeocodePath)
	}
	if got, want := gotQuery.Get("loc"), "37.3316851,-122.0300674"; got != want {
		t.Errorf("loc: got %q, want %q", got, want)
	}
	if got := gotQuery.Get("lang"); got != "fr-FR" {
		t.Errorf("lang: got %q", got)
	}
	if len(places) != 1 {
		t.Errorf("places: got %d, want 1", len(places))
	}
}

func TestReverseGeocodeEmptyResultsIsNotFound(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"results":[]}`)
	})

	// Mid-Pacific: a real coordinate that resolves to no address.
	_, err := client.ReverseGeocode(context.Background(), ReverseGeocodeRequest{Latitude: 0, Longitude: -160})
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("got %T (%v), want *NotFoundError", err, err)
	}
	if notFound.Query != "0,-160" {
		t.Errorf("query: got %q, want the coordinate", notFound.Query)
	}
}

func TestReverseGeocodeSendsOnlyLocAndLang(t *testing.T) {
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		fmt.Fprint(w, whiteHouseGeocodeResponse)
	})

	if _, err := client.ReverseGeocode(context.Background(), ReverseGeocodeRequest{Latitude: 1, Longitude: 2}); err != nil {
		t.Fatalf("ReverseGeocode: %v", err)
	}

	// Apple documents no bias parameters for this endpoint; sending them would
	// be silently ignored at best.
	if len(gotQuery) != 1 {
		t.Errorf("want only loc, got %v", gotQuery)
	}
}

func TestGeocodePropagatesAPIErrors(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"message":"Quota exceeded"}`)
	})

	_, err := client.Geocode(context.Background(), GeocodeRequest{Q: "anywhere"})
	var quotaErr *QuotaError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("got %T (%v), want *QuotaError", err, err)
	}
}
