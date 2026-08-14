# Apple Maps Phase 2 (Geocoding) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route `Geocode` and `ReverseGeocode` to the Apple Maps Server API with automatic fallback to Google, leaving place search entirely on Google.

**Architecture:** `SearchClient`'s two address methods become a named `Geocoder` interface. `AppleMapsClient` implements it over the existing `applemaps` package. `AppleGeocodeRouter` implements the full `SearchClient` — Apple-with-fallback for geocoding, unconditional passthrough to Google for `NearbySearch`. `PoiSearcher` holds one `searcher SearchClient` field, so all three provider call sites route through a single seam. Apple is off by default.

**Tech Stack:** Go 1.24, `github.com/weihesdlegend/Vacation-planner/applemaps` (Phase 1, already merged), `go-redis`, `miniredis` for tests, `envconfig` for configuration.

**Design:** [2026-08-11-apple-maps-phase-2-geocoding-design.md](../specs/2026-08-11-apple-maps-phase-2-geocoding-design.md)

## Global Constraints

- No Apple-sourced **place** is ever written to Redis. Apple serves geocoding only; `NearbySearch` always goes to Google.
- The `applemaps` package must keep zero dependencies on this repository. `applemaps/package_test.go` enforces this — never import `iowrappers` or `POI` from it.
- No live Apple calls in tests. Every fixture is either an Apple published example or a synthetic payload matching a recorded probe shape.
- Apple credentials are read from the environment only. Never commit a `.p8`, a key body, or a token.
- `APPLE_MAPS_ENABLED` defaults to `false`. With Apple disabled, `PoiSearcher.searcher` is the `*MapsClient` itself and `AppleGeocodeRouter` is never constructed.
- Missing or invalid Apple credentials degrade to Google-only. They must never fail startup.
- Field mapping, verified live and not negotiable:
  - `GeocodeQuery.City` ← `structuredAddress.locality`
  - `GeocodeQuery.AdminAreaLevelOne` ← `structuredAddress.administrativeAreaCode`, falling back to `structuredAddress.administrativeArea` when the code is empty
  - `GeocodeQuery.Country` ← `Place.country` (long form)
  - **Never overwrite a caller's `GeocodeQuery` field with an empty Apple value.**
- Run `go build ./... && go vet ./... && go test ./...` before every commit.

## File Structure

| File | Responsibility |
|---|---|
| `iowrappers/maps_client.go` (modify) | Split `Geocoder` out of `SearchClient`; add `PlaceDetailsClient` |
| `iowrappers/poi_searcher.go` (modify) | One `searcher SearchClient` seam; `details` capability; new constructor signature |
| `iowrappers/apple_maps_client.go` (create) | `AppleMapsClient` — `applemaps.Client` → `Geocoder` |
| `iowrappers/apple_quota.go` (create) | `QuotaCounter` + counting `http.RoundTripper` |
| `iowrappers/apple_geocode_router.go` (create) | `AppleGeocodeRouter` — routing, fallback, logging |
| `planner/planner.go` (modify) | Pass config through `Init`; drop `GetMapsClient()` use |
| `main.go` (modify) | Apple env vars |

---

### Task 1: Split `Geocoder` out of `SearchClient`

Pure refactor. No behaviour change, no call site moves — `*MapsClient` already satisfies both.

**Files:**
- Modify: `iowrappers/maps_client.go:19-23`
- Test: `iowrappers/interfaces_test.go` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `Geocoder` (methods `Geocode(context.Context, *GeocodeQuery) (float64, float64, error)`, `ReverseGeocode(context.Context, float64, float64) (*GeocodeQuery, error)`); `SearchClient` now embeds `Geocoder` and adds `NearbySearch(context.Context, *PlaceSearchRequest) ([]POI.Place, error)`; `PlaceDetailsClient` (method `PlaceDetailedSearch(context.Context, string, []string) (maps.PlaceDetailsResult, error)`).

- [x] **Step 1: Write the failing test**

Create `iowrappers/interfaces_test.go`:

```go
package iowrappers

// Compile-time proof that the concrete Google client still satisfies every
// interface it is assigned to after the split. A break here is a build failure
// at the point of the mistake rather than at some distant call site.
var (
	_ Geocoder           = (*MapsClient)(nil)
	_ SearchClient       = (*MapsClient)(nil)
	_ PlaceDetailsClient = (*MapsClient)(nil)
)
```

- [x] **Step 2: Run test to verify it fails**

Run: `go vet ./iowrappers/`
Expected: FAIL — `undefined: Geocoder`, `undefined: PlaceDetailsClient`.

