package iowrappers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// appleTestKey returns a throwaway P-256 key in the PEM form Apple's .p8 uses.
func appleTestKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// appleTestClient points an AppleMapsClient at a stub that answers the token
// exchange itself, so tests only describe the endpoint under test.
func appleTestClient(t *testing.T, handler http.HandlerFunc) (*AppleMapsClient, *url.Values) {
	t.Helper()
	lastQuery := &url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/token" {
			_, _ = fmt.Fprint(w, `{"accessToken":"test-token","expiresInSeconds":1800}`)
			return
		}
		q := r.URL.Query()
		*lastQuery = q
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	client, err := CreateAppleMapsClient(AppleMapsConfig{
		TeamID: "TEAM123456", KeyID: "KEY7890123",
		PrivateKey: appleTestKey(t), BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("CreateAppleMapsClient: %v", err)
	}
	return client, lastQuery
}

// Apple omits administrativeAreaCode for countries with no conventional
// subdivision abbreviation — confirmed live for France, Germany, and Japan,
// while the US, Australia, and Canada all return one. Google fills
// AdminAreaLevelOne from administrative_area_level_1's ShortName, which falls
// back to the long name in exactly those cases, so the adapter must too.
func TestAppleReverseGeocodeMapsAdminAreaBothWays(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantCity  string
		wantAdmin string
		wantCtry  string
	}{
		{
			name: "code present",
			body: `{"results":[{"country":"United States","countryCode":"US",
				"coordinate":{"latitude":37.33,"longitude":-122.03},
				"structuredAddress":{"locality":"Cupertino",
					"administrativeArea":"California","administrativeAreaCode":"CA"}}]}`,
			wantCity: "Cupertino", wantAdmin: "CA", wantCtry: "United States",
		},
		{
			name: "code absent falls back to the name",
			body: `{"results":[{"country":"France","countryCode":"FR",
				"coordinate":{"latitude":48.85,"longitude":2.29},
				"structuredAddress":{"locality":"Paris",
					"administrativeArea":"Île-de-France"}}]}`,
			wantCity: "Paris", wantAdmin: "Île-de-France", wantCtry: "France",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := appleTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, tc.body)
			})
			got, err := client.ReverseGeocode(context.Background(), 37.33, -122.03)
			if err != nil {
				t.Fatalf("ReverseGeocode: %v", err)
			}
			if got.City != tc.wantCity || got.AdminAreaLevelOne != tc.wantAdmin || got.Country != tc.wantCtry {
				t.Errorf("got %+v, want {%s %s %s}", *got, tc.wantCity, tc.wantAdmin, tc.wantCtry)
			}
		})
	}
}

// Forward-geocoding a query that names an administrative area rather than a
// locality returns an empty locality — confirmed live for "Tokyo, Tokyo, Japan".
// PoiSearcher.Geocode writes the mutated query to the geocode:cities cache, so a
// blank City must not reach it. Google can clobber freely because its component
// matching always yields a locality; Apple cannot.
func TestAppleGeocodeNeverOverwritesWithAnEmptyValue(t *testing.T) {
	client, _ := appleTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"results":[{"country":"Japan","countryCode":"JP",
			"coordinate":{"latitude":35.6895,"longitude":139.6917},
			"structuredAddress":{"administrativeArea":"Tokyo"}}]}`)
	})

	query := &GeocodeQuery{City: "Tokyo", AdminAreaLevelOne: "Tokyo", Country: "Japan"}
	lat, lng, err := client.Geocode(context.Background(), query)
	if err != nil {
		t.Fatalf("Geocode: %v", err)
	}
	if lat != 35.6895 || lng != 139.6917 {
		t.Errorf("coordinate: got %v,%v", lat, lng)
	}
	if query.City != "Tokyo" {
		t.Errorf("City: got %q, want the caller's %q preserved", query.City, "Tokyo")
	}
}

// Google takes structured components; Apple takes one free-text q. Probed live:
// "Paris, TX, United States" and "Paris, Île-de-France, France" resolve to
// different, correct coordinates, so the flattening preserves disambiguation.
// Empty fields must be dropped rather than left as empty segments.
func TestAppleGeocodeFlattensTheQuery(t *testing.T) {
	tests := []struct {
		name  string
		query GeocodeQuery
		wantQ string
	}{
		{"all three", GeocodeQuery{City: "Paris", AdminAreaLevelOne: "TX", Country: "United States"}, "Paris, TX, United States"},
		{"no admin area", GeocodeQuery{City: "Paris", Country: "France"}, "Paris, France"},
		{"city only", GeocodeQuery{City: "Paris"}, "Paris"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, lastQuery := appleTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, `{"results":[{"country":"United States",
					"coordinate":{"latitude":33.66,"longitude":-95.55},
					"structuredAddress":{"locality":"Paris","administrativeAreaCode":"TX"}}]}`)
			})
			q := tc.query
			if _, _, err := client.Geocode(context.Background(), &q); err != nil {
				t.Fatalf("Geocode: %v", err)
			}
			if got := lastQuery.Get("q"); got != tc.wantQ {
				t.Errorf("q: got %q, want %q", got, tc.wantQ)
			}
		})
	}
}

func TestAppleGeocodeRejectsAnEmptyQuery(t *testing.T) {
	client, _ := appleTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be sent for an empty query")
	})
	if _, _, err := client.Geocode(context.Background(), &GeocodeQuery{}); err == nil {
		t.Error("want an error when every field is empty")
	}
}
