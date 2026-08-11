# Apple Maps SDK — Phase 1 Implementation Plan

Design: [2026-08-04-apple-maps-sdk-design.md](2026-08-04-apple-maps-sdk-design.md).
Section references below point there; no design decisions are restated or
re-opened in this document.

Scope: the `applemaps/` package only. Nothing outside the new directory changes
except `.gitignore` and `go.mod`. Phase 2 (adapter, category map, fallback
routing, quota counter, config wiring) is a separate plan.

Every step is test-first and independently verifiable. `go build ./... && go vet
./... && go test ./applemaps/...` must pass at the end of each step.

## Step 0 — Guard the key, scaffold the package

- Add `*.p8` to `.gitignore` **before any other file lands**.
- `mkdir applemaps`, add `doc.go` with the package comment.

Verify: `git check-ignore -v test.p8` resolves to the new rule.

## Step 1 — `types.go`

All 22 objects and 5 enums from spec § "What Apple actually returns" and
§ "Endpoints". No behaviour, pure declarations.

Enum constants: `PoiCategory` (77 values, own file `poicategory.go`),
`SearchResultType`, `SearchACResultType`, `AddressCategory`, `DirectionsAvoid`.

`TransportType` is a string type with constants `Automobile`, `Walking`,
`Transit`, and `Cycling`, carrying a comment that Apple's documentation truncates
the valid set mid-sentence and that Step 8 confirms it empirically. `Cycling` is
the spelling Apple accepts; `Bicycle` is rejected with HTTP 400. The set is not
uniform across endpoints — `Transit` works on `/v1/etas` but not on
`/v1/directions`, so `DirectionsRequest` rejects it locally.

Every field is a pointer or has an explicit `omitempty` decision: Apple marks
every response field optional, so a zero value must stay distinguishable from an
absent one wherever a caller could misread it — notably `Eta.distanceMeters` and
`Route.hasTolls`, where `0` and `false` are meaningful values.

Verify: compiles; a round-trip test decodes the documented example payloads for
`SearchResponse`, `PlaceResults`, and `TokenResponse` without loss.

## Step 2 — `auth.go`

Per spec § "Authentication". Tests first, all against `httptest.Server`.

Tests:

1. Auth JWT carries `alg: ES256`, `kid`, `typ: JWT` in the header and
   `iss`/`iat`/`exp` in the claims; signature verifies against the public key.
2. Exchange returns the access token and stores expiry from
   `expiresInSeconds`.
3. A second call inside the validity window performs no HTTP request.
4. A call with under 5 minutes remaining triggers exactly one refresh.
5. 50 goroutines racing a cold `TokenSource` produce exactly one exchange
   (assert on a request counter).
6. `401` on exchange surfaces as an auth error, not a retry loop.
7. Key parsing accepts the real PKCS#8 PEM shape; a malformed PEM errors
   without panicking.

Signing tests generate their own P-256 key via `ecdsa.GenerateKey`. No fixture
ever contains a real key.

## Step 3 — `client.go`

`Client`, `Options`, and the shared request path.

Tests:

1. Query encoding: comma-joined lists for the `*PoiCategories` and
   `limitToCountries` params; `lat,lng` formatting for `searchLocation`,
   `userLocation`, and `loc`; the 4-value `searchRegion` ordering
   (north, east, south, west) exactly as spec § "Endpoints" states.
2. `Authorization: Bearer <accessToken>` is set from the `TokenSource`.
3. `401` invalidates the token and retries once; a second `401` returns.
   Assert the retry actually re-exchanged.
   Invalidation is generation-checked, so this needs a concurrent case too:
   several requests holding the *same* stale token all get `401`, and between
   them they must cost one refresh rather than one each. A late invalidation
   naming a token that has already been replaced must be a no-op — an
   unconditional clear would let each `401` discard the refresh the previous one
   paid for. Cover it both racing (many goroutines, assert the exchange count)
   and deterministically (two generations by hand, assert the newer token
   survives), since the racing test cannot guarantee which interleaving it hit.
