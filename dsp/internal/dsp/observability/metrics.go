package observability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

var metricBuckets = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25}

// Metrics contains low-cardinality HTTP service metrics.
type Metrics struct {
	requests              atomic.Uint64
	errors                atomic.Uint64
	durationNanos         atomic.Uint64
	buckets               [len(metricBuckets) + 1]atomic.Uint64
	decisionCandidates    atomic.Uint64
	decisionNoBids        atomic.Uint64
	decisionErrors        atomic.Uint64
	decisionTimeouts      atomic.Uint64
	decisionDurationNanos atomic.Uint64
	decisionCount         atomic.Uint64
}

// Observe records one completed HTTP request.
func (m *Metrics) Observe(status int, duration time.Duration) {
	m.requests.Add(1)
	if status >= http.StatusInternalServerError {
		m.errors.Add(1)
	}
	m.durationNanos.Add(uint64(duration))
	seconds := duration.Seconds()
	for i, bound := range metricBuckets {
		if seconds <= bound {
			m.buckets[i].Add(1)
			return
		}
	}
	m.buckets[len(metricBuckets)].Add(1)
}

// ObserveDecision records fixed-cardinality decision outcomes without request
// identifiers or other caller-controlled labels.
func (m *Metrics) ObserveDecision(candidate, noBid bool, err error, duration time.Duration) {
	if candidate {
		m.decisionCandidates.Add(1)
	}
	if noBid {
		m.decisionNoBids.Add(1)
	}
	if err != nil {
		m.decisionErrors.Add(1)
		if errors.Is(err, context.DeadlineExceeded) {
			m.decisionTimeouts.Add(1)
		}
	}
	m.decisionDurationNanos.Add(uint64(duration))
	m.decisionCount.Add(1)
}

// Handler exposes metrics in Prometheus text format.
func (m *Metrics) Handler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	requests := m.requests.Load()
	fmt.Fprintf(w, "open_ads_http_requests_total %d\n", requests)
	fmt.Fprintf(w, "open_ads_http_errors_total %d\n", m.errors.Load())
	fmt.Fprintf(w, "open_ads_http_request_duration_seconds_sum %.9f\n", float64(m.durationNanos.Load())/float64(time.Second))
	fmt.Fprintf(w, "open_ads_http_request_duration_seconds_count %d\n", requests)
	fmt.Fprintf(w, "open_ads_dsp_decision_candidates_total %d\n", m.decisionCandidates.Load())
	fmt.Fprintf(w, "open_ads_dsp_decision_no_bids_total %d\n", m.decisionNoBids.Load())
	fmt.Fprintf(w, "open_ads_dsp_decision_errors_total %d\n", m.decisionErrors.Load())
	fmt.Fprintf(w, "open_ads_dsp_decision_timeouts_total %d\n", m.decisionTimeouts.Load())
	fmt.Fprintf(w, "open_ads_dsp_decision_duration_seconds_sum %.9f\n", float64(m.decisionDurationNanos.Load())/float64(time.Second))
	fmt.Fprintf(w, "open_ads_dsp_decision_duration_seconds_count %d\n", m.decisionCount.Load())

	var cumulative uint64
	for i, bound := range metricBuckets {
		cumulative += m.buckets[i].Load()
		fmt.Fprintf(w, "open_ads_http_request_duration_seconds_bucket{le=\"%.3f\"} %d\n", bound, cumulative)
	}
	cumulative += m.buckets[len(metricBuckets)].Load()
	fmt.Fprintf(w, "open_ads_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", cumulative)
}
