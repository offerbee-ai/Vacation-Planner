package applemaps

import (
	"encoding/json"
	"testing"
)

// The payloads below are Apple's own documented examples, copied verbatim, so a
// decode failure here means our structs disagree with the published schema
// rather than with a guess.

const eiffelTowerSearchResponse = `{
  "displayMapRegion": {
    "southLatitude": 48.856909736059606,
    "westLongitude": 2.2924737352877855,
    "northLatitude": 48.85963364504278,
    "eastLongitude": 2.2965897526592016
  },
  "results": [
    {
      "name": "Eiffel Tower",
      "formattedAddressLines": ["5 Avenue Anatole France", "75007 Paris", "France"],
      "structuredAddress": {
        "administrativeArea": "Île-de-France",
        "locality": "Paris",
        "postCode": "75007",
        "subLocality": "Tour Eiffel-Champs de Mars",
        "thoroughfare": "Avenue Anatole France",
        "subThoroughfare": "5",
        "fullThoroughfare": "5 Avenue Anatole France",
        "areasOfInterest": ["Eiffel Tower", "Parc Du Champ De Mars"],
        "dependentLocalities": ["7th arr.", "Tour Eiffel-Champs de Mars"]
      },
      "country": "France",
      "countryCode": "FR",
      "coordinate": {"latitude": 48.85827172505176, "longitude": 2.294531782785587},
      "poiCategory": "Landmark"
    }
  ]
}`