- [x] **Step 3: Write minimal implementation**

In `iowrappers/maps_client.go`, replace the `SearchClient` declaration:

```go
// Geocoder translates between textual locations and coordinates. It is the half
// of SearchClient that Apple Maps can serve: Apple's Place object carries no
// opening hours, rating, or price at any tier, so it cannot serve NearbySearch,
// but its address data is complete.
type Geocoder interface {
	Geocode(context.Context, *GeocodeQuery) (float64, float64, error)        // translate a textual location to latitude and longitude
	ReverseGeocode(context.Context, float64, float64) (*GeocodeQuery, error) // look up a textual location based on latitude and longitude
}

// SearchClient defines an interface of a client that performs location-based operations such as nearby search
type SearchClient interface {
	Geocoder
	NearbySearch(context.Context, *PlaceSearchRequest) ([]POI.Place, error) // search nearby places in a category around a central location
}

// PlaceDetailsClient buys the per-place detail record. It is separate from
// SearchClient because it is a capability with exactly one provider — Apple
// exposes no equivalent — and only the data migrations use it.
type PlaceDetailsClient interface {
	PlaceDetailedSearch(context.Context, string, []string) (maps.PlaceDetailsResult, error)
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go vet ./... && go test ./iowrappers/ ./planner/`
Expected: PASS, no call site changes required.

- [x] **Step 5: Commit**

```bash
git add iowrappers/maps_client.go iowrappers/interfaces_test.go
git commit -m "refactor(iowrappers): split Geocoder out of SearchClient"
```

---

### Task 2: Give `PoiSearcher` one search seam

Still no Apple. This makes the seam exist so later tasks plug into it, and closes the `GetMapsClient()` leak.

**Files:**
- Modify: `iowrappers/poi_searcher.go:39-42` (struct), `:69-83` (constructor and getter), `:139`, `:159`, `:348`
- Modify: `iowrappers/data_migrations.go:146`
- Modify: `planner/planner.go:184`, `:193-197`
- Modify: `planner/place_search_auth_test.go:31`, `planner/reclassify_buckets_dry_run_test.go:33`, `:98`
- Test: `iowrappers/poi_searcher_seam_test.go` (create)

**Interfaces:**
- Consumes: `Geocoder`, `SearchClient`, `PlaceDetailsClient` from Task 1.
- Produces: `CreatePoiSearcher(mapsApiKey string, redisUrl *url.URL, detailedSearchFields []string) *PoiSearcher`. `PoiSearcher.GetMapsClient()` is **deleted**. Field `PoiSearcher.searcher SearchClient` is the single provider seam that Task 5 replaces.

- [x] **Step 1: Write the failing test**

Create `iowrappers/poi_searcher_seam_test.go`:

```go
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
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./iowrappers/ -run 'TestPoiSearcher(RoutesGeocoding|HasNoGetMapsClient)' -v`
Expected: FAIL — `unknown field searcher in struct literal`.

- [x] **Step 3: Write minimal implementation**

In `iowrappers/poi_searcher.go`, replace the struct and constructor:

```go
type PoiSearcher struct {
	// searcher is the single provider seam. Every outbound Geocode,
	// ReverseGeocode, and NearbySearch goes through it, so routing a provider in
	// means replacing this one field and nothing else. It is the *MapsClient
	// itself unless Apple is enabled.
	searcher SearchClient
	// details buys per-place detail records. Separate from searcher because it is
	// a capability with one provider rather than a choice between providers, and
	// only the data migrations use it.
	details     PlaceDetailsClient
	redisClient *RedisClient
}

func CreatePoiSearcher(mapsApiKey string, redisUrl *url.URL, detailedSearchFields []string) *PoiSearcher {
	mapsClient := CreateMapsClient(mapsApiKey)
	redisClient := CreateRedisClient(redisUrl)

	// Both of these used to be poked in from outside after construction, which is
	// why PoiSearcher had to expose the concrete client at all. Doing it here
	// leaves nothing for callers to reach through for.
	mapsClient.SetCachedPlaceLookup(redisClient.CachedPlaces)
	if len(detailedSearchFields) > 0 {
		mapsClient.SetDetailedSearchFields(detailedSearchFields)
	}

	return &PoiSearcher{
		searcher:    mapsClient,
		details:     mapsClient,
		redisClient: redisClient,
	}
}

// SetSearchClient replaces the provider seam. Used by the Apple Maps wiring in
// main to wrap the Google client in a router; nothing else should call it.
func (s *PoiSearcher) SetSearchClient(client SearchClient) {
	s.searcher = client
}
```

