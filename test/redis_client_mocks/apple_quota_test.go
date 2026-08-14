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

// newTestQuotaCounter returns a counter with today's key cleared. Only the
// quota key is deleted — this package's fixtures (cities, places) are seeded
// once in init() functions and shared by every test, so a FlushAll here would
// wipe them for whichever tests happen to run later.
func newTestQuotaCounter(limit int, threshold float64, allowance int) *iowrappers.QuotaCounter {
	counter := iowrappers.NewQuotaCounter(RedisClient, iowrappers.QuotaConfig{
		DailyLimit:        limit,
		Threshold:         threshold,
		ExternalAllowance: allowance,
	})
	RedisMockSvr.Del(counter.Key())
	return counter
}

// Apple charges for every HTTP round trip, not every logical geocode: a stale
// token costs a /v1/token exchange and a 5xx costs a retry. Counting logical
// operations would under-report by a factor that varies with the failure rate,
// worst exactly when the budget is tightest. So the counter sits in the
// transport.
func TestQuotaCounterCountsEveryRoundTrip(t *testing.T) {
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
