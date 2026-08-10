package applemaps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDirectionsEncodesParams(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{"routes":[]}`)
	})

	departure := time.Date(2026, 9, 15, 16, 42, 0, 0, time.UTC)
	_, err := client.Directions(context.Background(), DirectionsRequest{
		Origin:                  FormatPoint(37.7857, -122.4011),
		Destination:             "San Francisco City Hall, CA",
		TransportType:           TransportTypeAutomobile,
		DepartureDate:           &departure,
		Avoid:                   []DirectionsAvoid{DirectionsAvoidTolls},
		RequestsAlternateRoutes: true,
		Lang:                    "en-US",
		SearchLocation:          &Location{Latitude: 37.78, Longitude: -122.4},
	})
	if err != nil {
		t.Fatalf("Directions: %v", err)
	}

	if gotPath != directionsPath {
		t.Errorf("path: got %q, want %q", gotPath, directionsPath)
	}
	checks := map[string]string{
		"origin":                  "37.7857,-122.4011",
		"destination":             "San Francisco City Hall, CA",
		"transportType":           "Automobile",
		"departureDate":           "2026-09-15T16:42:00Z",
		"avoid":                   "Tolls",
		"requestsAlternateRoutes": "true",
		"lang":                    "en-US",
		"searchLocation":          "37.78,-122.4",
	}
	for key, want := range checks {
		if got := gotQuery.Get(key); got != want {
			t.Errorf("%s: got %q, want %q", key, got, want)
		}
	}
	if _, present := gotQuery["arrivalDate"]; present {
		t.Error("arrivalDate should be absent when unset")
	}
}

// Apple's date parameters are ISO 8601 in UTC. A caller working in a local zone
// should get a correct conversion, not a rejection or a wrong instant.
func TestDirectionsConvertsNonUTCTimes(t *testing.T) {
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{"routes":[]}`)
	})

	// 09:42 at UTC-7 is 16:42 UTC.
	zone := time.FixedZone("PDT", -7*3600)
	arrival := time.Date(2026, 9, 15, 9, 42, 0, 0, zone)

	if _, err := client.Directions(context.Background(), DirectionsRequest{
		Origin: "a", Destination: "b", ArrivalDate: &arrival,
	}); err != nil {
		t.Fatalf("Directions: %v", err)
	}
	if got, want := gotQuery.Get("arrivalDate"), "2026-09-15T16:42:00Z"; got != want {
		t.Errorf("arrivalDate: got %q, want %q", got, want)
	}
}

func TestDirectionsValidation(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		req  DirectionsRequest
	}{
		{"missing origin", DirectionsRequest{Destination: "b"}},
		{"missing destination", DirectionsRequest{Origin: "a"}},
		{
			// Apple accepts one or the other. Catching it locally beats a
			// generic 400 and does not spend a quota call.
			name: "both dates",
			req:  DirectionsRequest{Origin: "a", Destination: "b", DepartureDate: &now, ArrivalDate: &now},
		},
		{
			// /v1/directions documents Automobile, Walking, and Cycling only.
			// Transit is valid for ETAs alone, matching MapKit, which gives
			// transit travel times but no transit turn-by-turn.
			name: "transit",
			req:  DirectionsRequest{Origin: "a", Destination: "b", TransportType: TransportTypeTransit},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("no request should be sent for an invalid DirectionsRequest")
			})
			if _, err := client.Directions(context.Background(), tc.req); err == nil {
				t.Error("want a validation error")
			}
		})
	}
}

// Transit is rejected for Directions but must stay available for ETAs, which is
// the whole point of splitting the two lists.
func TestETAsAcceptsTransit(t *testing.T) {
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{"etas":[]}`)
	})

	if _, err := client.ETAs(context.Background(), ETAsRequest{
		Origin:        Location{Latitude: 37.33, Longitude: -122.03},
		Destinations:  []Location{{Latitude: 37.32, Longitude: -121.94}},
		TransportType: TransportTypeTransit,
	}); err != nil {
		t.Fatalf("ETAs with Transit: %v", err)
	}
	if got := gotQuery.Get("transportType"); got != "Transit" {
		t.Errorf("transportType: got %q, want %q", got, "Transit")
	}
}

// Every other list parameter in this API is comma-separated; ETA destinations
// are bar-separated, because commas already separate each pair's components.
func TestETAsPipeJoinsDestinations(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{"etas":[{"distanceMeters":1200,"expectedTravelTimeSeconds":300,"staticTravelTimeSeconds":280,"transportType":"Automobile","destination":{"latitude":37.32,"longitude":-121.94}}]}`)
	})

	etas, err := client.ETAs(context.Background(), ETAsRequest{
		Origin: Location{Latitude: 37.331423, Longitude: -122.030503},
		Destinations: []Location{
			{Latitude: 37.32556561130194, Longitude: -121.94635203581443},
			{Latitude: 37.44176585512703, Longitude: -122.17259315798667},
		},
		TransportType: TransportTypeAutomobile,
	})
	if err != nil {
		t.Fatalf("ETAs: %v", err)
	}

	if gotPath != etasPath {
		t.Errorf("path: got %q, want %q", gotPath, etasPath)
	}
	if got, want := gotQuery.Get("origin"), "37.331423,-122.030503"; got != want {
		t.Errorf("origin: got %q, want %q", got, want)
	}
	wantDestinations := "37.32556561130194,-121.94635203581443|37.44176585512703,-122.17259315798667"
	if got := gotQuery.Get("destinations"); got != wantDestinations {
		t.Errorf("destinations: got %q, want %q", got, wantDestinations)
	}

	if len(etas) != 1 {
		t.Fatalf("etas: got %d, want 1", len(etas))
	}
	if etas[0].DistanceMeters == nil || *etas[0].DistanceMeters != 1200 {
		t.Errorf("distanceMeters: got %v", etas[0].DistanceMeters)
	}
	if etas[0].TransportType != TransportTypeAutomobile {
		t.Errorf("transportType: got %q", etas[0].TransportType)
	}
}

