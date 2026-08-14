package iowrappers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/weihesdlegend/Vacation-planner/applemaps"
)

// AppleMapsConfig carries the credentials and transport for an AppleMapsClient.
type AppleMapsConfig struct {
	TeamID string
	KeyID  string
	// PrivateKey is the .p8 contents, either raw PEM or base64-encoded PEM.
	PrivateKey string
	// BaseURL defaults to Apple's. Tests point it at an httptest server.
	BaseURL string
	// HTTPClient carries the quota-counting transport when one is wired in.
	HTTPClient *http.Client
}

// AppleMapsClient adapts the applemaps package to Geocoder.
//
// It implements Geocoder and not SearchClient deliberately. Apple's Place object
// carries no opening hours, rating, price level, or photo at any endpoint or
// tier, and there is no join key from an Apple place ID back to a Google
// place_id, so those fields could never be backfilled. A place search served
// from Apple would be permanently hours-less. Address data has no such gap.
type AppleMapsClient struct {
	client *applemaps.Client
}

func CreateAppleMapsClient(cfg AppleMapsConfig) (*AppleMapsClient, error) {
	key, err := applemaps.ParsePrivateKey(cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	client, err := applemaps.New(applemaps.Options{
		TeamID:     cfg.TeamID,
		KeyID:      cfg.KeyID,
		PrivateKey: key,
		BaseURL:    cfg.BaseURL,
		HTTPClient: cfg.HTTPClient,
	})
	if err != nil {
		return nil, err
	}
	return &AppleMapsClient{client: client}, nil
}

// appleQueryString flattens a GeocodeQuery into Apple's single free-text q.
//
// Google matches structured components — ComponentLocality, ComponentCountry,
// ComponentAdministrativeArea — per field. Apple has no component parameters at
// all. Probing confirmed the flattened form still disambiguates: "Paris, TX,
// United States" and "Paris, Île-de-France, France" resolve to different,
// correct coordinates. Empty fields are dropped so the query never carries a
// bare separator.
func appleQueryString(query *GeocodeQuery) string {
	parts := make([]string, 0, 3)
	for _, field := range []string{query.City, query.AdminAreaLevelOne, query.Country} {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, ", ")
}

// appleAdminAreaLevelOne mirrors what Google writes into AdminAreaLevelOne:
// administrative_area_level_1's ShortName, which is an abbreviation where one
// exists and the long name otherwise. Apple splits those across two fields and
// omits the code for countries without conventional abbreviations.
func appleAdminAreaLevelOne(address *applemaps.StructuredAddress) string {
	if address.AdministrativeAreaCode != "" {
		return address.AdministrativeAreaCode
	}
	return address.AdministrativeArea
}

// applyApplePlace copies an Apple place's address onto a GeocodeQuery.
//
// A field is assigned only when Apple returned something for it. Forward
// geocoding a query that names an administrative area returns no locality, and
// PoiSearcher.Geocode writes the mutated query straight into the geocode:cities
// cache, so overwriting the caller's City with "" would poison the cache key.
func applyApplePlace(query *GeocodeQuery, place applemaps.Place) {
	if address := place.StructuredAddress; address != nil {
		if address.Locality != "" {
			query.City = address.Locality
		}
		if adminArea := appleAdminAreaLevelOne(address); adminArea != "" {
			query.AdminAreaLevelOne = adminArea
		}
	}
	if place.Country != "" {
		query.Country = place.Country
	}
}

func (c *AppleMapsClient) Geocode(ctx context.Context, query *GeocodeQuery) (float64, float64, error) {
	q := appleQueryString(query)
	if q == "" {
		return 0, 0, errors.New("applemaps: geocode query has no city, administrative area, or country")
	}

	places, err := c.client.Geocode(ctx, applemaps.GeocodeRequest{Q: q})
	if err != nil {
		return 0, 0, err
	}

	// applemaps.Geocode converts an empty result set into *NotFoundError, so a
	// nil error guarantees at least one place.
	place := places[0]
	applyApplePlace(query, place)
	return place.Coordinate.Latitude, place.Coordinate.Longitude, nil
}

func (c *AppleMapsClient) ReverseGeocode(ctx context.Context, latitude, longitude float64) (*GeocodeQuery, error) {
	places, err := c.client.ReverseGeocode(ctx, applemaps.ReverseGeocodeRequest{
		Latitude:  latitude,
		Longitude: longitude,
	})
	if err != nil {
		return nil, err
	}

	query := &GeocodeQuery{}
	applyApplePlace(query, places[0])
	return query, nil
}
