# Apple Maps Phase 2 — Adapter and Routing

Design: [2026-08-04-apple-maps-sdk-design.md](2026-08-04-apple-maps-sdk-design.md).
Phase 1 (the `applemaps` package) is complete; see
[the Phase 1 plan](2026-08-04-apple-maps-sdk-plan.md).

Scope: the `iowrappers` adapter, the category allowlist, provider routing, the
quota counter, and config wiring.

This plan is derived from what `~/code/offerbee` actually reads over the wire,
not from the design's assumptions. Where the two disagree, offerbee wins and the
design is corrected.

## What offerbee actually consumes

Five endpoints, not the two the design named.

| Endpoint | Consumer | Fields read |
|---|---|---|
| `POST /v1/nearby-places` | `nearby.ts:147` | `lat`, `lng`, `hours` |
| `POST /v1/nearby-places-by-category` | `bestCard.ts:366` | `placeId`, `name`, `address`, `lat`, `lng`, `hours`, `url`, `rating`, `locationType` |
| `POST /v1/place-search` | `bestCard.ts:508`, `placeSearch.ts` | as above, plus `category`, `insertable` |
| `POST /v1/place-search/confirm` | `bestCard.ts` | `place`, `category`, `alreadyCached` |
| `POST /v1/create-token` | operational | — |

Categories requested: `Eatery`, `Shopping`, `Lodging`, `Wellness`, `Visit`
(`cardRewards.ts` `NEARBY_CATEGORIES`).

### Field priorities, measured

- **`locationType` — load-bearing and safety-critical.** See below.
- **`hours` — load-bearing.** Filters in both the brand and category paths.
- **`rating` — read**, and passed through to the UI in three places.
- **`priceLevel` — rendered nowhere in offerbee,** confirmed by grep across
  `packages` and `apps`. That is narrower than it first looks: it settles the
  *response* side only. Price is still an input on the request side of this
  service, where it filters, so Apple's missing price level does cost something.
  See Correction 4.

## Correction 1: Apple-sourced places must carry empty Hours

`POI.CreatePlace` fills `POI.DefaultOpeningHours` (`"8:30 am – 9:30 pm"`) into
every weekday a source left blank (`POI/places.go:383`). The design said to do
that for Apple places. That is wrong, and worse than leaving the field alone.

offerbee's `isOpenAtParts` returns `null` — meaning "unknown" — only when
`hours.length !== 7` (`placeHours.ts:110`). Both consumers treat `null` as
"keep": `bestCard.ts:404` drops a place only on `=== false`, and `nearby.ts:163`
keeps on `!== false`. But seven entries of `"8:30 am – 9:30 pm"` parse to a real
range, so a fabricated window yields a confident `true` or `false` instead of
`null`. A place open until midnight would be filtered out at 22:00 because our
invented hours said it closed at 21:30.

So the adapter must leave `Hours` empty for Apple-sourced places. `POI` already
distinguishes the two cases via `HasRealOpeningHours` (`POI/places.go:180`),
which exists precisely because filled defaults make emptiness undetectable.

This makes hours-less Apple places degrade to "unknown, keep" downstream rather
than to a confident lie.

## Correction 2: restrict Apple to types with a 1:1 mapping

`cardRewards.ts:574` keys card rewards on exact lowercase Google place type
strings, and deliberately maps many to `null`, with reasons in the source:

```ts
convenience_store: null,  // grocery bonuses explicitly exclude convenience stores
hardware_store: null,     // "mis-crediting a department-store bonus here is worse
electronics_store: null,  //  than the base rate"
book_store: null, jewelry_store: null, pet_store: null, florist: null, ...
supermarket: GROCERY,
store: DEPARTMENT,
```

Apple's `PoiCategoryStore` collapses `supermarket`, `shopping_mall`,
`clothing_store`, `hardware_store`, `electronics_store`, `furniture_store`,
`book_store`, `shoe_store`, `jewelry_store`, `pet_store`, `bicycle_store`, and
`florist` into one value. `PoiCategoryFoodMarket` covers both supermarkets and
specialty grocers.

