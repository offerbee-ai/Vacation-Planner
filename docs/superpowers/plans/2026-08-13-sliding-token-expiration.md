# Sliding Token Expiration (PAT Auto-Renewal) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** PATs created with a `sliding_interval` renew themselves on use — every successful authentication in the back half of the validity window pushes `ExpiresAt` forward — so machine consumers (OfferBee Convex) never lose access while actively calling.

**Architecture:** `TokenRecord` gains a `RenewInterval` field (zero = legacy fixed expiry). `ValidatePATByHash` slides `ExpiresAt` transactionally when past the halfway mark. `/v1/create-token` accepts `sliding_interval`; new `GET /v1/token` introspects (and thereby keeps alive) the presented token. Spec: `docs/superpowers/specs/2026-08-13-sliding-token-expiration-design.md`.

**Tech Stack:** Go 1.24, gin, go-redis v9, miniredis (tests), testify.

## Global Constraints

- Tokens created without `sliding_interval` behave exactly as today; all existing tests stay green. Legacy Redis records (no `renew_interval` JSON field) unmarshal to `RenewInterval == 0` and never slide — no migration.
- The slide is best-effort: any slide failure is logged and MUST NOT fail authentication.
- Revocation always wins a race with the slide: the slide re-checks `RevokedAt` inside a `Watch` on `pat:<id>` and swallows `redis.TxFailedErr`.
- `sliding_interval` bounds: ≥ `1h` and ≤ `8760h`; mutually exclusive with `expiration_duration` (both → 400).
- `go build ./...` and `go vet ./...` clean after every task.
- Branch off `main` (current checkout `feat/apple-maps-sdk` is unrelated in-flight work — do not touch it).

## Setup note (controller)

The spec and this plan were written in the main tree while on an unrelated branch. When the worktree for this plan is created off `main`, copy both docs in and make the branch's first commit:
`docs/superpowers/specs/2026-08-13-sliding-token-expiration-design.md` + this file, message `docs: sliding token expiration design and plan`.

## File map

- Modify: `iowrappers/pat.go` (Tasks 1, 2)
- Create: `iowrappers/pat_slide_test.go` (Task 2)
- Modify: `test/redis_client_mocks/pat_test.go` (Tasks 1, 2)
- Modify: `planner/nearby_places_auth_test.go`, `planner/reclassify_buckets_dry_run_test.go` (Task 1, mechanical)
- Modify: `planner/planner.go` (Tasks 1, 3, 4)
- Create: `planner/create_token_sliding_test.go` (Task 3)
- Create: `planner/token_introspection_test.go` (Task 4)

---

### Task 1: `RenewInterval` on the data model + `NewPAT` signature

**Files:**
- Modify: `iowrappers/pat.go` (TokenRecord ~line 15, TokenMetadata ~line 27, NewPAT ~line 91, ListUserPATMetadata ~line 285)
- Modify: `planner/planner.go:1727` (NewPAT call site)
- Modify: `test/redis_client_mocks/pat_test.go` (17 NewPAT call sites + new test)
- Modify: `planner/nearby_places_auth_test.go:60-62`, `planner/reclassify_buckets_dry_run_test.go:148-150` (1 call site each)

**Interfaces:**
- Consumes: existing `TokenRecord`, `NewPAT`.
- Produces: `NewPAT(ctx context.Context, name, userId, token string, valid, renewInterval time.Duration) (*NewPATResponse, error)`; `TokenRecord.RenewInterval time.Duration`; `TokenMetadata.RenewInterval time.Duration`. Tasks 2–4 rely on these exact names.

- [ ] **Step 1: Write the failing test**

Append to `test/redis_client_mocks/pat_test.go` (add `fmt` to imports):

