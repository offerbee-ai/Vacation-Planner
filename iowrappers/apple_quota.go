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
