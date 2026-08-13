# Sliding Token Expiration (PAT Auto-Renewal) — Design

**Date:** 2026-08-13
**Status:** Approved pending user review
**Goal:** Service tokens (PATs) used by machine consumers (OfferBee Convex backends) must never expire while in active use — zero-touch continuity, no client-side refresh flow, no operator intervention.

## Context

The geo backend (this repo) authenticates API consumers with Personal Access Tokens: opaque UUIDs stored in Redis (`iowrappers/pat.go`), validated per-request via `ValidatePATByHash`. Expiry is enforced logically by `TokenRecord.Valid()` (`ExpiresAt` check) — Redis keys are stored with TTL 0 and never evicted. There is no refresh or renewal mechanism; when a token's `ExpiresAt` passes, the consumer gets 401s until an operator mints and propagates a new token.

Three OfferBee Convex deployments (dev, staging, prod) each hold a static PAT in the `GEO_SERVICE_TOKEN` env var. Today those tokens have fixed 1-year expiries — a silent time bomb.

**Decision (user, 2026-08-12):** priority is continuity/zero-touch, not credential rotation. Convex side may change but geo-only is preferred.

## Design: Sliding Expiration

A token created with a *sliding interval* renews itself on use: every successful authentication that lands in the back half of its validity window pushes `ExpiresAt` forward to `now + interval`. An actively used token never expires; an abandoned one dies within one interval.

### 1. Data model — `TokenRecord` gains `RenewInterval`

`iowrappers/pat.go`:

```go
type TokenRecord struct {
    // ... existing fields ...
    RenewInterval time.Duration `json:"renew_interval,omitempty"` // 0 = fixed expiry (legacy behavior)
}
```

- Zero value = today's fixed-expiry behavior. Existing records in Redis unmarshal with `RenewInterval == 0` — **fully backward compatible, no migration**.
- `TokenMetadata` gains the same field so `list-tokens` and introspection can expose it.

### 2. Creation — `sliding_interval` on `/v1/create-token`

`NewTokenInfo` (`planner/planner.go`) gains:

```go
SlidingInterval string `json:"sliding_interval,omitempty"` // e.g. "720h" for 30 days
```

- Parsed with `time.ParseDuration`, same as `expiration_duration`. Invalid format → 400.
- When set: initial `ExpiresAt = now + slidingInterval` and `RenewInterval = slidingInterval`. Mutually exclusive with `expiration_duration` — sending both → 400.
- When absent: existing behavior unchanged (default 5m fixed expiry).
- `NewPAT` signature gains a `renewInterval time.Duration` parameter (single call site).
- Bounds: `sliding_interval` must be ≥ 1h and ≤ 8760h (1 year); outside → 400. Prevents accidental "renew every 30s" hot-path writes and infinite windows.

### 3. Renewal — slide inside `ValidatePATByHash`

After a token validates successfully (`token.Valid()` true), in `iowrappers/pat.go`:

```
if token.RenewInterval > 0 && now.After(token.ExpiresAt.Add(-token.RenewInterval/2)) {
    // slide: ExpiresAt = now + RenewInterval, transactional
}
```

- **Halfway-mark gate:** slide only in the back half of the window. Under continuous traffic this bounds Redis writes to ~one per `RenewInterval/2` per token; the hot auth path stays read-only almost always.
- **Transactional (revoke-race safety):** the slide runs in `Watch` on `pat:<id>`, re-reads the record inside the transaction, and skips if `RevokedAt != nil`. `TxFailedErr` (lost race with a concurrent revoke or another slide) is swallowed — the current request already authenticated; the next request slides. Revocation is safe regardless: `RevokePAT` deletes `pat_hash:<token>`, so a stale record overwrite can never resurrect a revoked token.
- **Slide failure never fails auth:** any error in the slide path is logged (warn) and ignored; the validated record is returned. Renewal is best-effort on a path that retries itself on every request.
- The slide updates only `ExpiresAt` (re-marshals the record read inside the transaction, not the possibly-stale one from validation).

### 4. Introspection — `GET /v1/token`

New endpoint, registered in the `/v1` group. **PAT-only** (does not use `UserAuthentication`, which swallows the token record and only returns a user view):

- Parses `Authorization: Bearer <token>` itself; missing/malformed header → 401.
- Calls `ValidatePATByHash` directly — which means **hitting this endpoint also slides the token**, so it doubles as a keep-alive for idle periods.
- Response 200:

```json
{
  "name": "offerbee-production",
  "expiresAt": "2026-09-12T08:00:00Z",
  "renewInterval": "720h0m0s",
  "valid": true
}
```

- Invalid/expired/revoked token → 401 (same message as other endpoints).

### 5. Consumer keep-alive (optional, deferred)

Active OfferBee traffic already renews the token. As pure insurance against long idle periods (> interval with zero requests), a daily Convex cron can call `GET /v1/token`. **Not shipped in this plan** — noted as an OfferBee-side follow-up if desired.

### 6. Cutover (ops, after deploy)

For each of the three Convex environments (dev `exuberant-minnow-833`, staging `adept-porpoise-776`, prod `handsome-dodo-841`):

1. `DELETE /v1/revoke-token` with the current token name (`NewPAT` rejects re-minting a name while the old token is still valid — revoke first).
2. `POST /v1/create-token` with the same name and `"sliding_interval": "720h"` (30 days).
3. `CONVEX_DEPLOYMENT=<id> npx convex env set GEO_SERVICE_TOKEN <new-token>` from `~/code/Offerbee/packages/backend`.
4. Verify with `GET /v1/token` and a live `POST /v1/nearby-places`.

Order per environment: mint + set + verify before touching the next (prod last).

**Chosen interval: 720h (30 days).** Long enough that a quiet week cannot kill an in-use token; short enough that an abandoned token self-expires within a month.

## Error handling summary

| Failure | Behavior |
|---|---|
| Slide write fails (Redis error) | Log warn, auth still succeeds |
| Slide loses Watch race | Silently skipped; next request slides |
| Revoke races with slide | Revoke wins (`pat_hash` deleted); revoke may see `TxFailedErr` and the caller retries |
| `sliding_interval` unparseable / out of bounds | 400 on create-token |
| Both `sliding_interval` and `expiration_duration` sent | 400 on create-token |
| `GET /v1/token` without Bearer | 401 |

## Testing

- `iowrappers` (miniredis mocks, extend `test/redis_client_mocks/pat_test.go`):
  - sliding token created with correct `ExpiresAt`/`RenewInterval`
  - validation before halfway mark does NOT change `ExpiresAt`
  - validation after halfway mark slides `ExpiresAt` to `now + interval`
  - revoked sliding token fails validation and does not slide
  - legacy record (no `renew_interval` in JSON) validates and never slides
- `planner` (extend `planner/nearby_places_auth_test.go` harness):
  - create-token with `sliding_interval` returns token; out-of-bounds → 400
  - `GET /v1/token` returns metadata with valid PAT; 401 without/invalid
- Full existing suite must stay green (backward compat).

## Out of scope (YAGNI)

- Credential rotation / refresh-token pairs — user explicitly chose continuity over rotation.
- JWT cookie renewal — the machine integration uses PATs only.
- Per-token scopes (existing `Scopes` field stays unused).
- Convex-side cron (deferred, section 5).
