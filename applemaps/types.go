package applemaps

import "encoding/json"

// Apple documents every response field as optional. Where a Go zero value would
// be indistinguishable from an absent field AND the zero value is itself
// meaningful — a distance of 0 metres, a route with no tolls — the field is a
// pointer. Where the zero value is not meaningful (an empty name, an empty
// slice) a plain type is used, because collapsing "absent" into "empty" loses
// nothing a caller could act on.

// Location describes a point in terms of its latitude and longitude.
type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// UnmarshalJSON accepts both spellings Apple uses for a coordinate.
//
// Every endpoint sends "latitude"/"longitude" except /v1/searchAutocomplete, whose
// location object is {"lat":...,"lng":...}. Apple's schema documents one Location
// type and gives no hint of the second spelling; only the endpoint's own example
// response shows it.
//
// Decoding just the documented spelling is silently wrong rather than an error:
// unknown keys are ignored, so every suggestion's coordinate becomes a non-nil
// (0, 0) — a real point in the Gulf of Guinea that a caller cannot tell apart from
// an answer. Accepting either spelling here keeps that failure out of every caller,
// and costs nothing on the endpoints that use the long form.
//
// There is no matching MarshalJSON. Only decoding is tolerant; the package never
// sends a Location as a JSON body, and emitting whichever spelling was last read
// would be worse than emitting the documented one.
func (l *Location) UnmarshalJSON(data []byte) error {
	// Pointers distinguish an absent key from a present zero, so a coordinate
	// legitimately at 0 does not read as missing and trigger the fallback.
	var wire struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
		Lat       *float64 `json:"lat"`
		Lng       *float64 `json:"lng"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	// The documented spelling wins if a response somehow carries both.
	switch {
	case wire.Latitude != nil:
		l.Latitude = *wire.Latitude
	case wire.Lat != nil:
		l.Latitude = *wire.Lat
	}
	switch {
	case wire.Longitude != nil:
		l.Longitude = *wire.Longitude
	case wire.Lng != nil:
		l.Longitude = *wire.Lng
	}
	return nil
}

// MapRegion is a rectangular region expressed as its south-west and north-east
// corners.
type MapRegion struct {
	NorthLatitude float64 `json:"northLatitude"`
	EastLongitude float64 `json:"eastLongitude"`
	SouthLatitude float64 `json:"southLatitude"`
	WestLongitude float64 `json:"westLongitude"`
}

// SearchMapRegion is Apple's name for the region a search response echoes back.
// Apple documents it as a separate object, but its fields are identical to
// MapRegion, so it is an alias rather than a duplicate declaration.
type SearchMapRegion = MapRegion

// StructuredAddress describes the individual components of a place's address.
//
// SubAdministrativeArea is absent from Apple's published StructuredAddress schema,
// which lists ten fields. Live responses carry eleven: Apple's own
// /v1/searchAutocomplete example returns "subAdministrativeArea":"San Francisco
// County" on most of its results. Omitting it here discarded the county silently,
// since encoding/json drops unknown keys without complaint.
type StructuredAddress struct {
	AdministrativeArea     string   `json:"administrativeArea,omitempty"`
	AdministrativeAreaCode string   `json:"administrativeAreaCode,omitempty"`
	SubAdministrativeArea  string   `json:"subAdministrativeArea,omitempty"`
	AreasOfInterest        []string `json:"areasOfInterest,omitempty"`
	DependentLocalities    []string `json:"dependentLocalities,omitempty"`
	FullThoroughfare       string   `json:"fullThoroughfare,omitempty"`
	Locality               string   `json:"locality,omitempty"`
	PostCode               string   `json:"postCode,omitempty"`
	SubLocality            string   `json:"subLocality,omitempty"`
	SubThoroughfare        string   `json:"subThoroughfare,omitempty"`
	Thoroughfare           string   `json:"thoroughfare,omitempty"`
}

// Place describes a place in terms of its spatial and administrative
// properties.
//
// Apple exposes no photo, opening hours, rating, review count, price level, or
// business status on this object, and there is no endpoint or parameter that
// adds them. Callers needing those fields must source them elsewhere.
type Place struct {
	ID                    string             `json:"id,omitempty"`
	Name                  string             `json:"name,omitempty"`
	Coordinate            Location           `json:"coordinate"`
	FormattedAddressLines []string           `json:"formattedAddressLines,omitempty"`
	StructuredAddress     *StructuredAddress `json:"structuredAddress,omitempty"`
	Country               string             `json:"country,omitempty"`
	CountryCode           string             `json:"countryCode,omitempty"`
	DisplayMapRegion      *MapRegion         `json:"displayMapRegion,omitempty"`
	AlternateIDs          []string           `json:"alternateIds,omitempty"`
}

// SearchPlace is a Place as returned by the search endpoints, which add a
// category. Apple names this type SearchResponse.Place.
//
// PoiCategory comes from a fixed enum whose entire retail branch is
// PoiCategoryStore, so it cannot distinguish a supermarket from a shopping mall
// from a clothing store. Callers needing that granularity should carry it in the
// search query rather than reading it back off this field.
type SearchPlace struct {
	Place
	PoiCategory PoiCategory `json:"poiCategory,omitempty"`
}

// PaginationInfo carries the tokens and totals for a paginated search. Apple
// names this type SearchResponse.PaginationInfo.
type PaginationInfo struct {
	NextPageToken  string `json:"nextPageToken,omitempty"`
	PrevPageToken  string `json:"prevPageToken,omitempty"`
	TotalPageCount int    `json:"totalPageCount,omitempty"`
	TotalResults   int    `json:"totalResults,omitempty"`
}

// SearchResponse is the response from /v1/search.
type SearchResponse struct {
	DisplayMapRegion *SearchMapRegion `json:"displayMapRegion,omitempty"`
	Results          []SearchPlace    `json:"results,omitempty"`
	PaginationInfo   *PaginationInfo  `json:"paginationInfo,omitempty"`
}

// PlaceResults is the response from /v1/geocode and /v1/reverseGeocode.
type PlaceResults struct {
	Results []Place `json:"results,omitempty"`
}

// PlaceLookupError reports a single failed ID within an otherwise successful
// batch lookup. Apple names this type PlacesResponse.PlaceLookupError.
type PlaceLookupError struct {
	ID        string `json:"id,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
}