```go
func TestRedisClient_NewPAT_SlidingInterval(t *testing.T) {
	testUser := user.View{
		Username: "sliding_create_test_user",
		Email:    "sliding_create@example.com",
		Password: "test_password",
	}
	createdUser, err := RedisClient.CreateUser(RedisContext, testUser, false)
	require.NoError(t, err, "Failed to create test user")

	t.Run("sliding token stores RenewInterval and exposes it in metadata", func(t *testing.T) {
		_, err := RedisClient.NewPAT(RedisContext, "sliding-token", createdUser.ID, "sliding_hash_1", time.Hour, time.Hour)
		require.NoError(t, err)

		record, err := RedisClient.ValidatePATByHash(RedisContext, "sliding_hash_1")
		require.NoError(t, err)
		assert.Equal(t, time.Hour, record.RenewInterval)

		metadata, err := RedisClient.ListUserPATMetadata(RedisContext, createdUser.ID)
		require.NoError(t, err)
		found := false
		for _, meta := range metadata {
			if meta.Name == "sliding-token" {
				found = true
				assert.Equal(t, time.Hour, meta.RenewInterval)
			}
		}
		assert.True(t, found, "sliding token should appear in metadata")
	})

	t.Run("legacy record without renew_interval field validates with zero interval", func(t *testing.T) {
		// Simulates a record written before the RenewInterval field existed.
		expiry := time.Now().Add(time.Hour)
		legacyJSON := fmt.Sprintf(
			`{"id":"legacy-id-1","name":"legacy-token","hash":"legacy_hash_1","user_id":"%s","scopes":null,"created_at":"%s","expires_at":"%s","revoked_at":null}`,
			createdUser.ID, time.Now().Format(time.RFC3339Nano), expiry.Format(time.RFC3339Nano))
		require.NoError(t, RedisMockSvr.Set("pat:legacy-id-1", legacyJSON))
		require.NoError(t, RedisMockSvr.Set("pat_hash:legacy_hash_1", "legacy-id-1"))

		record, err := RedisClient.ValidatePATByHash(RedisContext, "legacy_hash_1")
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), record.RenewInterval)
		assert.True(t, record.Valid())
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./test/redis_client_mocks/ -run TestRedisClient_NewPAT_SlidingInterval -v`
Expected: FAIL to compile — "too many arguments in call to RedisClient.NewPAT" / unknown field `RenewInterval`.

- [ ] **Step 3: Implement**

In `iowrappers/pat.go`:

Add to `TokenRecord` (after `RevokedAt`):

```go
	// RenewInterval > 0 makes the token sliding: each authenticated use past the
	// halfway mark pushes ExpiresAt to now+RenewInterval. 0 = fixed expiry.
	RenewInterval time.Duration `json:"renew_interval,omitempty"`
```

Add to `TokenMetadata` (after `IsActive`):

```go
	RenewInterval time.Duration `json:"renew_interval,omitempty"`
```

Change `NewPAT` signature and record construction:

```go
func (r *RedisClient) NewPAT(ctx context.Context, name, userId, token string, valid, renewInterval time.Duration) (*NewPATResponse, error) {
	now := time.Now()
	expiresAt := now.Add(valid)
	record := TokenRecord{
		Id:            uuid.NewString(),
		Name:          name,
		Hash:          token,
		UserId:        userId,
		Scopes:        nil,
		CreatedAt:     now,
		ExpiresAt:     &expiresAt,
		RevokedAt:     nil,
		RenewInterval: renewInterval,
	}
```

In `ListUserPATMetadata`, add to the `meta` construction:

```go
			RenewInterval: token.RenewInterval,
```

