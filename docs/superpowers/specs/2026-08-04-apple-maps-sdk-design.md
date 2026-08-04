# Apple Maps Server API SDK — Design

Date: 2026-08-04
Status: approved for planning

## Goal

Add an Apple Maps Server API client to this service so Apple can serve as the
primary geo provider with Google Maps as the fallback. Which provider is primary
is a config switch; see "Implementation phasing" for why the first deploy leaves
Google primary. Three motivations, in order:

1. **Cut Google Maps spend.** Apple allows 25,000 service calls per day per
   Apple Developer team at no cost.
2. **Add directions and ETA.** The service has no travel-time provider today.
   Apple `/v1/directions` and `/v1/etas` are net-new capability.
3. **Keep Google as fallback** rather than removing it, so any Apple failure,
   quota exhaustion, or data gap degrades instead of breaking.

The consuming product is `offerbee`, a separate TypeScript monorepo whose Convex
backend calls this service over HTTP (`packages/backend/convex/geoService.ts`)
at `POST /v1/nearby-places` and `POST /v1/nearby-places-by-category`. For those
use cases the load-bearing fields are a place's **coordinate, name, address, and
category** — not rating, price level, opening hours, or photos.

## What Apple actually returns

Verified against Apple's published schema, not assumed.

`Place`:

| Field | Type |
|---|---|
| `id` | string (opaque) |
| `name` | string |
| `coordinate` | `Location` (latitude, longitude) |
| `formattedAddressLines` | `[string]` |
| `structuredAddress` | `StructuredAddress` |
| `country` / `countryCode` | string |
| `displayMapRegion` | `MapRegion` |
| `alternateIds` | `[string]` |

`SearchResponse.Place` extends `Place` with exactly one field: `poiCategory`.

`StructuredAddress`: `administrativeArea`, `administrativeAreaCode`,
`areasOfInterest`, `dependentLocalities`, `fullThoroughfare`, `locality`,
`postCode`, `subLocality`, `subThoroughfare`, `thoroughfare`.

### Fields Apple does not provide, at any tier

No photo, image, or thumbnail. No opening hours. No rating or review count. No
price level. No business status. Apple's complete object list is `TokenResponse`,
`AutocompleteResult`, `DirectionsResponse`, `EtaResponse`, `Location`,
`MapRegion`, `Place`, `PlaceResults`, `PlacesResponse`,
`SearchAutocompleteResponse`, `SearchMapRegion`, `SearchResponse`,
`StructuredAddress`, plus the scalar types `CountryCode`, `DirectionsAvoid`,
`Lang`, `PoiCategory`, `SearchLocation`, `SearchRegion`, `UserLocation`, and
`ErrorResponse`. There is no image object to request. Photos on Apple Maps place
cards come from licensed partners and are not exposed through the API.

Consequence: `POI.Place.Photo`, `.Rating`, `.UserRatingsTotal`, `.PriceLevel`,
and `.Hours` cannot be populated from Apple. The planner paths that consume them
(`matching/score.go:30`, `matching/matcher.go:116`, `matching/matcher.go:131`)
degrade for Apple-sourced places. This is accepted: those fields are not needed
for the offerbee use cases driving this work.

## Endpoints

All ten, at `https://maps-api.apple.com`:

| Endpoint | Required params |
|---|---|
| `GET /v1/token` | — (auth JWT in `Authorization`) |
| `GET /v1/geocode` | `q` |
| `GET /v1/reverseGeocode` | `loc` |
| `GET /v1/search` | `q` |
| `GET /v1/searchAutocomplete` | `q` |
| `GET /v1/place/:id` | path id |
| `GET /v1/place` | `ids` |
| `GET /v1/place/alternateIds` | `ids` |
| `GET /v1/directions` | `origin`, `destination` |
| `GET /v1/etas` | `origin`, `destinations` |

