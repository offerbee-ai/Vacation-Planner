package applemaps

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	directionsPath = "/v1/directions"
	etasPath       = "/v1/etas"

	// MaxETADestinations is the number of destinations /v1/etas accepts in one
	// call. Enforcing it locally turns what would be an opaque HTTP 400 into a
	// clear error, and costs nothing against the quota.
	MaxETADestinations = 10
)

// formatAppleTime renders a time the way Apple's date parameters require: ISO
// 8601 in UTC, for example 2020-09-15T16:42:00Z. A caller's local zone is
// converted rather than rejected.
func formatAppleTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// DirectionsRequest describes a /v1/directions call.
//
// Origin and Destination are each either an address or a "latitude,longitude"
// pair. Use FormatPoint to build the coordinate form.
type DirectionsRequest struct {
	// Origin is the starting address or coordinate. Required.
	Origin string
	// Destination is the ending address or coordinate. Required.
	Destination string
	// TransportType selects the mode of transportation.
	TransportType TransportType
	// DepartureDate is the intended departure. Apple accepts either this or
	// ArrivalDate, never both.
	DepartureDate *time.Time
	// ArrivalDate is the intended arrival. Apple accepts either this or
	// DepartureDate, never both.
	ArrivalDate *time.Time
	// Avoid lists features to route around. Tolls is Apple's only value.
	Avoid []DirectionsAvoid
	// RequestsAlternateRoutes asks for additional routes where available.
	RequestsAlternateRoutes bool
	// Lang overrides the client's default language, which also localises the
	// step instructions.
	Lang string
	// SearchLocation biases how Origin and Destination are interpreted.
	SearchLocation *Location
	// SearchRegion biases how Origin and Destination are interpreted.
	SearchRegion *MapRegion
	// UserLocation is used as a fallback bias when SearchLocation is unset.
	UserLocation *Location
}

// FormatPoint renders a coordinate for the Origin and Destination fields.
func FormatPoint(lat, lng float64) string {
	return formatLocation(lat, lng)
}

func (r DirectionsRequest) validate() error {
	if r.Origin == "" {
		return errors.New("applemaps: Directions requires Origin")
	}
	if r.Destination == "" {
		return errors.New("applemaps: Directions requires Destination")
	}
	// Apple documents these as mutually exclusive. Rejecting the combination
	// here gives a specific message instead of a generic 400, and saves a call.
	if r.DepartureDate != nil && r.ArrivalDate != nil {
		return errors.New("applemaps: Directions accepts DepartureDate or ArrivalDate, not both")
	}
	return nil
}