Delete the `GetMapsClient` method entirely. Then update the three call sites in the same file — `s.mapsClient.Geocode` at line 139 and `s.mapsClient.ReverseGeocode` at line 159 become `s.searcher.…`, and `s.GetMapsClient().NearbySearch` at line 348 becomes `s.searcher.NearbySearch`.

In `iowrappers/data_migrations.go:146`, replace `mapsClient := s.GetMapsClient()` with `mapsClient := s.details`.

In `planner/planner.go`, read the config before constructing and drop the post-hoc poke:

```go
	var placeDetailsFields []string
	if v, exists := p.Configs["server:google_maps:detailed_search_fields"]; exists {
		placeDetailsFields = v.([]string)
	}

	// initialize poi searcher
	PoiSearcher := iowrappers.CreatePoiSearcher(mapsClientApiKey, redisURL, placeDetailsFields)
```

Keep the existing `p.Solver.Init(...)` block that follows, and delete the later
`p.Solver.Searcher.GetMapsClient().SetDetailedSearchFields(placeDetailsFields)`
line along with the `var placeDetailsFields []string` declaration that preceded
it. `placeDetailsFields` is still used by `CreatePhotoClient` below, so it must
stay in scope.

In all three test call sites, add the new argument: `iowrappers.CreatePoiSearcher("test-maps-api-key", redisURL, nil)`.

- [x] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS. Confirm no `GetMapsClient` remains: `grep -rn "GetMapsClient" --include="*.go" .` returns nothing.

- [x] **Step 5: Commit**

```bash
git add iowrappers/poi_searcher.go iowrappers/poi_searcher_seam_test.go iowrappers/data_migrations.go planner/
git commit -m "refactor(iowrappers): route every provider call through one PoiSearcher seam"
```

---

### Task 3: `AppleMapsClient` adapter

**Files:**
- Create: `iowrappers/apple_maps_client.go`
- Test: `iowrappers/apple_maps_client_test.go`

**Interfaces:**
- Consumes: `Geocoder`, `GeocodeQuery` from Tasks 1-2.
- Produces: `CreateAppleMapsClient(cfg AppleMapsConfig) (*AppleMapsClient, error)`; `AppleMapsConfig{TeamID, KeyID, PrivateKey string; BaseURL string; HTTPClient *http.Client}`; `*AppleMapsClient` satisfies `Geocoder`.

- [x] **Step 1: Write the failing test**

Create `iowrappers/apple_maps_client_test.go`:

```go
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
			fmt.Fprint(w, `{"accessToken":"test-token","expiresInSeconds":1800}`)
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
				fmt.Fprint(w, tc.body)
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
		fmt.Fprint(w, `{"results":[{"country":"Japan","countryCode":"JP",
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
				fmt.Fprint(w, `{"results":[{"country":"United States",
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
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./iowrappers/ -run TestApple -v`
Expected: FAIL — `undefined: CreateAppleMapsClient`, `undefined: AppleMapsConfig`.

- [x] **Step 3: Write minimal implementation**

Create `iowrappers/apple_maps_client.go`:

```go
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
```

Add to `iowrappers/interfaces_test.go`: `_ Geocoder = (*AppleMapsClient)(nil)`.

- [x] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go vet ./... && go test ./iowrappers/ -run TestApple -v`
Expected: PASS, all four tests including every subtest.

- [x] **Step 5: Commit**

```bash
git add iowrappers/apple_maps_client.go iowrappers/apple_maps_client_test.go iowrappers/interfaces_test.go
git commit -m "feat(iowrappers): Apple Maps geocoding adapter"
```

---

### Task 4: Quota counter

**Files:**
- Create: `iowrappers/apple_quota.go`
- Test: `test/redis_client_mocks/apple_quota_test.go`

**Interfaces:**
- Consumes: `RedisClient`.
- Produces: `NewQuotaCounter(redisClient *RedisClient, cfg QuotaConfig) *QuotaCounter`; `QuotaConfig{DailyLimit int; Threshold float64; ExternalAllowance int}`; methods `(*QuotaCounter).OverThreshold(ctx context.Context) bool` and `(*QuotaCounter).Transport(base http.RoundTripper) http.RoundTripper`; exported `AppleDailyCallQuota = 25000`.

- [x] **Step 1: Write the failing test**

Create `test/redis_client_mocks/apple_quota_test.go`:

```go
package redis_client_mocks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/weihesdlegend/Vacation-planner/iowrappers"
)

func newTestQuotaCounter(limit int, threshold float64, allowance int) *iowrappers.QuotaCounter {
	return iowrappers.NewQuotaCounter(RedisClient, iowrappers.QuotaConfig{
		DailyLimit:        limit,
		Threshold:         threshold,
		ExternalAllowance: allowance,
	})
}

// Apple charges for every HTTP round trip, not every logical geocode: a stale
// token costs a /v1/token exchange and a 5xx costs a retry. Counting logical
// operations would under-report by a factor that varies with the failure rate,
// worst exactly when the budget is tightest. So the counter sits in the
// transport.
func TestQuotaCounterCountsEveryRoundTrip(t *testing.T) {
	RedisMockSvr.FlushAll()
	counter := newTestQuotaCounter(100, 0.9, 0)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: counter.Transport(http.DefaultTransport)}
	for range 3 {
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		_ = resp.Body.Close()
	}

	if got := counter.Count(context.Background()); got != 3 {
		t.Errorf("count: got %d, want 3", got)
	}
}

func TestQuotaCounterThreshold(t *testing.T) {
	tests := []struct {
		name      string
		limit     int
		threshold float64
		allowance int
		spend     int
		want      bool
	}{
		{"well under", 100, 0.9, 0, 10, false},
		{"just under", 100, 0.9, 0, 89, false},
		{"at the threshold", 100, 0.9, 0, 90, true},
		// The 25,000 is shared with MapKit JS, so traffic this counter cannot see
		// still spends the budget. The allowance is charged before our own calls.
		{"allowance alone crosses it", 100, 0.9, 90, 0, true},
		{"allowance plus spend crosses it", 100, 0.9, 50, 40, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			RedisMockSvr.FlushAll()
			counter := newTestQuotaCounter(tc.limit, tc.threshold, tc.allowance)
			ctx := context.Background()
			for range tc.spend {
				counter.Record(ctx)
			}
			if got := counter.OverThreshold(ctx); got != tc.want {
				t.Errorf("OverThreshold: got %v, want %v", got, tc.want)
			}
		})
	}
}

// The counter is a cost guard, not a correctness guard. If Redis is unreachable
// the request must still be served rather than failing closed.
//
// This uses its own miniredis rather than closing the package-wide one, which
// every other test in this package shares — stopping and restarting that server
// would make this test's failure mode depend on execution order.
func TestQuotaCounterRedisFailureDoesNotBlock(t *testing.T) {
	deadSvr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	deadURL, err := url.Parse("redis://" + deadSvr.Addr())
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	deadClient := iowrappers.CreateRedisClient(deadURL)
	deadSvr.Close()

	counter := iowrappers.NewQuotaCounter(deadClient, iowrappers.QuotaConfig{
		DailyLimit: 100, Threshold: 0.9,
	})
	ctx := context.Background()

	if counter.OverThreshold(ctx) {
		t.Error("a Redis failure must not report the quota as exhausted")
	}
	// Record must also swallow the failure rather than panicking.
	counter.Record(ctx)
}

// A 48 hour expiry keeps yesterday's key readable for diagnosis without letting
// the keyspace grow without bound.
func TestQuotaCounterKeyExpires(t *testing.T) {
	RedisMockSvr.FlushAll()
	counter := newTestQuotaCounter(100, 0.9, 0)
	counter.Record(context.Background())

	ttl := RedisMockSvr.TTL(counter.Key())
	if ttl <= 0 {
		t.Fatalf("TTL: got %v, want a positive expiry", ttl)
	}
	if ttl.Hours() > 48 {
		t.Errorf("TTL: got %v, want at most 48h", ttl)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./test/redis_client_mocks/ -run TestQuota -v`
Expected: FAIL — `undefined: iowrappers.NewQuotaCounter`.

- [x] **Step 3: Write minimal implementation**

Create `iowrappers/apple_quota.go`:

```go
package iowrappers

import (
	"context"
	"net/http"
	"time"
)

// AppleDailyCallQuota is Apple's free daily service call allowance, per
// developer team. It is shared between the Maps Server API and MapKit JS.
const AppleDailyCallQuota = 25000

// appleQuotaKeyExpiry keeps the previous day's counter readable for diagnosis
// while bounding the keyspace.
const appleQuotaKeyExpiry = 48 * time.Hour

// QuotaConfig configures a QuotaCounter.
type QuotaConfig struct {
	// DailyLimit is the team's daily allowance. Zero means AppleDailyCallQuota.
	DailyLimit int
	// Threshold is the fraction of the allowance at which we stop using Apple.
	// Zero means 0.9. Stopping early is deliberate: once the quota is exhausted
	// Apple returns 429 on every endpoint including /v1/token, so waiting for the
	// cliff would fail the whole provider over at an unpredictable moment.
	Threshold float64
	// ExternalAllowance is how many calls per day are assumed to be spent by
	// other consumers on the same team, principally MapKit JS. This counter can
	// only ever observe our own traffic, so a threshold applied to our count
	// alone is an under-estimate of true consumption rather than a measure of it.
	ExternalAllowance int
}

// QuotaCounter tracks outbound Apple calls for the current UTC day.
type QuotaCounter struct {
	redisClient *RedisClient
	cfg         QuotaConfig

	// now is injectable so date rollover is testable.
	now func() time.Time
}

func NewQuotaCounter(redisClient *RedisClient, cfg QuotaConfig) *QuotaCounter {
	if cfg.DailyLimit <= 0 {
		cfg.DailyLimit = AppleDailyCallQuota
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = 0.9
	}
	return &QuotaCounter{redisClient: redisClient, cfg: cfg, now: time.Now}
}

// Key is the counter's Redis key for the current UTC day.
func (q *QuotaCounter) Key() string {
	return "applemaps:quota:" + q.now().UTC().Format("2006-01-02")
}

// Record increments today's counter. A Redis failure is logged and ignored: the
// counter is a cost guard, and failing a request over it would trade a small
// overspend for an outage.
func (q *QuotaCounter) Record(ctx context.Context) {
	key := q.Key()
	count, err := q.redisClient.client.Incr(ctx, key).Result()
	if err != nil {
		Logger.Debugw("applemaps quota: increment failed", "key", key, "error", err)
		return
	}
	// Only the first increment of the day needs the expiry set.
	if count == 1 {
		if err := q.redisClient.client.Expire(ctx, key, appleQuotaKeyExpiry).Err(); err != nil {
			Logger.Debugw("applemaps quota: expire failed", "key", key, "error", err)
		}
	}
}

// Count returns today's recorded call count, or 0 if Redis is unreachable.
func (q *QuotaCounter) Count(ctx context.Context) int64 {
	count, err := q.redisClient.client.Get(ctx, q.Key()).Int64()
	if err != nil {
		return 0
	}
	return count
}

// OverThreshold reports whether Apple should be skipped for this request. It
// returns false when Redis is unreachable, so a cache outage degrades to
// spending quota rather than to refusing to use the provider.
func (q *QuotaCounter) OverThreshold(ctx context.Context) bool {
	budget := float64(q.cfg.DailyLimit) * q.cfg.Threshold
	spent := float64(q.Count(ctx) + int64(q.cfg.ExternalAllowance))
	return spent >= budget
}

// quotaTransport increments the counter once per HTTP round trip.
type quotaTransport struct {
	base    http.RoundTripper
	counter *QuotaCounter
}

func (t *quotaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.counter.Record(req.Context())
	return t.base.RoundTrip(req)
}

// Transport wraps base so every request through it is counted, including token
// exchanges and retries — the two costs a per-search counter would miss.
func (q *QuotaCounter) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &quotaTransport{base: base, counter: q}
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go vet ./... && go test ./test/redis_client_mocks/ -run TestQuota -v`
Expected: PASS, all four tests including every threshold subtest.

- [x] **Step 5: Commit**

```bash
git add iowrappers/apple_quota.go test/redis_client_mocks/apple_quota_test.go
git commit -m "feat(iowrappers): Apple Maps daily quota counter"
```

---

### Task 5: `AppleGeocodeRouter`

**Files:**
- Create: `iowrappers/apple_geocode_router.go`
- Test: `iowrappers/apple_geocode_router_test.go`

**Interfaces:**
- Consumes: `Geocoder`, `SearchClient` (Task 1), `QuotaCounter` (Task 4), and the `stubSearchClient` test double defined in `iowrappers/poi_searcher_seam_test.go` (Task 2) — same package, so it is reused rather than redeclared.
- Produces: `NewAppleGeocodeRouter(apple Geocoder, google SearchClient, quota *QuotaCounter) *AppleGeocodeRouter`, satisfying `SearchClient`. The `stubGeocoder` test double defined here is reused by Task 6.

- [x] **Step 1: Write the failing test**

Create `iowrappers/apple_geocode_router_test.go`:

```go
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
	calls   int
	query   *GeocodeQuery
	lat     float64
	lng     float64
	err     error
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
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./iowrappers/ -run TestRouter -v`
Expected: FAIL — `undefined: NewAppleGeocodeRouter`.

- [x] **Step 3: Write minimal implementation**

Create `iowrappers/apple_geocode_router.go`:

```go
package iowrappers

import (
	"context"
	"errors"

	"github.com/weihesdlegend/Vacation-planner/POI"
	"github.com/weihesdlegend/Vacation-planner/applemaps"
)

// AppleGeocodeRouter serves geocoding from Apple and everything else from Google.
//
// The split is a property of this type rather than a rule spread across
// PoiSearcher: Apple can answer address questions completely, and cannot answer
// place questions at all, so NearbySearch has no routing decision to make.
type AppleGeocodeRouter struct {
	apple  Geocoder
	google SearchClient
	// quota may be nil, which disables the pre-emptive check.
	quota *QuotaCounter
}

func NewAppleGeocodeRouter(apple Geocoder, google SearchClient, quota *QuotaCounter) *AppleGeocodeRouter {
	return &AppleGeocodeRouter{apple: apple, google: google, quota: quota}
}

// useApple reports whether Apple should be tried, logging the reason when not.
func (r *AppleGeocodeRouter) useApple(ctx context.Context, method string) bool {
	if r.apple == nil {
		return false
	}
	if r.quota != nil && r.quota.OverThreshold(ctx) {
		Logger.Infow("applemaps: routing to Google", "method", method, "reason", "daily quota threshold reached")
		return false
	}
	return true
}

// logFallback records why an Apple attempt was abandoned. A fallback costs one
// Apple call plus one Google call, so a silent one hides real spend and makes
// the Apple hit rate unmeasurable.
func logFallback(method string, err error) {
	reason := "error"
	var quotaErr *applemaps.QuotaError
	var notFound *applemaps.NotFoundError
	switch {
	case errors.As(err, &quotaErr):
		reason = "quota exhausted"
	case errors.As(err, &notFound):
		reason = "no match"
	}
	Logger.Infow("applemaps: falling back to Google", "method", method, "reason", reason, "error", err)
}

func (r *AppleGeocodeRouter) Geocode(ctx context.Context, query *GeocodeQuery) (float64, float64, error) {
	if r.useApple(ctx, "Geocode") {
		// Apple's adapter mutates the query it is given, so it gets a copy: a
		// failed attempt must not leave half-corrected fields for Google.
		attempt := *query
		lat, lng, err := r.apple.Geocode(ctx, &attempt)
		if err == nil {
			*query = attempt
			return lat, lng, nil
		}
		logFallback("Geocode", err)
	}
	return r.google.Geocode(ctx, query)
}

func (r *AppleGeocodeRouter) ReverseGeocode(ctx context.Context, latitude, longitude float64) (*GeocodeQuery, error) {
	if r.useApple(ctx, "ReverseGeocode") {
		query, err := r.apple.ReverseGeocode(ctx, latitude, longitude)
		if err == nil {
			return query, nil
		}
		logFallback("ReverseGeocode", err)
	}
	return r.google.ReverseGeocode(ctx, latitude, longitude)
}

// NearbySearch always goes to Google. See the type comment.
func (r *AppleGeocodeRouter) NearbySearch(ctx context.Context, request *PlaceSearchRequest) ([]POI.Place, error) {
	return r.google.NearbySearch(ctx, request)
}
```

Add `_ SearchClient = (*AppleGeocodeRouter)(nil)` to `iowrappers/interfaces_test.go`.

- [x] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go vet ./... && go test ./iowrappers/ -run TestRouter -v`
Expected: PASS, all three tests including every fallback subtest.

- [x] **Step 5: Commit**

```bash
git add iowrappers/apple_geocode_router.go iowrappers/apple_geocode_router_test.go iowrappers/interfaces_test.go
git commit -m "feat(iowrappers): route geocoding to Apple with Google fallback"
```

---

### Task 6: Configuration and enablement

**Files:**
- Modify: `main.go:23-37` (Config struct), `:92-94` (Init call)
- Modify: `planner/planner.go:148` (Init signature), `:184`
- Test: `iowrappers/apple_wiring_test.go` (create)

**Interfaces:**
- Consumes: everything above, plus two test doubles already in the package — `stubSearchClient` from Task 2 and `appleTestKey` from Task 3's test file.
- Produces: `AppleMapsSettings{Enabled bool; TeamID, KeyID, PrivateKey string; QuotaThreshold float64; ExternalAllowance int}`; `(*PoiSearcher).EnableAppleMaps(settings AppleMapsSettings) error`; `planner.MyPlanner.Init` gains a final `appleMaps iowrappers.AppleMapsSettings` parameter.

- [x] **Step 1: Write the failing test**

Create `iowrappers/apple_wiring_test.go`:

```go
package iowrappers

import "testing"

// A bad key must degrade to Google-only, never take the service down. Credentials
// arrive from the environment and a deploy-time mistake is entirely possible.
func TestEnableAppleMapsWithBadCredentialsLeavesGoogleInPlace(t *testing.T) {
	google := &stubSearchClient{}
	s := &PoiSearcher{searcher: google}

	err := s.EnableAppleMaps(AppleMapsSettings{
		Enabled: true, TeamID: "T", KeyID: "K", PrivateKey: "not a key",
	})
	if err == nil {
		t.Fatal("want an error for an unparseable private key")
	}
	if s.searcher != SearchClient(google) {
		t.Error("a bad key must leave the Google client in place")
	}
}

// Disabled is the default, and the router must not be constructed at all — the
// zero configuration is Google-only with nothing extra in the request path.
func TestEnableAppleMapsDisabledIsANoOp(t *testing.T) {
	google := &stubSearchClient{}
	s := &PoiSearcher{searcher: google}

	if err := s.EnableAppleMaps(AppleMapsSettings{Enabled: false}); err != nil {
		t.Fatalf("EnableAppleMaps: %v", err)
	}
	if _, wrapped := s.searcher.(*AppleGeocodeRouter); wrapped {
		t.Error("Apple disabled must not wrap the searcher")
	}
}

func TestEnableAppleMapsWrapsTheSearcher(t *testing.T) {
	google := &stubSearchClient{}
	s := &PoiSearcher{searcher: google}

	if err := s.EnableAppleMaps(AppleMapsSettings{
		Enabled: true, TeamID: "TEAM123456", KeyID: "KEY7890123",
		PrivateKey: appleTestKey(t),
	}); err != nil {
		t.Fatalf("EnableAppleMaps: %v", err)
	}
	if _, wrapped := s.searcher.(*AppleGeocodeRouter); !wrapped {
		t.Error("want the searcher wrapped in an AppleGeocodeRouter")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./iowrappers/ -run TestEnableAppleMaps -v`
Expected: FAIL — `undefined: AppleMapsSettings`.

- [x] **Step 3: Write minimal implementation**

Append to `iowrappers/apple_geocode_router.go`:

```go
// AppleMapsSettings is the deployment-time configuration for Apple Maps.
type AppleMapsSettings struct {
	// Enabled is false by default. Nothing Apple-derived is requested or cached
	// until it is deliberately turned on, which is also what keeps the
	// unresolved question of Apple's caching terms off the merge path.
	Enabled bool
	// TeamID is the Apple Developer team ID, the JWT iss claim.
	TeamID string
	// KeyID is the MapKit key ID, the JWT kid header.
	KeyID string
	// PrivateKey is the .p8 contents, raw PEM or base64-encoded PEM.
	PrivateKey string
	// QuotaThreshold is the fraction of the daily allowance at which Apple stops
	// being used. Zero means the QuotaConfig default.
	QuotaThreshold float64
	// ExternalAllowance is the calls per day assumed spent by MapKit JS on the
	// same team, which this service cannot observe.
	ExternalAllowance int
}

// EnableAppleMaps routes geocoding through Apple, keeping Google as the
// fallback and as the only place-search provider.
//
// It returns an error rather than logging and continuing so the caller can
// decide, but the caller in main deliberately continues: a missing or malformed
// credential should cost the quota saving, not the service.
func (s *PoiSearcher) EnableAppleMaps(settings AppleMapsSettings) error {
	if !settings.Enabled {
		return nil
	}

	quota := NewQuotaCounter(s.redisClient, QuotaConfig{
		Threshold:         settings.QuotaThreshold,
		ExternalAllowance: settings.ExternalAllowance,
	})

	// The counter lives in the transport so token exchanges and retries are
	// counted alongside the calls a search makes directly.
	appleClient, err := CreateAppleMapsClient(AppleMapsConfig{
		TeamID:     settings.TeamID,
		KeyID:      settings.KeyID,
		PrivateKey: settings.PrivateKey,
		HTTPClient: &http.Client{
			Timeout:   15 * time.Second,
			Transport: quota.Transport(nil),
		},
	})
	if err != nil {
		return err
	}

	s.searcher = NewAppleGeocodeRouter(appleClient, s.searcher, quota)
	Logger.Infow("applemaps: geocoding enabled", "team", settings.TeamID)
	return nil
}
```

Add `"net/http"` and `"time"` to that file's imports.

In `main.go`, extend the `Config` struct:

```go
	AppleMaps struct {
		Enabled           bool    `envconfig:"APPLE_MAPS_ENABLED" default:"false"`
		TeamID            string  `envconfig:"APPLE_MAPS_TEAM_ID"`
		KeyID             string  `envconfig:"APPLE_MAPS_KEY_ID"`
		PrivateKey        string  `envconfig:"APPLE_MAPS_PRIVATE_KEY"`
		QuotaThreshold    float64 `envconfig:"APPLE_MAPS_QUOTA_THRESHOLD" default:"0.9"`
		ExternalAllowance int     `envconfig:"APPLE_MAPS_EXTERNAL_ALLOWANCE" default:"0"`
	}
```

Pass it through the `Init` call:

```go
	myPlanner.Init(conf.MapsClientApiKey, redisURL, conf.Redis.RedisStreamName,
		flattenConfig(configs), conf.GoogleOAuthClientID, conf.GoogleOAuthClientSecret,
		conf.Server.Domain, conf.GeonamesApiKey, conf.BlobBucketId,
		iowrappers.AppleMapsSettings{
			Enabled:           conf.AppleMaps.Enabled,
			TeamID:            conf.AppleMaps.TeamID,
			KeyID:             conf.AppleMaps.KeyID,
			PrivateKey:        conf.AppleMaps.PrivateKey,
			QuotaThreshold:    conf.AppleMaps.QuotaThreshold,
			ExternalAllowance: conf.AppleMaps.ExternalAllowance,
		})
```

In `planner/planner.go:148`, add the parameter to `Init`'s signature —
`appleMaps iowrappers.AppleMapsSettings` as the final argument — and enable it
immediately after `CreatePoiSearcher`:

```go
	PoiSearcher := iowrappers.CreatePoiSearcher(mapsClientApiKey, redisURL, placeDetailsFields)
	if err := PoiSearcher.EnableAppleMaps(appleMaps); err != nil {
		// Degrade to Google-only. A bad Apple credential costs the quota saving,
		// not the service.
		logger.Warnf("Apple Maps disabled: %v", err)
	}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS. Confirm the default is off: `APPLE_MAPS_ENABLED` unset means `EnableAppleMaps` returns immediately and no router is constructed.

- [x] **Step 5: Commit**

```bash
git add main.go planner/planner.go iowrappers/apple_geocode_router.go iowrappers/apple_wiring_test.go
git commit -m "feat: wire Apple Maps geocoding behind APPLE_MAPS_ENABLED"
```

---

## Verification

After Task 6, end to end:

1. `gofmt -l . && go build ./... && go vet ./... && go test ./...` — clean.
2. `go test -race -count=2 ./iowrappers/ ./test/redis_client_mocks/` — clean.
3. `grep -rn "GetMapsClient" --include="*.go" .` — no results.
4. Google-only path unchanged: run the service with `APPLE_MAPS_ENABLED` unset
   and confirm `POST /v1/nearby-places-by-category` returns places with populated
   `rating` and `hours`, exactly as before.
5. With credentials set and `APPLE_MAPS_ENABLED=true`, call
   `GET /v1/reverse-geocoding?latitude=37.3316851&longitude=-122.0300674` and
   confirm `{"city":"Cupertino","admin_area_level_one":"CA","country":"United States"}`
   — the values the live probe recorded — and that the log shows no fallback.
6. Repeat step 5 with a non-abbreviating country, latitude `48.8583701`
   longitude `2.2944813`, expecting
   `{"city":"Paris","admin_area_level_one":"Île-de-France","country":"France"}`.
   This is the case the `administrativeAreaCode` fallback exists for.
7. Confirm the quota key exists and counts more than the number of requests made,
   since the first request also pays for a token exchange:
   `redis-cli GET applemaps:quota:$(date -u +%F)`.
8. Set `APPLE_MAPS_QUOTA_THRESHOLD=0.0001` and confirm the next request logs
   `routing to Google` with reason `daily quota threshold reached` and still
   returns a correct answer.

## Out of scope

Recorded so they are not mistaken for omissions.

- Place search on Apple. Structurally blocked by missing opening hours with no
  join key to backfill them; see the design's scope section.
- A TTL on the `geocode:cities` hash. It has none today, which is a live question
  for Apple's caching terms, but changing it also changes Google-sourced data and
  belongs to that decision rather than this one.
- Confirming `AdminAreaLevelOne` against Google by paired call. No Google API key
  was available; the mapping rests on documented `ShortName` behaviour plus the
  live Apple probe.