`/v1/search` optional params: `excludePoiCategories`, `includePoiCategories`,
`limitToCountries`, `resultTypeFilter`, `lang`, `searchLocation`, `searchRegion`,
`userLocation`, `searchRegionPriority`, `enablePagination`, `pageToken`,
`includeAddressCategories`, `excludeAddressCategories`.

**There is no radius parameter and no result limit.** `q` is required.
`searchLocation` and `searchRegion` are documented as *hints*, not constraints.
Geographic filtering is therefore the SDK caller's responsibility.

## Package layout

New top-level package with zero imports from `iowrappers` or `POI`, so it stays
extractable into its own module later without a rewrite:

```
applemaps/
  auth.go          TokenSource: ES256 JWT signing, /v1/token exchange, TTL cache
  client.go        Client, Options, doJSON, backoff, 401-retry, typed 429
  types.go         all request/response structs
  poicategory.go   the 75 PoiCategory constants
  geocode.go       Geocode, ReverseGeocode
  search.go        Search, SearchAll (pagination), SearchAutocomplete
  place.go         Place, Places, AlternateIDs
  directions.go    Directions, ETAs
```

Only dependency is `github.com/golang-jwt/jwt/v5`, already in `go.mod`. No new
module requirement.

## Authentication

Two hops. Sign an auth JWT locally with the `.p8` key, then exchange it for a
short-lived access token used on every other call.

```go
type TokenSource struct {
    teamID, keyID string           // JWT iss and kid respectively
    key           *ecdsa.PrivateKey
    httpClient    *http.Client

    mu     sync.Mutex
    token  string
    expiry time.Time
}
```

Auth JWT header `{alg: ES256, kid: <keyID>, typ: JWT}`, claims
`{iss: <teamID>, iat: now, exp: now + 20m}`. `GET /v1/token` with
`Authorization: Bearer <authJWT>` returns `{accessToken, expiresInSeconds}`,
where `expiresInSeconds` is 1800.

Rules:

- Cache the access token; refresh when fewer than 5 minutes remain.
- Hold `mu` across the refresh so a concurrent burst produces one exchange, not
  N. `/v1/token` itself counts against the daily quota.
- On a `401` from any endpoint, invalidate the cached token and retry the request
  exactly once. A second `401` is returned to the caller.

## Credentials

Read through the existing `envconfig` struct in `main.go:23`:

```go
AppleMapsTeamID     string `envconfig:"APPLE_MAPS_TEAM_ID"`
AppleMapsKeyID      string `envconfig:"APPLE_MAPS_KEY_ID"`
AppleMapsPrivateKey string `envconfig:"APPLE_MAPS_PRIVATE_KEY"`      // PEM contents
AppleMapsKeyFile    string `envconfig:"APPLE_MAPS_PRIVATE_KEY_FILE"` // local dev only
```

PEM **contents** is the primary path because deployment goes through
`Procfile` / `heroku.yml`, where there is no filesystem to mount a `.p8` into.
The file path variant exists for local development only. If both are set,
contents wins. `*.p8` is added to `.gitignore`; the key never enters the repo.

### Where each value comes from

| Var | Value | Source |
|---|---|---|
| `APPLE_MAPS_PRIVATE_KEY` | full contents of the `AuthKey_<KEYID>.p8` file, `BEGIN`/`END` lines included | the key downloaded from the Apple Developer portal |
| `APPLE_MAPS_KEY_ID` | 10-character key ID | embedded in the `.p8` filename; also listed under developer.apple.com/account → Keys |
| `APPLE_MAPS_TEAM_ID` | 10-character team ID | developer.apple.com/account → Membership Details |

The Maps ID (the `maps.*` identifier registered under Identifiers) is **not**
used by the Server API. It scopes the key at creation time and is required for
MapKit JS, but nothing in the token exchange or any endpoint references it. Only
Team ID (`iss`) and Key ID (`kid`) appear in the JWT.

The key is a PKCS#8 EC private key on the P-256 curve (`prime256v1`), which is
what `ES256` requires. `jwt/v5` parses it directly — it falls back to
`x509.ParsePKCS8PrivateKey` internally, so no manual PEM handling is needed:

```go
key, err := jwt.ParseECPrivateKeyFromPEM([]byte(pemBytes))
```

### Value encoding

`APPLE_MAPS_PRIVATE_KEY` accepts **either** raw PEM with real newlines **or** a
base64 encoding of the same bytes. Select on whether the trimmed value begins
with `-----BEGIN`; base64-decode otherwise.

Raw newlines survive `heroku config:set` and Docker `--env-file`, but most `.env`
loaders flatten or truncate them, which is the common way this fails. Accepting
both forms removes that failure mode for the cost of one prefix check.

### Local development

The key lives at `~/.config/applemaps/AuthKey_<KEYID>.p8`, mode `600` inside a
`700` directory, and is referenced by absolute path through
`APPLE_MAPS_PRIVATE_KEY_FILE`. Deliberately outside any git working tree: a
gitignored secret that sits inside a repo is one `git add -f` or one edited
ignore rule away from being committed.

Apple permits exactly one download of this key and offers no way to retrieve it
again. If it is lost, or leaked, the only remedy is revoking it in the Apple
Developer portal and issuing a new one. It should be backed up in a password
manager, and it must never be committed, logged, or included in a test fixture —
signing tests generate their own throwaway EC key.

## Adapter onto the existing seam

`iowrappers/apple_maps_client.go` defines `AppleMapsClient`, implementing the
existing `SearchClient` interface (`iowrappers/maps_client.go:19`) so no
downstream caller changes shape:

| `SearchClient` method | Apple call |
|---|---|
| `Geocode` | `/v1/geocode?q=<city>, <admin>, <country>` — coordinate out; `structuredAddress.locality` / `.administrativeArea` / `country` written back into `GeocodeQuery` |
| `ReverseGeocode` | `/v1/reverseGeocode?loc=<lat>,<lng>` — same fields, reverse direction |
| `NearbySearch` | `/v1/search` per mapped term, then client-side filter |

`NearbySearch` details:

1. Map `req.PlaceCat` / `req.Keyword` to a query term and category set (below).
2. Issue `/v1/search` with `q=<term>`, `searchLocation=<lat>,<lng>`,
   `searchRegion=<bbox>`, `includePoiCategories=<mapped>`,
   `enablePagination=true`. The bbox is the coordinate offset by `req.Radius` in
   each cardinal direction: `±radius/111320` degrees latitude, and
   `±radius/(111320·cos(lat))` degrees longitude.
3. Page until a page yields zero results inside the radius, or 5 pages have been
   fetched. The cap bounds quota consumption per search; when it is hit, log the
   truncation rather than returning silently short.
4. **Haversine-filter every result to `req.Radius`.** Apple's location params are
   hints, so unfiltered results can be arbitrarily far away.
5. Tag each place's `LocationType` with the **requested** type.
   `POI/places.go:80` already documents `LocationType` as "the single type a
   search tagged the place with (often the SEARCHED type, not the actual one)",
   so this matches existing semantics rather than fighting them.
6. Fill unavailable fields explicitly, never by accidental zero value:
   `Status = POI.Operational`, `Hours = POI.DefaultOpeningHours` for all seven
   days, `Rating` / `PriceLevel` / `UserRatingsTotal` / `Photo` left zero.

Because Apple reports no business status, the closure-persistence behaviour added
in commit `03f799c` stays Google-only. An Apple-sourced record can never be
retired by the `Operational` read filters, and is refreshed only by
`PlaceDetailsRefreshDuration` ageing.

## Category mapping

Apple's `PoiCategory` is a fixed 75-value enum whose entire retail branch is
`Store`, plus `FoodMarket`, `Pharmacy`, `GasStation`, `Bakery`, `Cafe`,
`Restaurant`. It has no `supermarket`, `department_store`, `clothing_store`,
`electronics_store`, or `convenience_store`.