4. `429` decodes to a distinct `QuotaError` — callers must be able to tell quota
   exhaustion from every other failure, since spec § "Quota guard" routes on it.
5. `5xx` retries with backoff up to a cap, then returns the last error.
6. `ErrorResponse` body is decoded into the returned error's message and
   details; a non-JSON error body still yields a useful error.
7. Context cancellation aborts in flight.

## Step 4 — `geocode.go`

`Geocode(ctx, GeocodeRequest)` and `ReverseGeocode(ctx, lat, lng, opts)`.

Tests: param encoding for both; `PlaceResults` decode; empty `results` returns a
typed not-found error rather than a zero `Place`.

## Step 5 — `search.go`

`Search`, `SearchAll`, `SearchAutocomplete`.

`SearchAll` owns pagination only — it sets `enablePagination`, follows
`nextPageToken`, and stops on an absent token or at the caller's page cap. The
radius filtering and page cap described in spec § "Adapter onto the existing
seam" belong to Phase 2's adapter, not here; this package stays free of
`POI` concepts.

Tests: single page; three pages followed via `nextPageToken`; termination on
absent token; page cap honoured and reported; `poiCategory` survives decode on
`SearchResponse.Place`.

## Step 6 — `place.go`

`Place(ctx, id, opts)`, `Places(ctx, ids, opts)`, `AlternateIDs(ctx, ids)`.

Tests: path escaping for an id containing reserved characters; `ids` comma
joining; partial success — `PlacesResponse` carrying both `results` and `errors`
must surface both, never silently drop the errors.

## Step 7 — `directions.go`

`Directions` and `ETAs`.

`DirectionsResponse` arrives flattened with index pointers:
`routes[].stepIndexes` index into top-level `steps[]`, and each
`steps[].stepPathIndex` indexes into top-level `stepPaths[]`. Expose a resolver
that walks a route into its steps and paths.

Tests, and the reason this step is last:

1. A well-formed multi-route response resolves to the right steps and paths.
2. **Out-of-range `stepIndexes` returns an error and does not panic.**
3. **Out-of-range `stepPathIndex` returns an error and does not panic.**
4. `destinations` pipe-joining for `/v1/etas`.
5. `departureDate`/`arrivalDate` ISO 8601 UTC formatting; setting both is
   rejected locally, since spec § "Endpoints" records that Apple accepts only
   one.

Unchecked indexing here panics the server process on a malformed upstream
response. Bounds checks are the point of this step, not an afterthought.

## Step 8 — Live probe

Against the real API using `~/.config/applemaps/AuthKey_FUTFWSCQA4.p8`, a
throwaway `main` under `/private/tmp`, never committed:

1. `/v1/token` — confirm the exchange and observed `expiresInSeconds`.
2. One call per endpoint. **Do not commit the responses.** Record the observed
   *shape* — key names, nesting, which documented fields are absent and which
   undocumented ones appear — and build fixtures from Apple's own published
   example payloads or from synthetic data matching that shape. Apple's terms
   restrict persistent caching of its data and its combination with other
   providers, and that question is still open (see Open questions), so raw live
   responses must not enter the repository ahead of it. If live fixtures ever
   become necessary, they need legal sign-off plus stated scrubbing and retention
   rules first.
3. `/v1/etas` with a deliberately invalid `transportType`, to make Apple echo
   the accepted set in `ErrorResponse.details`. Fold the result into the
   `TransportType` constants and drop the caveat comment from Step 1.
4. Record any place where observed behaviour contradicts the documented schema.

Budget: well under 20 calls against the 25,000/day quota.

### Step 8 findings

Run on 2026-08-06 with team `JRBD76VZ75` and key `FUTFWSCQA4`. Roughly 30 calls.
Every endpoint answered successfully.