Update call sites (append `, 0` as the new final argument — behavior unchanged):
- `planner/planner.go:1727`: `p.RedisClient.NewPAT(ctx, t.Name, userId, token, duration, 0)`
- `test/redis_client_mocks/pat_test.go`: all 17 existing `RedisClient.NewPAT(...)` calls
- `planner/nearby_places_auth_test.go:60-62`: the one call
- `planner/reclassify_buckets_dry_run_test.go:148-150`: the one call

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go vet ./... && go test ./test/redis_client_mocks/ ./planner/ -v`
Expected: PASS (all existing + new).

- [ ] **Step 5: Commit**

```bash
git add iowrappers/pat.go planner/planner.go test/redis_client_mocks/pat_test.go planner/nearby_places_auth_test.go planner/reclassify_buckets_dry_run_test.go
git commit -m "feat(pat): add RenewInterval to token model and NewPAT"
```

---

### Task 2: Slide on validation

**Files:**
- Modify: `iowrappers/pat.go` (`ValidatePATByHash` ~line 242; new `slidePAT` below `RevokePATByName`)
- Create: `iowrappers/pat_slide_test.go`
- Modify: `test/redis_client_mocks/pat_test.go` (new test)

**Interfaces:**
- Consumes: `TokenRecord.RenewInterval` (Task 1).
- Produces: unexported `slidePAT(ctx context.Context, tokenId string) *time.Time` (returns new expiry or nil); `ValidatePATByHash` unchanged signature, returned record carries slid `ExpiresAt` when a slide happened. Tasks 3–4 rely on the slide firing inside `ValidatePATByHash`.

- [ ] **Step 1: Write the failing black-box test**

Append to `test/redis_client_mocks/pat_test.go`:

```go
func TestRedisClient_ValidatePATByHash_SlidingRenewal(t *testing.T) {
	testUser := user.View{
		Username: "sliding_renewal_test_user",
		Email:    "sliding_renewal@example.com",
		Password: "test_password",
	}
	createdUser, err := RedisClient.CreateUser(RedisContext, testUser, false)
	require.NoError(t, err, "Failed to create test user")

	t.Run("front half of window does not slide", func(t *testing.T) {
		// 45m left of a 1h sliding window: halfway mark (30m remaining) not
		// reached. A buggy slide would push expiry to ~now+1h, which the
		// 45m ceiling below catches.
		_, err := RedisClient.NewPAT(RedisContext, "front-half-token", createdUser.ID, "front_half_hash", 45*time.Minute, time.Hour)
		require.NoError(t, err)

		record, err := RedisClient.ValidatePATByHash(RedisContext, "front_half_hash")
		require.NoError(t, err)
		assert.LessOrEqual(t, time.Until(*record.ExpiresAt), 45*time.Minute+2*time.Second,
			"expiry must not extend past the original window")
	})

	t.Run("back half of window slides expiry to now+interval", func(t *testing.T) {
		// 20m left of a 1h sliding window: past the halfway mark.
		_, err := RedisClient.NewPAT(RedisContext, "back-half-token", createdUser.ID, "back_half_hash", 20*time.Minute, time.Hour)
		require.NoError(t, err)

		record, err := RedisClient.ValidatePATByHash(RedisContext, "back_half_hash")
		require.NoError(t, err)
		// Returned record reflects the slide...
		assert.Greater(t, time.Until(*record.ExpiresAt), 55*time.Minute, "expiry should have slid to ~now+1h")

		// ...and the slide was persisted.
		persisted, err := RedisClient.ValidatePATByHash(RedisContext, "back_half_hash")
		require.NoError(t, err)
		assert.Greater(t, time.Until(*persisted.ExpiresAt), 55*time.Minute)
	})

	t.Run("fixed-expiry token never slides", func(t *testing.T) {
		_, err := RedisClient.NewPAT(RedisContext, "fixed-token", createdUser.ID, "fixed_hash", 20*time.Minute, 0)
		require.NoError(t, err)

		record, err := RedisClient.ValidatePATByHash(RedisContext, "fixed_hash")
		require.NoError(t, err)
		assert.LessOrEqual(t, time.Until(*record.ExpiresAt), 20*time.Minute+time.Second)
	})
}
```

- [ ] **Step 2: Write the failing white-box test**

Create `iowrappers/pat_slide_test.go`:

```go
package iowrappers

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPATSlideFixture(t *testing.T) (*RedisClient, *miniredis.Miniredis, context.Context) {
	t.Helper()
	redisMockSvr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(redisMockSvr.Close)

	redisURL, _ := url.Parse("redis://" + redisMockSvr.Addr())
	redisClient := CreateRedisClient(redisURL)
	if err := CreateLogger(); err != nil {
		t.Fatalf("CreateLogger: %v", err)
	}
	return redisClient, redisMockSvr, context.Background()
}