This matters because `geoService.ts:23` records that offerbee's `bestCard.ts`
"keys card-reward selection off the specific Google place type (supermarket ≠
mall)" — exactly the distinction Apple collapses.

Mitigation: **do not read `poiCategory` off the response to determine type.**
Carry specificity in the free-text `q`, use `includePoiCategories` only as a
coarse narrowing filter, and tag results with the requested type.

Table lives in the adapter, not in `applemaps/`, since it is Google-taxonomy
specific:

```go
type appleQuery struct {
    term string
    cats []applemaps.PoiCategory
}

var appleQueryByLocationType = map[POI.LocationType]appleQuery{ /* ... */ }
```

| `POI.LocationType` | `q` | `includePoiCategories` | Known loss |
|---|---|---|---|
| `cafe` | cafe | `Cafe` | — |
| `restaurant` | restaurant | `Restaurant` | — |
| `bar` | bar | `Nightlife`, `Brewery` | Apple has no Bar; `Nightlife` also covers clubs |
| `bakery` | bakery | `Bakery` | — |
| `meal_takeaway` | takeout | `Restaurant` | Apple models no takeaway concept |
| `meal_delivery` | delivery | `Restaurant` | Apple models no delivery concept |
| `night_club` | night club | `Nightlife` | indistinguishable from `bar` |
| `museum` | museum | `Museum` | — |
| `art_gallery` | art gallery | `Museum` | no gallery category |
| `amusement_park` | amusement park | `AmusementPark` | — |
| `park` | park | `Park`, `NationalPark` | — |
| `tourist_attraction` | tourist attraction | `Landmark`, `NationalMonument` | coarse |
| `zoo` | zoo | `Zoo` | — |
| `aquarium` | aquarium | `Aquarium` | — |
| `movie_theater` | movie theater | `MovieTheater` | — |
| `stadium` | stadium | `Stadium` | — |
| `bowling_alley` | bowling alley | `Bowling` | — |
| `shopping_mall` | shopping mall | `Store` | `Store` covers all retail |
| `department_store` | department store | `Store` | as above |
| `supermarket` | supermarket | `FoodMarket` | `FoodMarket` also matches specialty grocers |
| `grocery_or_supermarket` | grocery store | `FoodMarket` | as above |
| `convenience_store` | convenience store | `Store`, `FoodMarket` | as above |
| `clothing_store` | clothing store | `Store` | as above |
| `store` | store | `Store` | — |
| `hardware_store` | hardware store | `Store` | as above |
| `home_goods_store` | home goods store | `Store` | as above |
| `electronics_store` | electronics store | `Store` | as above |
| `furniture_store` | furniture store | `Store` | as above |
| `book_store` | book store | `Store` | as above |
| `shoe_store` | shoe store | `Store` | as above |
| `jewelry_store` | jewelry store | `Store` | as above |
| `pet_store` | pet store | `Store` | `AnimalService` is veterinary, not retail |
| `bicycle_store` | bike shop | `Store` | as above |
| `florist` | florist | `Store` | as above |
| `liquor_store` | liquor store | `Store` | `Brewery`/`Winery`/`Distillery` are producers |
| `gas_station` | gas station | `GasStation` | — |
| `lodging` | hotel | `Hotel`, `RVPark`, `Campground` | — |
| `gym` | gym | `FitnessCenter` | — |
| `spa` | spa | `Spa` | — |
| `pharmacy` | pharmacy | `Pharmacy` | — |
| `drugstore` | drugstore | `Pharmacy`, `Store` | — |
| `beauty_salon` | beauty salon | `Beauty` | indistinguishable from `hair_care` |
| `hair_care` | hair salon | `Beauty` | as above |

Eighteen of the forty-four types collapse to `Store`. Brand keyword searches
(`req.Keyword` set) are unaffected — the brand name is the discriminator there,
and `geoService.ts:21` confirms the brand endpoint already leaves `locationType`
undefined.

## Cache identity