The design's plan — tag results with the *requested* type — is unsafe given a
measured fact from the Phase 1 probe: Apple's `q` matching is loose, with
`q=Golden Gate Bridge` returning 23 loosely related places. Searching
`q=supermarket`, receiving a convenience store, and tagging it `supermarket`
credits a grocery bonus that the card explicitly excludes. That is wrong
financial advice, and it is the exact error offerbee's explicit `null`s were
written to prevent.

The category map is therefore an **allowlist**, not a best-effort table. Apple
serves a Google type only where Apple has an unambiguous equivalent:

| `POI.LocationType` | Apple `q` | `includePoiCategories` |
|---|---|---|
| `cafe` | cafe | `Cafe` |
| `restaurant` | restaurant | `Restaurant` |
| `bakery` | bakery | `Bakery` |
| `pharmacy` | pharmacy | `Pharmacy` |
| `gas_station` | gas station | `GasStation` |
| `lodging` | hotel | `Hotel` |
| `movie_theater` | movie theater | `MovieTheater` |
| `zoo` | zoo | `Zoo` |
| `aquarium` | aquarium | `Aquarium` |
| `museum` | museum | `Museum` |
| `stadium` | stadium | `Stadium` |
| `bowling_alley` | bowling alley | `Bowling` |
| `amusement_park` | amusement park | `AmusementPark` |
| `gym` | gym | `FitnessCenter` |
| `spa` | spa | `Spa` |

Every other type — all ambiguous retail, plus `bar`, `night_club`,
`meal_takeaway`, `meal_delivery`, `art_gallery`, `tourist_attraction`,
`beauty_salon`, `hair_care`, `park` — routes to Google. A type absent from the
allowlist is not an error; it is a routing decision.

Two consequences worth stating plainly. `Shopping` is almost entirely outside the
allowlist, so it stays on Google. And Apple covers 5 of the 9 types in
`ENTERTAINMENT_PLACE_TYPES` (`cardRewards.ts:210`), missing `art_gallery`,
`tourist_attraction`, `museum` is covered, so `Visit` is partially servable.

## Correction 3: hours-filtered requests route to Google

Any request carrying `localTime` needs real opening hours to mean anything, and
Apple has none. Such requests route to Google regardless of the allowlist.

Because both providers share one Redis keyspace (per the design's cache-identity
decision), an Apple-sourced place written on a no-`localTime` request can later be
read from cache by a `localTime` request. With Correction 1 in place that place
carries empty hours, so it degrades to "unknown, keep" rather than to a
fabricated window — acceptable, and the reason Correction 1 is a prerequisite
rather than a nicety.

## Correction 4: price-constrained requests route to Google

`priceLevel` being unread by offerbee (measured above) said only that nothing
*renders* it. It is still an active part of the **search contract** on this side,
and the design's "Apple's missing price level costs nothing" was scoped too
narrowly.

`matching/matcher.go:83` copies `req.PriceLevel` into `PlaceSearchRequest`, and
two things downstream act on it:

- `iowrappers/nearby_search.go:94-112` — when `POI.PriceyEatery` holds (an eatery
  at level 3 or above), the price becomes Google's `MinPrice`/`MaxPrice`, a filter
  applied by the provider. Apple has no equivalent parameter, so an Apple-served
  request would return unfiltered results under a request that asked for filtered
  ones.
- `matching/matcher.go:128` — `filterPlacesOnPriceLevel` keeps a place only when
  `place.PriceLevel == level` exactly. Apple-sourced places carry level zero by
  Step 1's explicit-zero rule, so **every** Apple place is discarded whenever a
  non-zero price filter runs. The Apple call is spent and its results thrown away.

So this is not a degraded-field problem but a wasted-call-and-wrong-answer one.
`canServe` returns false for any request with a non-zero `PriceLevel`, alongside
the non-allowlisted types and the `localTime` case.