func TestSearchResponseDecodesAppleExample(t *testing.T) {
	var got SearchResponse
	if err := json.Unmarshal([]byte(eiffelTowerSearchResponse), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(got.Results) != 1 {
		t.Fatalf("results: got %d, want 1", len(got.Results))
	}
	place := got.Results[0]

	// Promoted fields from the embedded Place must survive the flattened JSON.
	if place.Name != "Eiffel Tower" {
		t.Errorf("name: got %q, want %q", place.Name, "Eiffel Tower")
	}
	if place.Coordinate.Latitude != 48.85827172505176 {
		t.Errorf("latitude: got %v", place.Coordinate.Latitude)
	}
	if place.CountryCode != "FR" {
		t.Errorf("countryCode: got %q", place.CountryCode)
	}
	if place.PoiCategory != PoiCategoryLandmark {
		t.Errorf("poiCategory: got %q, want %q", place.PoiCategory, PoiCategoryLandmark)
	}
	if got := len(place.FormattedAddressLines); got != 3 {
		t.Errorf("formattedAddressLines: got %d, want 3", got)
	}
	if place.StructuredAddress == nil {
		t.Fatal("structuredAddress: got nil")
	}
	if place.StructuredAddress.Locality != "Paris" {
		t.Errorf("locality: got %q", place.StructuredAddress.Locality)
	}
	if got := len(place.StructuredAddress.DependentLocalities); got != 2 {
		t.Errorf("dependentLocalities: got %d, want 2", got)
	}
	if got.DisplayMapRegion == nil {
		t.Fatal("displayMapRegion: got nil")
	}
	if got.DisplayMapRegion.NorthLatitude != 48.85963364504278 {
		t.Errorf("northLatitude: got %v", got.DisplayMapRegion.NorthLatitude)
	}

	// Apple omits paginationInfo when a response is not paginated. That must
	// stay distinguishable from a present-but-empty one, since SearchAll
	// terminates on the absence of a next page token.
	if got.PaginationInfo != nil {
		t.Errorf("paginationInfo: got %+v, want nil when absent", got.PaginationInfo)
	}
}

const whiteHouseGeocodeResponse = `{
  "results": [
    {
      "coordinate": {"latitude": 38.8976635, "longitude": -77.036574},
      "displayMapRegion": {
        "southLatitude": 38.8931719235794,
        "westLongitude": -77.04234524082925,
        "northLatitude": 38.9021550764206,
        "eastLongitude": -77.03080275917075
      },
      "name": "1600 Pennsylvania Ave NW",
      "formattedAddressLines": ["1600 Pennsylvania Ave NW", "Washington, DC  20500", "United States"],
      "structuredAddress": {
        "administrativeArea": "District of Columbia",
        "administrativeAreaCode": "DC",
        "locality": "Washington",
        "postCode": "20500",
        "subLocality": "Washington Mall",
        "thoroughfare": "Pennsylvania Ave NW",
        "subThoroughfare": "1600",
        "fullThoroughfare": "1600 Pennsylvania Ave NW",
        "areasOfInterest": ["The White House", "President's Park"],
        "dependentLocalities": ["Washington Mall"]
      },
      "country": "United States",
      "countryCode": "US"
    }
  ]
}`

func TestPlaceResultsDecodesAppleExample(t *testing.T) {
	var got PlaceResults
	if err := json.Unmarshal([]byte(whiteHouseGeocodeResponse), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Results) != 1 {
		t.Fatalf("results: got %d, want 1", len(got.Results))
	}
	place := got.Results[0]
	if place.Coordinate.Longitude != -77.036574 {
		t.Errorf("longitude: got %v", place.Coordinate.Longitude)
	}
	if place.StructuredAddress.AdministrativeAreaCode != "DC" {
		t.Errorf("administrativeAreaCode: got %q", place.StructuredAddress.AdministrativeAreaCode)
	}
}

func TestTokenResponseDecodes(t *testing.T) {
	var got TokenResponse
	if err := json.Unmarshal([]byte(`{"accessToken":"abc.def.ghi","expiresInSeconds":1800}`), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AccessToken != "abc.def.ghi" {
		t.Errorf("accessToken: got %q", got.AccessToken)
	}
	if got.ExpiresInSeconds != 1800 {
		t.Errorf("expiresInSeconds: got %d, want 1800", got.ExpiresInSeconds)
	}
}

// A distance of zero and a toll-free route are real answers. If these fields
// were plain values rather than pointers, both would be indistinguishable from
// Apple having omitted them, and a caller could not tell "0 metres away" from
// "no distance reported".
func TestZeroValuesStayDistinguishableFromAbsentFields(t *testing.T) {
	t.Run("present and zero", func(t *testing.T) {
		var got EtaResponse
		if err := json.Unmarshal([]byte(`{"etas":[{"distanceMeters":0,"expectedTravelTimeSeconds":0}]}`), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		eta := got.ETAs[0]
		if eta.DistanceMeters == nil {
			t.Fatal("distanceMeters: got nil, want pointer to 0")
		}
		if *eta.DistanceMeters != 0 {
			t.Errorf("distanceMeters: got %d, want 0", *eta.DistanceMeters)
		}
	})

	t.Run("absent", func(t *testing.T) {
		var got EtaResponse
		if err := json.Unmarshal([]byte(`{"etas":[{}]}`), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.ETAs[0].DistanceMeters != nil {
			t.Error("distanceMeters: got non-nil, want nil when absent")
		}
	})

	t.Run("hasTolls false versus undefined", func(t *testing.T) {
		var explicit DirectionsResponse
		if err := json.Unmarshal([]byte(`{"routes":[{"hasTolls":false}]}`), &explicit); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if explicit.Routes[0].HasTolls == nil {
			t.Fatal("hasTolls: got nil, want pointer to false")
		}
		if *explicit.Routes[0].HasTolls {
			t.Error("hasTolls: got true, want false")
		}

		var undefined DirectionsResponse
		if err := json.Unmarshal([]byte(`{"routes":[{}]}`), &undefined); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if undefined.Routes[0].HasTolls != nil {
			t.Error("hasTolls: got non-nil, want nil when Apple leaves it undefined")
		}
	})
}

// Apple describes each step path as "a single polyline represented as an array
// of points", which makes stepPaths a list of polylines rather than a flat list
// of points. Its machine-readable schema says otherwise; this pins the prose
// reading that types.go documents.
func TestStepPathsDecodeAsPolylines(t *testing.T) {
	const body = `{"stepPaths":[[{"latitude":1,"longitude":2},{"latitude":3,"longitude":4}],[{"latitude":5,"longitude":6}]]}`
	var got DirectionsResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.StepPaths) != 2 {
		t.Fatalf("stepPaths: got %d polylines, want 2", len(got.StepPaths))
	}
	if len(got.StepPaths[0]) != 2 {
		t.Errorf("first polyline: got %d points, want 2", len(got.StepPaths[0]))
	}
	if got.StepPaths[1][0].Latitude != 5 {
		t.Errorf("second polyline first point: got %v, want 5", got.StepPaths[1][0].Latitude)
	}
}

// /v1/searchAutocomplete spells a coordinate "lat"/"lng" while every other
// endpoint spells it "latitude"/"longitude". Apple documents one Location type and
// never mentions the second spelling; only that endpoint's example response shows
// it. Decoding just the documented form is silently wrong rather than an error,
// because encoding/json drops unknown keys — the coordinate becomes (0, 0), which
// is a real point off the coast of Ghana.
func TestLocationDecodesBothWireSpellings(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantLat  float64
		wantLong float64
	}{
		{
			name:     "documented spelling",
			body:     `{"latitude":48.858,"longitude":2.294}`,
			wantLat:  48.858,
			wantLong: 2.294,
		},
		{
			name:     "searchAutocomplete spelling",
			body:     `{"lat":37.785743713378906,"lng":-122.40109252929688}`,
			wantLat:  37.785743713378906,
			wantLong: -122.40109252929688,
		},
		{
			// A coordinate legitimately at zero must not read as an absent key and
			// fall through to the other spelling.
			name:     "explicit zero in the documented spelling",
			body:     `{"latitude":0,"longitude":0,"lat":1,"lng":1}`,
			wantLat:  0,
			wantLong: 0,
		},
		{
			name:     "documented spelling wins when both are present",
			body:     `{"latitude":48.858,"longitude":2.294,"lat":1,"lng":1}`,
			wantLat:  48.858,
			wantLong: 2.294,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Location
			if err := json.Unmarshal([]byte(tt.body), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Latitude != tt.wantLat || got.Longitude != tt.wantLong {
				t.Errorf("got %+v, want {%v %v}", got, tt.wantLat, tt.wantLong)
			}
		})
	}
}

// The tolerant decoder must not disturb the endpoints that nest a Location, since
// those all use the documented spelling.
func TestNestedLocationsStillDecode(t *testing.T) {
	const body = `{
		"etas":[{"destination":{"latitude":37.32,"longitude":-121.94},"distanceMeters":1200}]
	}`
	var got EtaResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.ETAs) != 1 || got.ETAs[0].Destination == nil {
		t.Fatalf("etas did not decode: %+v", got)
	}
	if got.ETAs[0].Destination.Latitude != 37.32 {
		t.Errorf("destination latitude: got %v, want 37.32", got.ETAs[0].Destination.Latitude)
	}
}

// Apple's StructuredAddress schema lists ten fields; live responses carry
// subAdministrativeArea as an eleventh.
func TestStructuredAddressDecodesSubAdministrativeArea(t *testing.T) {
	const body = `{"administrativeArea":"California","subAdministrativeArea":"San Francisco County","locality":"San Francisco"}`
	var got StructuredAddress
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SubAdministrativeArea != "San Francisco County" {
		t.Errorf("subAdministrativeArea: got %q, want %q", got.SubAdministrativeArea, "San Francisco County")
	}
}

func TestPlacesResponseSurfacesPartialFailure(t *testing.T) {
	const body = `{"results":[{"id":"good","name":"Somewhere"}],"errors":[{"id":"bad","errorCode":"NOT_FOUND"}]}`
	var got PlacesResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Results) != 1 || len(got.Errors) != 1 {
		t.Fatalf("got %d results and %d errors, want 1 and 1", len(got.Results), len(got.Errors))
	}
	if got.Errors[0].ErrorCode != "NOT_FOUND" {
		t.Errorf("errorCode: got %q", got.Errors[0].ErrorCode)
	}
}

func TestAllPoiCategoriesIsCompleteAndUnique(t *testing.T) {
	// Apple's PoiCategory reference lists 77 categories.
	const want = 77
	if got := len(AllPoiCategories); got != want {
		t.Errorf("AllPoiCategories: got %d, want %d", got, want)
	}

	seen := make(map[PoiCategory]bool, len(AllPoiCategories))
	for _, c := range AllPoiCategories {
		if seen[c] {
			t.Errorf("duplicate category %q", c)
		}
		seen[c] = true
		if c == "" {
			t.Error("empty category in AllPoiCategories")
		}
	}
}

func TestPoiCategoryValid(t *testing.T) {
	if !PoiCategoryStore.Valid() {
		t.Error("PoiCategoryStore should be valid")
	}
	if PoiCategory("Supermarket").Valid() {
		t.Error(`"Supermarket" is not an Apple category and must not validate`)
	}
	if PoiCategory("").Valid() {
		t.Error("empty category must not validate")
	}
}