func TestETAsValidation(t *testing.T) {
	now := time.Now()
	tooMany := make([]Location, MaxETADestinations+1)

	tests := []struct {
		name string
		req  ETAsRequest
	}{
		{"no destinations", ETAsRequest{Origin: Location{}}},
		{"too many destinations", ETAsRequest{Destinations: tooMany}},
		{
			name: "both dates",
			req: ETAsRequest{
				Destinations:  []Location{{}},
				DepartureDate: &now,
				ArrivalDate:   &now,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("no request should be sent for an invalid ETAsRequest")
			})
			if _, err := client.ETAs(context.Background(), tc.req); err == nil {
				t.Error("want a validation error")
			}
		})
	}

	t.Run("exactly the maximum is allowed", func(t *testing.T) {
		client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"etas":[]}`)
		})
		if _, err := client.ETAs(context.Background(), ETAsRequest{
			Destinations: make([]Location, MaxETADestinations),
		}); err != nil {
			t.Errorf("ETAs: %v", err)
		}
	})
}

// A realistic flattened response: two routes sharing a global step array, whose
// steps point into a global step-path array.
const twoRouteDirections = `{
  "origin": {"name":"Start","coordinate":{"latitude":37.78,"longitude":-122.40}},
  "destination": {"name":"End","coordinate":{"latitude":37.79,"longitude":-122.41}},
  "routes": [
    {"name":"Fast","distanceMeters":1000,"durationSeconds":300,"hasTolls":true,"stepIndexes":[0,1],"transportType":"Automobile"},
    {"name":"Scenic","distanceMeters":1500,"durationSeconds":500,"hasTolls":false,"stepIndexes":[2],"transportType":"Automobile"}
  ],
  "steps": [
    {"instructions":"Head north","distanceMeters":400,"stepPathIndex":0},
    {"instructions":"Turn left","distanceMeters":600,"stepPathIndex":1},
    {"instructions":"Take the scenic road","distanceMeters":1500,"stepPathIndex":2}
  ],
  "stepPaths": [
    [{"latitude":1,"longitude":1},{"latitude":2,"longitude":2}],
    [{"latitude":3,"longitude":3}],
    [{"latitude":4,"longitude":4},{"latitude":5,"longitude":5},{"latitude":6,"longitude":6}]
  ]
}`

func TestResolveRouteWalksIndexes(t *testing.T) {
	var resp DirectionsResponse
	if err := json.Unmarshal([]byte(twoRouteDirections), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	t.Run("first route", func(t *testing.T) {
		steps, err := resp.ResolveRoute(0)
		if err != nil {
			t.Fatalf("ResolveRoute: %v", err)
		}
		if len(steps) != 2 {
			t.Fatalf("steps: got %d, want 2", len(steps))
		}
		if steps[0].Step.Instructions != "Head north" {
			t.Errorf("first instruction: got %q", steps[0].Step.Instructions)
		}
		if len(steps[0].Path) != 2 {
			t.Errorf("first path: got %d points, want 2", len(steps[0].Path))
		}
		if steps[1].Step.Instructions != "Turn left" {
			t.Errorf("second instruction: got %q", steps[1].Step.Instructions)
		}
		if len(steps[1].Path) != 1 {
			t.Errorf("second path: got %d points, want 1", len(steps[1].Path))
		}
	})

	t.Run("second route reaches a different step", func(t *testing.T) {
		steps, err := resp.ResolveRoute(1)
		if err != nil {
			t.Fatalf("ResolveRoute: %v", err)
		}
		if len(steps) != 1 {
			t.Fatalf("steps: got %d, want 1", len(steps))
		}
		if steps[0].Step.Instructions != "Take the scenic road" {
			t.Errorf("instruction: got %q", steps[0].Step.Instructions)
		}
		if len(steps[0].Path) != 3 {
			t.Errorf("path: got %d points, want 3", len(steps[0].Path))
		}
	})
}

// These are the cases that would panic and take the process down if the indexes
// were trusted. All of them arrive over the network, so none can be assumed
// well-formed.
func TestResolveRouteRejectsOutOfRangeIndexes(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		routeIndex int
		wantErrIs  string
	}{
		{
			name:       "route index too large",
			body:       `{"routes":[{"stepIndexes":[0]}],"steps":[{}]}`,
			routeIndex: 5,
			wantErrIs:  "route index 5 out of range",
		},
		{
			name:       "negative route index",
			body:       `{"routes":[{"stepIndexes":[0]}],"steps":[{}]}`,
			routeIndex: -1,
			wantErrIs:  "route index -1 out of range",
		},
		{
			name:       "no routes at all",
			body:       `{"routes":[]}`,
			routeIndex: 0,
			wantErrIs:  "route index 0 out of range",
		},
		{
			name:       "step index beyond the steps array",
			body:       `{"routes":[{"stepIndexes":[0,7]}],"steps":[{"instructions":"only one"}]}`,
			routeIndex: 0,
			wantErrIs:  "step index 7 out of range",
		},
		{
			name:       "negative step index",
			body:       `{"routes":[{"stepIndexes":[-2]}],"steps":[{}]}`,
			routeIndex: 0,
			wantErrIs:  "step index -2 out of range",
		},
		{
			name:       "steps array missing entirely",
			body:       `{"routes":[{"stepIndexes":[0]}]}`,
			routeIndex: 0,
			wantErrIs:  "step index 0 out of range",
		},
		{
			name:       "step path index beyond the stepPaths array",
			body:       `{"routes":[{"stepIndexes":[0]}],"steps":[{"stepPathIndex":9}],"stepPaths":[[]]}`,
			routeIndex: 0,
			wantErrIs:  "step path index 9 out of range",
		},
		{
			name:       "negative step path index",
			body:       `{"routes":[{"stepIndexes":[0]}],"steps":[{"stepPathIndex":-1}],"stepPaths":[[]]}`,
			routeIndex: 0,
			wantErrIs:  "step path index -1 out of range",
		},
		{
			name:       "stepPaths missing entirely",
			body:       `{"routes":[{"stepIndexes":[0]}],"steps":[{"stepPathIndex":0}]}`,
			routeIndex: 0,
			wantErrIs:  "step path index 0 out of range",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var resp DirectionsResponse
			if err := json.Unmarshal([]byte(tc.body), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}

			// An explicit guard: a panic here is the exact failure this test
			// exists to prevent, and without recovering it the message would be
			// a stack trace rather than a named test failure.
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("ResolveRoute panicked instead of erroring: %v", p)
				}
			}()

			_, err := resp.ResolveRoute(tc.routeIndex)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.wantErrIs) {
				t.Errorf("error: got %q, want it to mention %q", err.Error(), tc.wantErrIs)
			}
		})
	}
}

// Apple marks stepPathIndex optional, so its absence is normal rather than
// malformed and must not be an error.
func TestResolveRouteAllowsMissingStepPathIndex(t *testing.T) {
	const body = `{"routes":[{"stepIndexes":[0]}],"steps":[{"instructions":"no path"}],"stepPaths":[]}`
	var resp DirectionsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	steps, err := resp.ResolveRoute(0)
	if err != nil {
		t.Fatalf("ResolveRoute: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("steps: got %d, want 1", len(steps))
	}
	if steps[0].Path != nil {
		t.Errorf("path: got %v, want nil", steps[0].Path)
	}
}

func TestResolveRouteEmptyStepIndexes(t *testing.T) {
	var resp DirectionsResponse
	if err := json.Unmarshal([]byte(`{"routes":[{"name":"empty"}],"steps":[{}]}`), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	steps, err := resp.ResolveRoute(0)
	if err != nil {
		t.Fatalf("ResolveRoute: %v", err)
	}
	if len(steps) != 0 {
		t.Errorf("steps: got %d, want 0", len(steps))
	}
}

func TestDirectionsDecodesRouteMetadata(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, twoRouteDirections)
	})

	resp, err := client.Directions(context.Background(), DirectionsRequest{Origin: "a", Destination: "b"})
	if err != nil {
		t.Fatalf("Directions: %v", err)
	}

	if len(resp.Routes) != 2 {
		t.Fatalf("routes: got %d, want 2", len(resp.Routes))
	}
	if resp.Origin == nil || resp.Origin.Name != "Start" {
		t.Error("origin did not decode")
	}
	if resp.Destination == nil || resp.Destination.Name != "End" {
		t.Error("destination did not decode")
	}

	fast := resp.Routes[0]
	if fast.HasTolls == nil || !*fast.HasTolls {
		t.Error("first route should have tolls")
	}
	scenic := resp.Routes[1]
	if scenic.HasTolls == nil || *scenic.HasTolls {
		t.Error("second route should be explicitly toll-free, not undefined")
	}
	if fast.DistanceMeters == nil || *fast.DistanceMeters != 1000 {
		t.Errorf("distanceMeters: got %v", fast.DistanceMeters)
	}
}