Tests: a non-zero `PriceLevel` routes to Google with no Apple call; zero still
reaches Apple when the type is allowlisted.

## Step 1 — `iowrappers/apple_maps_client.go`

`AppleMapsClient` implementing `SearchClient` (`iowrappers/maps_client.go:19`).

- `Geocode` — `/v1/geocode`, filling `GeocodeQuery` from `structuredAddress`
- `ReverseGeocode` — `/v1/reverseGeocode`
- `NearbySearch` — allowlist lookup, `SearchAll`, then haversine filter to
  `req.Radius`

Place conversion, explicitly rather than by zero value:

| `POI.Place` field | Apple source |
|---|---|
| `ID` | `"apple:" + place.ID` |
| `Name` | `name` |
| `FormattedAddress` | `formattedAddressLines` joined |
| `Location` | `coordinate` |
| `LocationType` | the **requested** allowlisted type |
| `Types` | the requested type only |
| `Status` | `POI.Operational` (Apple reports no closures) |
| `Hours` | **left empty** — Correction 1 |
| `Rating`, `UserRatingsTotal`, `PriceLevel`, `Photo` | zero |
| `URL` | empty; Apple exposes no place URL |

`POI.CreatePlace` cannot be reused, since filling defaults is exactly what
Correction 1 forbids. The adapter constructs `POI.Place` directly.

Tests: allowlist is exhaustive over `POI.LocationType` and every entry round
trips; a non-allowlisted type is reported as unservable rather than guessed;
radius filter drops out-of-range results; `Hours` is empty and
`HasRealOpeningHours` is false; IDs carry the `apple:` prefix.

## Step 2 — routing

```go
type FallbackSearchClient struct {
    primary, secondary SearchClient
    canServe  func(*PlaceSearchRequest) bool
}
```

Routes to `secondary` (Google) when `canServe` is false — a non-allowlisted type,
a request carrying a local time, or a request carrying a non-zero `PriceLevel` —
and falls back on error, `*applemaps.QuotaError`, or an empty result. Every
fallback logs its reason so the real Apple hit rate is measurable rather than
assumed.

Tests: allowlisted type with no local time and no price constraint goes to Apple;
non-allowlisted goes to Google without an Apple call; local-time request goes to
Google; price-constrained request goes to Google; each fallback trigger works; a
success does not fall back.

## Step 3 — quota counter

Redis `INCR` on `applemaps:quota:<utc-date>`, 48-hour expiry, pre-emptive
fallback above a configurable threshold (default 90%). The 25,000 daily calls are
shared with MapKit JS, and offerbee's iOS app ships under the same team
(`JRBD76VZ75`), so the budget is not ours alone.

Tests: counter increments per outbound call; threshold routes to Google; the key
expires; a Redis failure does not block the request.

## Step 4 — config wiring

`main.go` gains the four Apple env vars from the design's Credentials section,
plus `APPLE_MAPS_ENABLED` defaulting to false. `PoiSearcher` builds the
`FallbackSearchClient` only when the credentials are present and valid, so a
missing key degrades to Google-only rather than failing startup.

## Done when

- `go build ./... && go vet ./... && go test ./...` clean
- Every offerbee-consumed field either sourced from Apple or explicitly left in
  its "unknown" state, never fabricated
- `priceLevel` left at zero on Apple places, and price-constrained requests never
  reach Apple in the first place
- Google still primary by default; Apple enabled by config
- A table in this document recording, per offerbee endpoint, which provider serves
  it after this change

## Open

1. Apple Maps terms of service on caching and on mixing providers, carried over
   from the design. Still unresolved, still a pre-production gate.
2. Whether offerbee's native app uses MapKit JS, which would draw on the same
   25,000 daily calls. `apps/native` has no MapKit reference found so far, but
   this was not exhaustively checked.
3. `rating` has no Apple source at all. Apple-sourced places will surface in
   offerbee's UI without a rating while Google-sourced ones have it, which is a
   visible inconsistency in the merchant sheet rather than a correctness problem.
