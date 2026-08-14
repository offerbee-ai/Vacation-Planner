# Apple Maps Phase 2 — Geocoding

Phase 1 (the `applemaps` package) is complete: ten endpoints, token lifecycle,
typed errors, tests. Nothing imports it. This design says what does.

Supersedes most of [the earlier Phase 2 plan](2026-08-07-apple-maps-phase-2-plan.md);
see [What this supersedes](#what-this-supersedes). Builds on
[the SDK design](2026-08-04-apple-maps-sdk-design.md).

## Why

Google Places is the only provider for place lookup today. Apple's quota is
25,000 calls per day per developer team, free, so any request Apple can serve
correctly is one we stop paying for.

"Correctly" turned out to be the whole design question.

## Scope: Apple serves geocoding, not place search

Apple serves `Geocode` and `ReverseGeocode`. Place search stays on Google
entirely.

The earlier plan had Apple serving `NearbySearch` behind a category allowlist.
That does not survive the requirement that opening hours stay intact, and the
reason is structural rather than fixable:

- Apple's `Place` carries no opening hours at any endpoint or tier. Phase 1
  established this against the published schema and confirmed it live.
- The usual remedy — return the place, then buy details for the missing fields —
  is unavailable. Google's Place Details is keyed on a Google `place_id`, and an
  Apple result gives `apple:<opaque>`. There is no join key. The SDK design ruled
  out name-plus-coordinate fuzzy matching, correctly, for data feeding
  card-reward decisions.

So an Apple-sourced place would arrive with empty hours permanently, not until
some later pass filled them in.

The cost argument also came out weaker than the earlier plan assumed. Google's
spend on the place path is dominated by `PlaceDetailedSearch` — one call per
place, capped by `DetailsLimit` — not by the Nearby Search call. Substituting
Apple removes the cheap half and keeps none of the expensive half, because the
details call cannot follow an Apple ID.

Geocoding has none of these problems. The mapping is 1:1, verified live (below).
`ReverseGeocode` runs on every nearby scan through `processLocation`, and its own
comment at `iowrappers/poi_searcher.go:151` records that on a warm place cache it
was the only Google call left. That is the saving worth having, and it carries no
field deficiency at all.

One consequence worth stating plainly: **no Apple-sourced place ever enters
Redis.** The shared-keyspace risks the SDK design worried about — Apple records
diluting the geo buckets, the planner's `filterPlacesOnTime` dropping hours-less
places, `PlaceScore` flooring them at zero — are all moot, because there are no
Apple places.

## What changes

Three new files, one changed struct.

```
iowrappers/apple_maps_client.go     AppleMapsClient — applemaps.Client → Geocoder
iowrappers/apple_geocode_router.go  AppleGeocodeRouter — routing and fallback
iowrappers/apple_quota.go           QuotaCounter — Redis INCR and threshold
```

### `SearchClient` splits

`SearchClient`'s first two methods become a named interface, so the Apple adapter
implements only what it can do:

```go
type Geocoder interface {
	Geocode(context.Context, *GeocodeQuery) (float64, float64, error)
	ReverseGeocode(context.Context, float64, float64) (*GeocodeQuery, error)
}

type SearchClient interface {
	Geocoder
	NearbySearch(context.Context, *PlaceSearchRequest) ([]POI.Place, error)
}
```

`*MapsClient` satisfies both unchanged. No call site moves.

### `AppleGeocodeRouter`

```go
type AppleGeocodeRouter struct {
	apple  Geocoder      // *AppleMapsClient
	google SearchClient  // *MapsClient
	quota  *QuotaCounter
}
```

It implements `SearchClient` in full. `Geocode` and `ReverseGeocode` try Apple
and fall back to Google. `NearbySearch` delegates to Google unconditionally —
there is no condition under which Apple serves it, so there is no `canServe`
predicate and no allowlist.

Putting the policy in one named type is the point: "Apple does geocoding only"
is a property of this struct, not a rule spread across `PoiSearcher`.

### `PoiSearcher` keeps one search seam

```go
type PoiSearcher struct {
	searcher    SearchClient        // *MapsClient, or AppleGeocodeRouter wrapping it
	details     PlaceDetailsClient  // migrations only
	redisClient *RedisClient
}
```

All three provider call sites — `Geocode:139`, `ReverseGeocode:159`, and the cold
search at `:348` — go through `searcher`. With Apple disabled, `searcher` *is*
`*MapsClient` and the router is not in the path at all.

`GetMapsClient()` is deleted. Its three callers are handled without it:

| Caller | Today | After |
|---|---|---|
| `CreatePoiSearcher:77` | `SetCachedPlaceLookup` after construction | set inside the constructor, before the client is stored |
| `planner.go:196` | `SetDetailedSearchFields` poked in post-hoc | `detailedSearchFields` passed to `CreatePoiSearcher` |
| `data_migrations.go:146` | reaches through for `PlaceDetailedSearch` | uses the `details` field |

`details` survives as a second field because Place Details is a capability Apple
does not have at any tier, used by exactly two migration handlers. Splitting by
capability is honest; splitting by vendor would not be.

```go
type PlaceDetailsClient interface {
	PlaceDetailedSearch(context.Context, string, []string) (maps.PlaceDetailsResult, error)
}
```

## Field mapping

`GeocodeQuery` has three fields, and Google fills them from
`geocodingResultsToGeocodeQuery` (`iowrappers/transformers.go:12`):

| Field | Google | Apple |
|---|---|---|
| `City` | `locality` **LongName** | `structuredAddress.locality` |
| `AdminAreaLevelOne` | `administrative_area_level_1` **ShortName** | `administrativeAreaCode`, falling back to `administrativeArea` |
| `Country` | `country` **LongName** | `Place.Country` |

Two rules are not obvious and both were established by live probe rather than
from the schema.

**`administrativeAreaCode` is conditional.** It is present where a country has
conventional subdivision abbreviations and absent where it does not:

| Query | `administrativeArea` | `administrativeAreaCode` |
|---|---|---|
| Washington, DC | District of Columbia | `DC` |
| Cupertino, CA | California | `CA` |
| Sydney, Australia | New South Wales | `NSW` |
| Toronto, Canada | Ontario | `ON` |
| Paris, France | Île-de-France | *(empty)* |
| Berlin, Germany | Berlin | *(empty)* |
| Tokyo, Japan | Tokyo | *(empty)* |

That is exactly Google's `ShortName` semantics, which returns an abbreviation
where one exists and the long name otherwise. So the fallback reproduces Google
in both branches. Mapping straight from `administrativeAreaCode` would empty the
field for every non-abbreviating country.

**Never overwrite a caller's field with an empty Apple value.** Forward-geocoding
`"Tokyo, Tokyo, Japan"` returns a match on the prefecture rather than a locality,
so `structuredAddress.locality` comes back empty. `MapsClient.Geocode` mutates
its `*GeocodeQuery` in place and `PoiSearcher.Geocode:144` writes the mutated
query to the cache, so a blank `City` would reach the `geocode:cities` hash field.
Google can clobber freely because its component matching always yields a locality;
Apple cannot. The adapter assigns a field only when Apple returned something.

Reverse geocode never showed this — Cupertino, Paris, Shibuya, Berlin, Singapore,
and Central all returned a locality — and `planner.go:791` reverse-geocodes every
forward result and overwrites all three fields from it. The rule is belt and
braces for the cache write.

### Query construction

Google's forward geocode takes structured components — `ComponentLocality`,
`ComponentCountry`, `ComponentAdministrativeArea` — matched per field. Apple takes
a single free-text `q`. The adapter joins the non-empty `GeocodeQuery` fields with
`", "`.

Probed, and the flattening holds:

| `q` | Result |
|---|---|
| `Paris, TX, United States` | 33.6601, −95.5554 — Texas |
| `Paris, Île-de-France, France` | 48.8568, 2.3511 — France |
| `Paris, ÎLE-DE-FRANCE, France` | identical coordinate |
| `Springfield, IL, United States` | 39.7984, −89.6494 — Illinois |
| `Toronto, ON, Canada` | 43.6516, −79.3831 — Ontario |
| `Paris, United States` | 33.6601, −95.5554 — Paris, TX |
| `Springfield, United States` | 37.2071, −93.2924 — Springfield, MO |

Apple honours the admin and country terms, and case is irrelevant — the
uppercased form `GeocodeQuery.String()` produces returns the same coordinate.
Underspecified input resolves by Apple's ranking, which is what Google does with
the same input.

## Fallback

`AppleGeocodeRouter` falls back to Google on:

- any transport or API error,
- `*applemaps.QuotaError`,
- `*applemaps.NotFoundError` — Apple answers an unmatched geocode with HTTP 200
  and an empty array, which the SDK already converts,
- the quota counter being over threshold, checked before the call.

Every fallback logs its reason at info level, so the real Apple hit rate is
measurable rather than assumed. A fallback costs one Apple call plus one Google
call; silent fallback would make that invisible.

## Quota guard

A Redis counter keyed `applemaps:quota:<utc-date>`, expiring after 48 hours.
Above a threshold, skip Apple and go straight to Google.

Two properties the counter has to have, or the threshold guards a number that
does not mean what it says:

**Increment at the HTTP transport boundary.** One logical geocode can cost more
than one call: a `/v1/token` exchange when the cached token has gone stale, plus
a retry per 5xx. Apple charges for each. Counting logical operations would
under-report by a factor that varies with the failure rate, worst exactly when the
budget is tightest.

**Account for traffic the counter cannot see.** The 25,000 is per team and shared
with MapKit JS, so anything else shipping under team `JRBD76VZ75` spends from the
same budget invisibly. The threshold applies to our count **plus a configured
external-consumption allowance**, stated explicitly rather than assumed zero.
Sizing it needs the Maps developer dashboard, which reports true team-wide usage.
Until someone reads it the allowance is a guess, so the default threshold is
conservative.

A Redis failure never blocks a request: the counter's error path falls through to
Google.

Expected volume is low. Both geocode methods are cache-first — `geocode:cities`
for forward, an ~8 km cell cache for reverse — so Apple sees misses only.

## Config and rollout

`APPLE_MAPS_ENABLED` defaults to false. `CreatePoiSearcher` gains the Apple
credentials and `detailedSearchFields`:

| Variable | Meaning |
|---|---|
| `APPLE_MAPS_ENABLED` | off by default |
| `APPLE_MAPS_TEAM_ID` | 10-character team ID, JWT `iss` |
| `APPLE_MAPS_KEY_ID` | 10-character key ID, JWT `kid` |
| `APPLE_MAPS_PRIVATE_KEY` | `.p8` contents, PEM or base64-encoded PEM |
| `APPLE_MAPS_QUOTA_THRESHOLD` | fraction of the daily budget, default 0.9 |
| `APPLE_MAPS_EXTERNAL_ALLOWANCE` | calls per day assumed spent by MapKit JS |

Missing or invalid credentials degrade to Google-only rather than failing
startup: a bad key should not take the service down.

## Cache and terms

Apple's terms on caching its data are unresolved, and this design does persist
Apple-derived data — geocode results, not places. Two facts matter for the gate:

- The reverse-geocode cache already expires after 30 days
  (`ReverseGeocodeExpiration`), and its comment says the bound exists because
  Google's terms allow only temporary caching. The same bound plausibly satisfies
  Apple.
- The forward-geocode cache does **not** expire. `SetGeocode` writes to the
  `geocode:cities` hash with no TTL, so entries are permanent.

`APPLE_MAPS_ENABLED=false` means nothing Apple-derived is written until someone
deliberately turns it on. Resolving the terms is a precondition for enabling, not
for merging. If they require bounded caching, giving `geocode:cities` a TTL is the
change — one that would arguably be right for the Google data already in it.

## Testing

Adapter, against Apple's documented payloads and the probe observations:

- both `administrativeAreaCode` shapes, present and absent, mapping to the same
  field Google fills;
- an empty `locality` leaves the caller's `City` untouched;
- `q` construction from one, two, and three populated fields;
- `Country` long form.

Router, with two fakes:

- Apple success returns Apple's answer and never calls Google;
- each fallback trigger — error, `*QuotaError`, `*NotFoundError`, over-threshold —
  calls Google and logs a reason;
- `NearbySearch` always calls Google, never Apple;
- Apple disabled means the router is not constructed at all.

Quota counter, on the existing `miniredis` setup in `redis_client_mocks`:

- increments per outbound call including retries and token exchanges;
- threshold routes to Google;
- the key expires;
- a Redis failure does not block the request.

No live Apple calls in tests. Fixtures come from Apple's published examples and
from recorded probe shapes, never from committed raw responses.

## What this supersedes

From [the Phase 2 plan](2026-08-07-apple-maps-phase-2-plan.md), now moot because
Apple serves no place search:

- the `POI.LocationType` → Apple category allowlist;
- Correction 1, Apple places carrying empty `Hours`;
- Correction 2, restricting Apple to types with a 1:1 mapping;
- Correction 3, hours-filtered requests routing to Google;
- Correction 4, price-constrained requests routing to Google;
- `FallbackSearchClient` and its `canServe` predicate.

Still current: the `apple:` ID prefix convention, should place search ever be
revisited; the quota-counter reasoning; and the credentials list.

From [the SDK design](2026-08-04-apple-maps-sdk-design.md), the "Cache identity"
section's shared-geo-index reasoning no longer applies, since no Apple place is
written.

## Open questions

1. Apple's terms on caching geocode results, and the `geocode:cities` TTL that
   may follow. Pre-production gate, not a merge gate.
2. Whether the native app draws on the same 25,000/day team quota through
   MapKit JS. Needed to size `APPLE_MAPS_EXTERNAL_ALLOWANCE`; until then the
   threshold stays conservative.
3. The Google side of the `AdminAreaLevelOne` comparison rests on documented
   `ShortName` behaviour, not a paired call — no Google API key was available
   during the probe. Worth one confirming pair, though it does not block.

## Probe record

Three throwaway probes on 2026-08-11, team `JRBD76VZ75`, key `FUTFWSCQA4`,
26 calls total. Never committed.

1. Six geocodes and two reverse geocodes across administrative systems,
   establishing the conditional `administrativeAreaCode`.
2. Ten forward geocodes testing `q` flattening and disambiguation, including the
   Paris TX / Paris France pair. All ten correct.
3. Four reverse geocodes of city-states — Tokyo, Berlin, Singapore, Hong Kong —
   confirming `locality` is populated there.

Also settled, and previously open in the Phase 1 plan: `expiresInSeconds` is
**1800**, observed on the wire. The Step 8 probe never saw it because its raw
`/v1/token` call presented the access token instead of a signed auth JWT and got a
401. The token response carries exactly two keys, `accessToken` and
`expiresInSeconds`.

One field went the other way: `subAdministrativeArea` was empty on all ten
geocode and reverse-geocode responses, despite appearing as
`"San Francisco County"` in Apple's `searchAutocomplete` example. It looks
endpoint-specific. Harmless — `GeocodeQuery` has no county field — but the struct
field added in the Phase 1 review has no source on the two endpoints this design
uses.
