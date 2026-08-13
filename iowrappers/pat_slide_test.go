package iowrappers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
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

// A slide racing a revoke must never resurrect the token or extend its life.
// Loops the race many times: a correct implementation never fails; an
// implementation without the Watch/TxPipelined optimistic lock gets caught.
func TestSlidePATNeverResurrectsRacingRevoke(t *testing.T) {
	redisClient, _, ctx := newPATSlideFixture(t)

	for i := 0; i < 100; i++ {
		// Create a sliding token past its halfway mark via the real API.
		name := fmt.Sprintf("race-token-%d", i)
		hash := fmt.Sprintf("race_hash_%d", i)
		resp, err := redisClient.NewPAT(ctx, name, "race-user", hash, 20*time.Minute, time.Hour)
		require.NoError(t, err)

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			redisClient.slidePAT(ctx, resp.TokenID)
		}()
		go func() {
			defer wg.Done()
			<-start
			assert.NoError(t, redisClient.RevokePAT(ctx, "race-user", resp.TokenID))
		}()
		close(start)
		wg.Wait()

		// Whatever the interleaving: token must not authenticate anymore...
		record, err := redisClient.ValidatePATByHash(ctx, hash)
		assert.Error(t, err, "iteration %d: revoked token must not validate", i)
		assert.Nil(t, record)

		// ...and the stored record must still be revoked.
		stored, err := redisClient.validatePATInternal(ctx, resp.TokenID)
		require.NoError(t, err)
		assert.NotNil(t, stored.RevokedAt, "iteration %d: revocation must survive the race", i)
	}
}