// A revoked record must never be resurrected by a slide, even when the slide
// path is reached with a stale in-memory copy (revoke racing validation).
func TestSlidePATSkipsRevokedRecord(t *testing.T) {
	redisClient, redisMockSvr, ctx := newPATSlideFixture(t)

	now := time.Now()
	expiry := now.Add(10 * time.Minute)
	revoked := now.Add(-time.Minute)
	record := TokenRecord{
		Id: "revoked-id", Name: "revoked", Hash: "revoked_hash", UserId: "u1",
		CreatedAt: now.Add(-time.Hour), ExpiresAt: &expiry, RevokedAt: &revoked,
		RenewInterval: time.Hour,
	}
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	require.NoError(t, redisMockSvr.Set("pat:revoked-id", string(raw)))

	assert.Nil(t, redisClient.slidePAT(ctx, "revoked-id"))

	stored, err := redisMockSvr.Get("pat:revoked-id")
	require.NoError(t, err)
	var after TokenRecord
	require.NoError(t, json.Unmarshal([]byte(stored), &after))
	assert.NotNil(t, after.RevokedAt, "revocation must survive")
	assert.WithinDuration(t, expiry, *after.ExpiresAt, time.Second, "expiry must not move")
}

func TestSlidePATMissingRecordIsNoop(t *testing.T) {
	redisClient, _, ctx := newPATSlideFixture(t)
	assert.Nil(t, redisClient.slidePAT(ctx, "no-such-id"))
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./test/redis_client_mocks/ -run TestRedisClient_ValidatePATByHash_SlidingRenewal -v && go test ./iowrappers/ -run TestSlidePAT -v`
Expected: FAIL — black-box "back half" case sees expiry ~20m (no slide yet); white-box fails to compile (`slidePAT` undefined).

- [ ] **Step 4: Implement**

In `iowrappers/pat.go`, add below `RevokePATByName`:

```go
// slidePAT extends a sliding token's expiration to now+RenewInterval. Best-effort:
// returns the new expiry on success, nil when the slide was skipped, lost a race,
// or failed — authentication never depends on it. Runs under Watch on the token
// record so a concurrent revoke always wins.
func (r *RedisClient) slidePAT(ctx context.Context, tokenId string) *time.Time {
	tokenKey := strings.Join([]string{"pat", tokenId}, ":")
	var newExpiry *time.Time
	err := r.Get().Watch(ctx, func(tx *redis.Tx) error {
		val, err := tx.Get(ctx, tokenKey).Result()
		if err != nil {
			return err
		}
		var record TokenRecord
		if err := json.Unmarshal([]byte(val), &record); err != nil {
			return err
		}
		// Re-check under the watch: a concurrent revoke must win, and a
		// non-sliding record must never be extended.
		if record.RevokedAt != nil || record.RenewInterval <= 0 {
			return nil
		}
		expiry := time.Now().Add(record.RenewInterval)
		record.ExpiresAt = &expiry
		save, err := json.Marshal(record)
		if err != nil {
			return err
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			return pipe.Set(ctx, tokenKey, string(save), 0).Err()
		})
		if err == nil {
			newExpiry = &expiry
		}
		return err
	}, tokenKey)
	if err != nil {
		if !errors.Is(err, redis.TxFailedErr) {
			Logger.Warnf("failed to slide PAT %s expiration: %v", tokenId, err)
		}
		return nil
	}
	return newExpiry
}
```

In `ValidatePATByHash`, after the `if !token.Valid()` block and before `return token, nil`:

```go
	// Sliding renewal: past the halfway mark, push expiry forward. Best-effort.
	if token.RenewInterval > 0 && time.Now().After(token.ExpiresAt.Add(-token.RenewInterval/2)) {
		if newExpiry := r.slidePAT(ctx, token.Id); newExpiry != nil {
			token.ExpiresAt = newExpiry
		}
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go build ./... && go vet ./... && go test ./iowrappers/ -run TestSlidePAT -v && go test ./test/redis_client_mocks/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add iowrappers/pat.go iowrappers/pat_slide_test.go test/redis_client_mocks/pat_test.go
git commit -m "feat(pat): slide sliding-token expiration on authenticated use"
```

---

### Task 3: `sliding_interval` on `/v1/create-token`

**Files:**
- Modify: `planner/planner.go` (`NewTokenInfo` ~line 1685, `createNewPAT` ~line 1694)
- Create: `planner/create_token_sliding_test.go`

**Interfaces:**
- Consumes: `NewPAT(..., valid, renewInterval)` (Task 1).
- Produces: request field `sliding_interval` (string, Go duration); 200 response gains `"renewInterval"` (e.g. `"720h0m0s"`) only for sliding tokens.

- [ ] **Step 1: Write the failing test**

Create `planner/create_token_sliding_test.go`:

```go
package planner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/weihesdlegend/Vacation-planner/test/redis_client_mocks"
	"github.com/weihesdlegend/Vacation-planner/user"
)