Apple place IDs are stored prefixed as `apple:<id>`; Google `place_id` values
stay bare. Rationale:

- Existing cached Google records and offerbee's `placeId` field keep working with
  no migration.
- Provenance is readable straight off the key, so an Apple-only data problem is
  diagnosable and reversible.
- Both providers share one geo index and one freshness marker, so a cell warmed
  by either provider satisfies a later request from either. A per-provider
  keyspace would double cold-start cost and require duplicating the marker logic
  in `iowrappers/poi_searcher.go:181`.

Reconciling Apple and Google records for the same physical place is explicitly
out of scope: there is no join key, only name-plus-coordinate fuzzy matching.

## Provider routing and fallback

```go
type FallbackSearchClient struct {
    primary, secondary SearchClient
}
```

Tries `primary`, falls back to `secondary` on error, on a typed quota error, or
on an empty result set. Every fallback logs the triggering reason so the real
Apple hit rate is measurable rather than assumed. Which client is primary is
config-selectable, so reverting to Google is a config change, not a deploy of new
code.

## Quota guard

The 25,000 daily calls are per team and **shared with MapKit JS**, and once
exhausted Apple returns `429` on every endpoint including `/v1/token`. Waiting
for that cliff would make the whole service fail over at an unpredictable moment.

Instead: a Redis counter keyed `applemaps:quota:<utc-date>`, incremented per
outbound Apple call, expiring after 48 hours. Above a configurable threshold
(default 90%), route to Google pre-emptively. The threshold is ours to tune; the
cliff is not.

## Testing

`applemaps` package, using `httptest.Server` throughout — no live Apple calls in
tests:

- token exchange happy path; refresh when near expiry; no refresh when fresh
- concurrent callers produce exactly one token exchange
- `401` triggers one invalidate-and-retry, and a second `401` surfaces
- `429` decodes to a typed quota error distinguishable from other failures
- query parameter encoding for every endpoint, including comma-joined lists
- response decoding for each object type, including absent optional fields
- pagination follows `pageToken` and terminates

JWT signing is tested against an EC key generated in the test. The real `.p8`
never appears in a test fixture.

Adapter tests in `iowrappers`, following the existing `miniredis` /
`redis_client_mocks` setup:

- category map is table-driven and total over every `POI.LocationType`
- radius filter drops results outside `req.Radius`
- Apple IDs are stored with the `apple:` prefix; Google IDs are not
- `GeocodeQuery` round-trips through both geocode directions
- `FallbackSearchClient` falls back on error, on quota error, and on empty; does
  not fall back on success

## Open items

1. **Apple Maps terms of service.** Apple's terms carry restrictions on caching
   results and on combining Apple Maps data with other map providers. This
   architecture does both: a persistent Redis place cache holding Google and
   Apple records side by side. This needs a legal read before shipping to
   production. It does not block building or testing the SDK.
2. **MapKit JS quota sharing.** If the offerbee native or web app uses MapKit JS,
   it draws from the same 25,000 daily calls. Confirm before setting the quota
   threshold.
3. **Recall parity is not achievable.** Apple has no radius search. Filtering
   hint-based results client-side will not reproduce Google's nearby-search
   recall. This does not change the design, but it does gate the rollout: ship
   with Google primary, measure the recall delta on real queries via the
   fallback logging, then flip the config once the delta is known and accepted.
   The flip is a config change, so this costs no extra implementation work.

## Implementation phasing

Two phases, so the SDK is reviewable independently of the routing changes:

1. **`applemaps/` package.** All ten endpoints, token lifecycle, typed errors,
   full `httptest` suite. No changes outside the new directory except `go.mod`
   and `.gitignore`. Independently verifiable against the real API with a
   throwaway script.
2. **Integration.** `iowrappers/apple_maps_client.go` adapter, category map,
   `FallbackSearchClient`, Redis quota counter, `main.go` config wiring, adapter
   tests. Ships with Google primary per open item 3.