func (r DirectionsRequest) params(c *Client) url.Values {
	params := url.Values{}
	params.Set("origin", r.Origin)
	params.Set("destination", r.Destination)

	if r.TransportType != "" {
		params.Set("transportType", string(r.TransportType))
	}
	if r.DepartureDate != nil {
		params.Set("departureDate", formatAppleTime(*r.DepartureDate))
	}
	if r.ArrivalDate != nil {
		params.Set("arrivalDate", formatAppleTime(*r.ArrivalDate))
	}
	if len(r.Avoid) > 0 {
		values := make([]string, len(r.Avoid))
		for i, a := range r.Avoid {
			values[i] = string(a)
		}
		setStrings(params, "avoid", values)
	}
	if r.RequestsAlternateRoutes {
		params.Set("requestsAlternateRoutes", "true")
	}
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

// Directions returns routes between two locations.
func (c *Client) Directions(ctx context.Context, req DirectionsRequest) (*DirectionsResponse, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}

	var resp DirectionsResponse
	if err := c.get(ctx, directionsPath, req.params(c), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ETAsRequest describes a /v1/etas call.
type ETAsRequest struct {
	// Origin is the starting coordinate. Required.
	Origin Location
	// Destinations are the coordinates to estimate arrival at. At least one and
	// at most MaxETADestinations.
	Destinations []Location
	// TransportType selects the mode of transportation.
	TransportType TransportType
	// DepartureDate is the intended departure. Apple accepts either this or
	// ArrivalDate, never both. Omitting both uses the current time.
	DepartureDate *time.Time
	// ArrivalDate is the intended arrival.
	ArrivalDate *time.Time
}

func (r ETAsRequest) validate() error {
	if len(r.Destinations) == 0 {
		return errors.New("applemaps: ETAs requires at least one destination")
	}
	if len(r.Destinations) > MaxETADestinations {
		return fmt.Errorf("applemaps: ETAs accepts at most %d destinations, got %d",
			MaxETADestinations, len(r.Destinations))
	}
	if r.DepartureDate != nil && r.ArrivalDate != nil {
		return errors.New("applemaps: ETAs accepts DepartureDate or ArrivalDate, not both")
	}
	return nil
}

func (r ETAsRequest) params() url.Values {
	params := url.Values{}
	params.Set("origin", formatLocation(r.Origin.Latitude, r.Origin.Longitude))

	// Apple separates ETA destinations with a vertical bar, unlike every other
	// list parameter in this API, which uses commas — commas already separate
	// each destination's own latitude and longitude.
	destinations := make([]string, len(r.Destinations))
	for i, d := range r.Destinations {
		destinations[i] = formatLocation(d.Latitude, d.Longitude)
	}
	params.Set("destinations", strings.Join(destinations, "|"))

	if r.TransportType != "" {
		params.Set("transportType", string(r.TransportType))
	}
	if r.DepartureDate != nil {
		params.Set("departureDate", formatAppleTime(*r.DepartureDate))
	}
	if r.ArrivalDate != nil {
		params.Set("arrivalDate", formatAppleTime(*r.ArrivalDate))
	}
	return params
}

// ETAs returns estimated travel time and distance from one origin to up to
// MaxETADestinations destinations.
func (c *Client) ETAs(ctx context.Context, req ETAsRequest) ([]Eta, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}

	var resp EtaResponse
	if err := c.get(ctx, etasPath, req.params(), &resp); err != nil {
		return nil, err
	}
	return resp.ETAs, nil
}

// ResolvedStep is a step paired with the polyline it traverses.
type ResolvedStep struct {
	// Step is the step itself.
	Step Step
	// Path is the step's polyline. It is nil when Apple supplied no
	// StepPathIndex for the step, which is not an error — the field is optional.
	Path []Location
}

// ResolveRoute returns the steps of one route, each paired with its polyline.
//
// A DirectionsResponse is flattened rather than nested: Steps and StepPaths are
// global across all routes, a route reaches its steps through Route.StepIndexes,
// and a step reaches its path through Step.StepPathIndex. Every one of those
// indexes comes from the network, so indexing with them directly would turn a
// malformed or truncated upstream response into a panic that takes down the
// calling process. This method bounds-checks each one and returns an error
// instead.
func (r *DirectionsResponse) ResolveRoute(routeIndex int) ([]ResolvedStep, error) {
	if routeIndex < 0 || routeIndex >= len(r.Routes) {
		return nil, fmt.Errorf("applemaps: route index %d out of range (%d routes)", routeIndex, len(r.Routes))
	}
	route := r.Routes[routeIndex]

	resolved := make([]ResolvedStep, 0, len(route.StepIndexes))
	for _, stepIndex := range route.StepIndexes {
		if stepIndex < 0 || stepIndex >= len(r.Steps) {
			return nil, fmt.Errorf("applemaps: route %d references step index %d out of range (%d steps)",
				routeIndex, stepIndex, len(r.Steps))
		}
		step := r.Steps[stepIndex]

		var path []Location
		if step.StepPathIndex != nil {
			pathIndex := *step.StepPathIndex
			if pathIndex < 0 || pathIndex >= len(r.StepPaths) {
				return nil, fmt.Errorf("applemaps: step %d references step path index %d out of range (%d step paths)",
					stepIndex, pathIndex, len(r.StepPaths))
			}
			path = r.StepPaths[pathIndex]
		}

		resolved = append(resolved, ResolvedStep{Step: step, Path: path})
	}
	return resolved, nil
}