func TestCreateTokenSlidingInterval(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := &MyPlanner{RedisClient: redis_client_mocks.RedisClient}
	router := gin.New()
	router.POST("/v1/create-token", p.createNewPAT)

	userView, err := redis_client_mocks.RedisClient.CreateUser(
		redis_client_mocks.RedisContext,
		user.View{Username: "sliding_create_svc", Email: "sliding_create_svc@example.com", Password: "pwd", UserLevel: user.LevelStringRegular},
		false,
	)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	pat, err := redis_client_mocks.RedisClient.NewPAT(
		redis_client_mocks.RedisContext, "sliding-create-auth", userView.ID, "sliding-create-auth-token", time.Hour, 0,
	)
	if err != nil {
		t.Fatalf("failed to create auth PAT: %v", err)
	}

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/create-token", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+pat.TokenHash)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	t.Run("sliding interval accepted", func(t *testing.T) {
		w := post(`{"name": "svc-sliding", "sliding_interval": "720h"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		if resp["renewInterval"] != "720h0m0s" {
			t.Errorf("expected renewInterval 720h0m0s, got %v", resp["renewInterval"])
		}
		if resp["token"] == "" {
			t.Error("expected token in response")
		}
	})

	t.Run("mutually exclusive with expiration_duration", func(t *testing.T) {
		w := post(`{"name": "svc-both", "sliding_interval": "720h", "expiration_duration": "24h"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("invalid format rejected", func(t *testing.T) {
		w := post(`{"name": "svc-bad", "sliding_interval": "30 days"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("below minimum rejected", func(t *testing.T) {
		w := post(`{"name": "svc-short", "sliding_interval": "30m"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("above maximum rejected", func(t *testing.T) {
		w := post(`{"name": "svc-long", "sliding_interval": "8761h"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("fixed expiration still works without renewInterval in response", func(t *testing.T) {
		w := post(`{"name": "svc-fixed", "expiration_duration": "24h"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		if _, ok := resp["renewInterval"]; ok {
			t.Error("fixed token must not include renewInterval")
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./planner/ -run TestCreateTokenSlidingInterval -v`
Expected: FAIL — "sliding interval accepted" gets 200 without `renewInterval` (unknown JSON fields are ignored, token created with 5m default), mutual-exclusion case gets 200 instead of 400.

- [ ] **Step 3: Implement**

In `planner/planner.go`, extend `NewTokenInfo`:

```go
type NewTokenInfo struct {
	Name               string `json:"name"`
	ExpirationDuration string `json:"expiration_duration,omitempty"` // Optional: e.g., "24h", "168h"
	SlidingInterval    string `json:"sliding_interval,omitempty"`    // Optional: e.g., "720h"; token renews on use, 1h-8760h
}
```

In `createNewPAT`, replace the duration-parsing block (lines ~1715-1724) with:

```go
	// Parse the expiration duration, default to 5 minutes if not provided
	duration := time.Minute * 5 // Default duration
	var renewInterval time.Duration
	if t.SlidingInterval != "" && t.ExpirationDuration != "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "sliding_interval and expiration_duration are mutually exclusive"})
		return
	}
	if t.SlidingInterval != "" {
		parsedInterval, err := time.ParseDuration(t.SlidingInterval)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid sliding interval format: %s. Use formats like '720h'", t.SlidingInterval)})
			return
		}
		if parsedInterval < time.Hour || parsedInterval > 8760*time.Hour {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "sliding_interval must be between 1h and 8760h"})
			return
		}
		renewInterval = parsedInterval
		duration = parsedInterval
	} else if t.ExpirationDuration != "" {
		if parsedDuration, err := time.ParseDuration(t.ExpirationDuration); err == nil {
			duration = parsedDuration
		} else {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid expiration duration format: %s. Use formats like '24h', '7d', '30d'", t.ExpirationDuration)})
			return
		}
	}
```

Update the `NewPAT` call and response:

```go
	token := uuid.NewString()
	resp, err := p.RedisClient.NewPAT(ctx, t.Name, userId, token, duration, renewInterval)
```

```go
	response := gin.H{"name": resp.Name, "token": resp.TokenHash, "expiresAt": resp.ExpiresAt}
	if renewInterval > 0 {
		response["renewInterval"] = renewInterval.String()
	}
	ctx.JSON(http.StatusOK, response)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go vet ./... && go test ./planner/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add planner/planner.go planner/create_token_sliding_test.go
git commit -m "feat(api): accept sliding_interval on /v1/create-token"
```

---

### Task 4: `GET /v1/token` introspection endpoint

**Files:**
- Modify: `planner/planner.go` (new handler near `ListPATs` ~line 1772; route registration ~line 1857)
- Create: `planner/token_introspection_test.go`

**Interfaces:**
- Consumes: `ValidatePATByHash` with slide (Task 2), `TokenRecord.RenewInterval` (Task 1).
- Produces: `GET /v1/token` — 200 `{"name","expiresAt"(RFC3339),"valid":true[,"renewInterval"]}`, 401 otherwise. Hitting it slides a sliding token (keep-alive).

- [ ] **Step 1: Write the failing test**

Create `planner/token_introspection_test.go`:

```go
package planner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/weihesdlegend/Vacation-planner/test/redis_client_mocks"
	"github.com/weihesdlegend/Vacation-planner/user"
)

func TestGetTokenIntrospection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := &MyPlanner{RedisClient: redis_client_mocks.RedisClient}
	router := gin.New()
	router.GET("/v1/token", p.getTokenInfo)

	userView, err := redis_client_mocks.RedisClient.CreateUser(
		redis_client_mocks.RedisContext,
		user.View{Username: "introspect_svc", Email: "introspect_svc@example.com", Password: "pwd", UserLevel: user.LevelStringRegular},
		false,
	)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	get := func(authorization string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/token", nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	t.Run("no header", func(t *testing.T) {
		if w := get(""); w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 without credentials, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		if w := get("Bearer not-a-real-token"); w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 with invalid token, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("fixed token returns metadata without renewInterval", func(t *testing.T) {
		pat, err := redis_client_mocks.RedisClient.NewPAT(
			redis_client_mocks.RedisContext, "introspect-fixed", userView.ID, "introspect-fixed-token", time.Hour, 0,
		)
		if err != nil {
			t.Fatalf("failed to create PAT: %v", err)
		}

		w := get("Bearer " + pat.TokenHash)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		if resp["name"] != "introspect-fixed" {
			t.Errorf("expected name introspect-fixed, got %v", resp["name"])
		}
		if resp["valid"] != true {
			t.Errorf("expected valid true, got %v", resp["valid"])
		}
		if _, err := time.Parse(time.RFC3339, resp["expiresAt"].(string)); err != nil {
			t.Errorf("expiresAt not RFC3339: %v", resp["expiresAt"])
		}
		if _, ok := resp["renewInterval"]; ok {
			t.Error("fixed token must not include renewInterval")
		}
	})

	t.Run("sliding token past halfway slides on introspection", func(t *testing.T) {
		// 20m left of a 1h window: introspection itself should renew it.
		_, err := redis_client_mocks.RedisClient.NewPAT(
			redis_client_mocks.RedisContext, "introspect-sliding", userView.ID, "introspect-sliding-token", 20*time.Minute, time.Hour,
		)
		if err != nil {
			t.Fatalf("failed to create PAT: %v", err)
		}

		w := get("Bearer introspect-sliding-token")
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		if resp["renewInterval"] != "1h0m0s" {
			t.Errorf("expected renewInterval 1h0m0s, got %v", resp["renewInterval"])
		}
		expiresAt, err := time.Parse(time.RFC3339, resp["expiresAt"].(string))
		if err != nil {
			t.Fatalf("expiresAt not RFC3339: %v", resp["expiresAt"])
		}
		if time.Until(expiresAt) <= 55*time.Minute {
			t.Errorf("expected expiry slid to ~now+1h, got %v away", time.Until(expiresAt))
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./planner/ -run TestGetTokenIntrospection -v`
Expected: FAIL to compile — `p.getTokenInfo` undefined.

- [ ] **Step 3: Implement**

In `planner/planner.go`, add after `ListPATs` (verify `strings` is imported — add if missing):

```go
// getTokenInfo returns metadata for the PAT presented in the Authorization
// header. PAT-only introspection: validating the token also renews a sliding
// token, so this endpoint doubles as a keep-alive for idle integrations.
func (p *MyPlanner) getTokenInfo(ctx *gin.Context) {
	authHeader := ctx.Request.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
		return
	}
	tokenHash := strings.TrimPrefix(authHeader, "Bearer ")

	record, err := p.RedisClient.ValidatePATByHash(ctx, tokenHash)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired access token"})
		return
	}

	resp := gin.H{
		"name":      record.Name,
		"expiresAt": record.ExpiresAt.Format(time.RFC3339),
		"valid":     true,
	}
	if record.RenewInterval > 0 {
		resp["renewInterval"] = record.RenewInterval.String()
	}
	ctx.JSON(http.StatusOK, resp)
}
```

Register the route in `SetupRouter` after `list-tokens` (~line 1857):

```go
		v1.GET("/token", p.getTokenInfo)
```

- [ ] **Step 4: Run full suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS everywhere.

- [ ] **Step 5: Commit**

```bash
git add planner/planner.go planner/token_introspection_test.go
git commit -m "feat(api): add GET /v1/token introspection endpoint"
```

---

## Post-deploy ops (controller, after merge to main + CI deploy)

Not a code task — run per environment, prod last:

**Important:** For steps 1–2 below, authenticate with a separate admin credential (e.g., a short-lived PAT minted via `/v1/login` first, or a JWT session cookie), NOT the token being replaced. The revoked token cannot authenticate step 2.

1. `DELETE https://geo.offerbee.ai/v1/revoke-token` body `{"name": "<env-token-name>"}` with the current token as Bearer (revoke first — `NewPAT` rejects re-minting a name while the old token is valid).
2. `POST https://geo.offerbee.ai/v1/create-token` body `{"name": "<same-name>", "sliding_interval": "720h"}`.
3. From `~/code/Offerbee/packages/backend`: `CONVEX_DEPLOYMENT=<id> npx convex env set GEO_SERVICE_TOKEN <new-token>`.
4. Verify: `GET /v1/token` shows `renewInterval: 720h0m0s`, then live `POST /v1/nearby-places` returns 200.

Environments: dev `exuberant-minnow-833`, staging `adept-porpoise-776`, prod `handsome-dodo-841`.
