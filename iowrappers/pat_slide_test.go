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