1. **`ErrorResponse` does not match its published schema.** Apple documents
   `message` and `details` at the top level; the live API nests them:

   ```json
   {"error":{"message":"transportType invalid","details":[]}}
   ```

   Decoding only the documented shape produced empty error messages, losing the
   reason for every failure. `ErrorResponse.UnmarshalJSON` now accepts both forms,
   preferring the nested one. This was the single real defect the probe caught.

2. **`TransportType` has four values, not three.** `Automobile`, `Walking`,
   `Transit`, and `Cycling` are all accepted; `Bicycle` is rejected with HTTP 400.
   `Cycling` was missing from the MapKit-derived guess. Apple's 400 response does
   not enumerate the accepted set, so each candidate had to be probed
   individually — `AllTransportTypes` records the confirmed list.

3. **`stepPaths` is an array of polylines**, confirming the prose over the
   machine-readable schema: `[[{lat,lng}],[{lat,lng},{lat,lng},…]]`. A real
   San Francisco to Cupertino route returned 1 route, 15 steps, and 15 step
   paths, and `ResolveRoute` walked all 15.

4. **`FAILED_INVALID_ID`** is the `errorCode` for an unknown place ID. A batch
   lookup of one good and one bogus ID returned 1 result and 1 error together,
   confirming the partial-success handling.

5. **The category mapping looks better than feared for grocery.** All ten results
   for `q=supermarket` near Cupertino came back as `FoodMarket`, not `Store` —
   Safeway, Hankook Supermarket, 99 Ranch Market. That is one query in one metro
   area, so it is encouraging rather than conclusive, but it is evidence for the
   design's approach of carrying specificity in `q`.

Not established: the observed `expiresInSeconds`. The probe's raw `/v1/token`
call presented the access token rather than a freshly signed auth JWT and so
returned 401. The value is documented as 1800 and the decode is covered by test,
but it has not been seen on the wire.

### Step 8 follow-up: pagination, probed separately

The first probe called `Search` (one page) and never exercised `SearchAll`, so
the only real algorithm in the package had synthetic coverage only. Probing it
against the live API found two further undocumented rules, both of which had
made `SearchAll` fail on page 2 every time:

6. **`enablePagination` is rejected once a `pageToken` is present.** Apple:
   `Cannot specify parameter [enablePagination] in search request by pageToken`.
   The flag opts into pagination and belongs to the first request only.

7. **A page request may carry no other parameter at all.** After fixing 6, Apple
   returned `Cannot specify parameter [q] in search request by pageToken`. The
   token encodes the entire original query, so a follow-up page is
   `GET /v1/search?pageToken=…` and nothing else — no `q`, no `searchLocation`,
   no `lang`.

Neither rule appears in Apple's documentation. The fix was structural rather than
documentary: `PageToken` was removed from `SearchRequest` and replaced with a
`SearchPage(ctx, token)` method, which makes the illegal request
unrepresentable instead of merely discouraged.

The test double was the deeper problem. It accepted any combination of
parameters, so it was more permissive than the service it stood in for and could
not have caught either bug. It now rejects a `pageToken` request carrying
anything else, with Apple's own message and status. Reverting the fix makes four
tests fail; before, none did.

Verified live afterwards: a restaurant search near San Francisco walked 3 pages
for 60 places with no duplicates across pages and `Truncated` correctly set
against Apple's reported `totalPageCount` of 5. Pages hold 20 results.

One observation for the design's open item on recall: `q=Golden Gate Bridge`
returned 23 results across 3 pages. Apple treats `searchLocation` as a weak hint
and returns loosely related places, so precision differs markedly from a Google
radius search. That reinforces measuring the recall delta before making Apple
primary.

## Done when

- `go build ./... && go vet ./... && go test ./applemaps/...` clean.
- No import of `iowrappers` or `POI` anywhere in `applemaps/` — the extractability
  property from spec § "Package layout". Enforced by a test that greps the
  package's own imports.
- `transportType` resolved from Step 8, not guessed.
- `*.p8` ignored; no key material in any committed file.
