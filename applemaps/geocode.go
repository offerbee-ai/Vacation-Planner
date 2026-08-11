package applemaps

import (
	"context"
	"errors"
	"net/url"
)

const (
	geocodePath        = "/v1/geocode"
	reverseGeocodePath = "/v1/reverseGeocode"
)

// GeocodeRequest describes a /v1/geocode call.
//
// SearchLocation, SearchRegion, and UserLocation are hints that bias results
// toward an area. Apple does not treat them as constraints, so a result can lie
// outside them; callers needing a hard geographic bound must filter the results
// themselves.
type GeocodeRequest struct {
	// Q is the address to geocode. Required.
	Q string
	// LimitToCountries is a list of two-letter ISO 3166-1 codes. With two or
	// more, Apple returns the best available results for some or all of them
	// rather than everything matching in each.
	LimitToCountries []string
	// Lang overrides the client's default language for this request.
	Lang string
	// SearchLocation biases results toward a coordinate.
	SearchLocation *Location
	// SearchRegion biases results toward a bounding box.
	SearchRegion *MapRegion
	// UserLocation is the user's own coordinate, used as a fallback bias when
	// SearchLocation is unset.
	UserLocation *Location
}

func (r GeocodeRequest) params(c *Client) url.Values {
	params := url.Values{}
	params.Set("q", r.Q)
	setStrings(params, "limitToCountries", r.LimitToCountries)
	c.applyLang(params, r.Lang)
	if r.SearchLocation != nil {
		params.Set("searchLocation", formatLocation(r.SearchLocation.Latitude, r.SearchLocation.Longitude))
	}
	if r.SearchRegion != nil {
		params.Set("searchRegion", formatRegion(*r.SearchRegion))
	}
	if r.UserLocation != nil {
		params.Set("userLocation", formatLocation(r.UserLocation.Latitude, r.UserLocation.Longitude))
	}
	return params
}

// Geocode resolves an address to one or more places.
//
// An address that matches nothing returns *NotFoundError rather than an empty
// slice, because Apple answers that case with HTTP 200 and an empty results
// array — indistinguishable from success unless it is turned into an error here.
func (c *Client) Geocode(ctx context.Context, req GeocodeRequest) ([]Place, error) {
	if req.Q == "" {
		return nil, errors.New("applemaps: Geocode requires Q")
	}

	var resp PlaceResults
	if err := c.get(ctx, geocodePath, req.params(c), &resp); err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return nil, &NotFoundError{Query: req.Q}
	}
	return resp.Results, nil
}

// ReverseGeocodeRequest describes a /v1/reverseGeocode call. Apple accepts only
// a coordinate and a language for this endpoint — none of the bias parameters
// apply.
type ReverseGeocodeRequest struct {
	Latitude  float64
	Longitude float64
	// Lang overrides the client's default language for this request.
	Lang string
}

// ReverseGeocode resolves a coordinate to one or more addresses.
//
// A coordinate that matches nothing — mid-ocean, for instance — returns
// *NotFoundError, for the same reason as Geocode.
func (c *Client) ReverseGeocode(ctx context.Context, req ReverseGeocodeRequest) ([]Place, error) {
	params := url.Values{}
	loc := formatLocation(req.Latitude, req.Longitude)
	params.Set("loc", loc)
	c.applyLang(params, req.Lang)

	var resp PlaceResults
	if err := c.get(ctx, reverseGeocodePath, params, &resp); err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return nil, &NotFoundError{Query: loc}
	}
	return resp.Results, nil
}
