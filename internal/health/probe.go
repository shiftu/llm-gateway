package health

import (
	"context"
	"net/http"
	"time"
)

// ProbeResult is the outcome of a single probe against an upstream URL.
type ProbeResult struct {
	Healthy    bool   // true iff Err == nil and 200 <= StatusCode < 300
	LatencyMs  int64  // round-trip duration, measured even on error
	StatusCode int    // 0 if the request never reached the server
	Err        error  // non-nil for transport / context / DNS failures
}

// Probe issues a GET against url, respecting ctx for deadline, and reports
// whether the upstream looks alive. It is the lowest-level primitive in the
// health package; callers (Manager) supply the per-provider target URL.
//
// Healthy = (response status is 2xx) AND (no transport error).
// LatencyMs is recorded even on failure so callers can distinguish "slow and
// failing" from "fast and failing."
func Probe(ctx context.Context, url string) ProbeResult {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ProbeResult{
			Healthy:   false,
			LatencyMs: time.Since(start).Milliseconds(),
			Err:       err,
		}
	}
	resp, err := http.DefaultClient.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return ProbeResult{
			Healthy:   false,
			LatencyMs: latency,
			Err:       err,
		}
	}
	defer resp.Body.Close()
	return ProbeResult{
		Healthy:    resp.StatusCode >= 200 && resp.StatusCode < 300,
		LatencyMs:  latency,
		StatusCode: resp.StatusCode,
	}
}