// PlacesResponse is the response from /v1/place. A batch lookup can partially
// succeed, populating both Results and Errors; neither field implies the other
// is empty.
type PlacesResponse struct {
	Results []Place            `json:"results,omitempty"`
	Errors  []PlaceLookupError `json:"errors,omitempty"`
}

// AlternateIDs lists the alternate place IDs for one place ID. Apple names this
// type AlternateIdsResponse.AlternateIds.
type AlternateIDs struct {
	ID           string   `json:"id,omitempty"`
	AlternateIDs []string `json:"alternateIds,omitempty"`
}

// AlternateIDsResponse is the response from /v1/place/alternateIds. Like
// PlacesResponse, it can partially succeed.
type AlternateIDsResponse struct {
	Results []AlternateIDs     `json:"results,omitempty"`
	Errors  []PlaceLookupError `json:"errors,omitempty"`
}

// TokenResponse is the response from /v1/token. ExpiresInSeconds is observed to
// be 1800.
type TokenResponse struct {
	AccessToken      string `json:"accessToken"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
}

// AutocompleteResult is a single suggestion from /v1/searchAutocomplete.
//
// CompletionURL is a relative URI into the search endpoint carrying opaque
// metadata about the suggestion; to resolve a suggestion in a specific language,
// the lang parameter must be set on the original autocomplete request rather
// than added to this URL.
type AutocompleteResult struct {
	CompletionURL     string             `json:"completionUrl,omitempty"`
	DisplayLines      []string           `json:"displayLines,omitempty"`
	Location          *Location          `json:"location,omitempty"`
	StructuredAddress *StructuredAddress `json:"structuredAddress,omitempty"`
}

// SearchAutocompleteResponse is the response from /v1/searchAutocomplete.
type SearchAutocompleteResponse struct {
	Results []AutocompleteResult `json:"results,omitempty"`
}

// Eta is an estimated time of arrival for one destination. Apple names this type
// EtaResponse.Eta.
//
// The three numeric fields are pointers because zero is a legitimate value —
// a destination at the origin has a distance of 0 — and Apple marks them
// optional, so a plain int could not distinguish the two.
type Eta struct {
	Destination               *Location     `json:"destination,omitempty"`
	DistanceMeters            *int          `json:"distanceMeters,omitempty"`
	ExpectedTravelTimeSeconds *int          `json:"expectedTravelTimeSeconds,omitempty"`
	StaticTravelTimeSeconds   *int          `json:"staticTravelTimeSeconds,omitempty"`
	TransportType             TransportType `json:"transportType,omitempty"`
}

// EtaResponse is the response from /v1/etas.
type EtaResponse struct {
	ETAs []Eta `json:"etas,omitempty"`
}

// Route is one route within a DirectionsResponse. Apple names this type
// DirectionsResponse.Route.
//
// StepIndexes are indexes into DirectionsResponse.Steps, not steps themselves.
// Use DirectionsResponse.ResolveRoute to walk them safely; indexing directly
// panics on a malformed response.
//
// HasTolls is a pointer because Apple documents three states: true, false, and
// undefined meaning the route may or may not have tolls.
type Route struct {
	Name            string        `json:"name,omitempty"`
	DistanceMeters  *int          `json:"distanceMeters,omitempty"`
	DurationSeconds *int          `json:"durationSeconds,omitempty"`
	HasTolls        *bool         `json:"hasTolls,omitempty"`
	StepIndexes     []int         `json:"stepIndexes,omitempty"`
	TransportType   TransportType `json:"transportType,omitempty"`
}

// Step is one step within a DirectionsResponse. Apple names this type
// DirectionsResponse.Step.
//
// StepPathIndex is an index into DirectionsResponse.StepPaths, not a path.
// TransportType is set only when it differs from the containing route's.
type Step struct {
	DistanceMeters  *int          `json:"distanceMeters,omitempty"`
	DurationSeconds *int          `json:"durationSeconds,omitempty"`
	Instructions    string        `json:"instructions,omitempty"`
	StepPathIndex   *int          `json:"stepPathIndex,omitempty"`
	TransportType   TransportType `json:"transportType,omitempty"`
}

// DirectionsResponse is the response from /v1/directions.
//
// The shape is flattened rather than nested: Steps and StepPaths are global
// across every route, and a route reaches its steps through Route.StepIndexes
// while a step reaches its path through Step.StepPathIndex. ResolveRoute walks
// those indexes with bounds checks.
//
// StepPaths is a slice of polylines, each polyline a slice of points. Apple's
// machine-readable schema annotates the field as a flat array of Location, which
// contradicts its own prose description ("each step path is a single polyline
// represented as an array of points"). A live response settles it in favour of
// the prose: the field arrives as [[{lat,lng},...],[{lat,lng},...]].
type DirectionsResponse struct {
	Origin      *Place       `json:"origin,omitempty"`
	Destination *Place       `json:"destination,omitempty"`
	Routes      []Route      `json:"routes,omitempty"`
	Steps       []Step       `json:"steps,omitempty"`
	StepPaths   [][]Location `json:"stepPaths,omitempty"`
}

// SearchResultType filters which kinds of result /v1/search returns.
type SearchResultType string

const (
	SearchResultTypePoi             SearchResultType = "poi"
	SearchResultTypeAddress         SearchResultType = "address"
	SearchResultTypePhysicalFeature SearchResultType = "physicalFeature"
	SearchResultTypePointOfInterest SearchResultType = "pointOfInterest"
)

// SearchACResultType filters which kinds of result /v1/searchAutocomplete
// returns. Unlike SearchResultType it has no address member.
type SearchACResultType string

const (
	SearchACResultTypePoi             SearchACResultType = "poi"
	SearchACResultTypePhysicalFeature SearchACResultType = "physicalFeature"
	SearchACResultTypePointOfInterest SearchACResultType = "pointOfInterest"
)

// AddressCategory narrows which address results a search returns. Using it
// requires SearchResultTypeAddress in the request's ResultTypeFilter.
//
// Apple's AddressCategory page renders six values as five, running the second into
// the first bullet: "Country: Countries and regions. AdministrativeArea The primary
// administrative divisions of countries or regions." The /v1/search parameter
// documentation settles it independently by using the missing value in its own
// example, excludeAddressCategories=Country,AdministrativeArea.
type AddressCategory string

const (
	AddressCategoryCountry               AddressCategory = "Country"
	AddressCategoryAdministrativeArea    AddressCategory = "AdministrativeArea"
	AddressCategorySubAdministrativeArea AddressCategory = "SubAdministrativeArea"
	AddressCategoryLocality              AddressCategory = "Locality"
	AddressCategorySubLocality           AddressCategory = "SubLocality"
	AddressCategoryPostalCode            AddressCategory = "PostalCode"
)

// SearchRegionPriority says how strongly a request's SearchRegion should be
// weighted. Apple accepts exactly two values, and rejects anything else with a 400.
type SearchRegionPriority string

const (
	// SearchRegionPriorityDefault treats the region as a hint, which is Apple's
	// behaviour when the parameter is absent.
	SearchRegionPriorityDefault SearchRegionPriority = "default"
	// SearchRegionPriorityRequired confines results to the region.
	SearchRegionPriorityRequired SearchRegionPriority = "required"
)

// DirectionsAvoid names a feature to avoid when routing. Tolls is the only value
// Apple defines.
type DirectionsAvoid string

const DirectionsAvoidTolls DirectionsAvoid = "Tolls"

// TransportType is a mode of transportation.
//
// Apple's documentation truncates the list of valid values mid-sentence ("which
// is one of:" followed by nothing), so these constants were established
// empirically against the live API instead: each was sent to /v1/etas and the
// response status recorded. Automobile, Walking, Transit, and Cycling are all
// accepted; "Bicycle" is rejected with HTTP 400 "transportType invalid", so
// Cycling is the spelling for that mode.
//
// Apple does not enumerate the accepted values in its 400 response, so extending
// this list means probing candidates one at a time.
//
// The set is not the same on both endpoints — see TransportTypeTransit.
type TransportType string

const (
	TransportTypeAutomobile TransportType = "Automobile"
	TransportTypeWalking    TransportType = "Walking"
	// TransportTypeTransit works on /v1/etas but not on /v1/directions, which
	// documents only Automobile, Walking, and Cycling. That matches MapKit, which
	// gives transit travel times but no transit turn-by-turn. Directions rejects
	// it locally rather than spending a call to learn the same thing.
	TransportTypeTransit TransportType = "Transit"
	TransportTypeCycling TransportType = "Cycling"
)

// AllTransportTypes lists every mode confirmed to be accepted by /v1/etas.
// Directions accepts all but TransportTypeTransit.
var AllTransportTypes = []TransportType{
	TransportTypeAutomobile,
	TransportTypeWalking,
	TransportTypeTransit,
	TransportTypeCycling,
}

// DirectionsTransportTypes lists the modes /v1/directions accepts.
var DirectionsTransportTypes = []TransportType{
	TransportTypeAutomobile,
	TransportTypeWalking,
	TransportTypeCycling,
}
