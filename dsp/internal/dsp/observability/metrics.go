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
	requests               atomic.Uint64
	errors                 atomic.Uint64
	durationNanos          atomic.Uint64
	buckets                [len(metricBuckets) + 1]atomic.Uint64
	decisionCandidates     atomic.Uint64
	decisionNoBids         atomic.Uint64
	decisionErrors         atomic.Uint64
	decisionTimeouts       atomic.Uint64
	decisionDurationNanos  atomic.Uint64
	decisionCount          atomic.Uint64
	snapshotLoadsTotal     atomic.Uint64
	snapshotLoadErrors     atomic.Uint64
	snapshotStalenessNanos atomic.Uint64
	// snapshotLoadDurationNanos records total nanoseconds spent loading snapshots.
	// Exported as open_ads_dsp_snapshot_load_duration_seconds_sum and count.
	snapshotLoadDurationNanos atomic.Uint64
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

// ObserveSnapshotLoad records a snapshot synchronization outcome and its staleness.
func (m *Metrics) ObserveSnapshotLoad(success bool, staleness time.Duration) {
	if success {
		m.snapshotLoadsTotal.Add(1)
		m.snapshotStalenessNanos.Store(uint64(staleness))
	} else {
		m.snapshotLoadErrors.Add(1)
	}
}

// ObserveSnapshotLoadDuration records the time taken for a snapshot load operation.
func (m *Metrics) ObserveSnapshotLoadDuration(d time.Duration) {
	m.snapshotLoadDurationNanos.Add(uint64(d))
}

// SetSnapshotStaleness updates the snapshot staleness duration.
func (m *Metrics) SetSnapshotStaleness(staleness time.Duration) {
	m.snapshotStalenessNanos.Store(uint64(staleness))
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
	fmt.Fprintf(w, "open_ads_dsp_snapshot_loads_total %d\n", m.snapshotLoadsTotal.Load())
	fmt.Fprintf(w, "open_ads_dsp_snapshot_load_errors_total %d\n", m.snapshotLoadErrors.Load())
	fmt.Fprintf(w, "open_ads_dsp_snapshot_staleness_seconds %.9f\n", float64(m.snapshotStalenessNanos.Load())/float64(time.Second))
	// Snapshot load duration metrics: sum and count (count aligns with total loads).
	fmt.Fprintf(w, "open_ads_dsp_snapshot_load_duration_seconds_sum %.9f\n", float64(m.snapshotLoadDurationNanos.Load())/float64(time.Second))
	fmt.Fprintf(w, "open_ads_dsp_snapshot_load_duration_seconds_count %d\n", m.snapshotLoadsTotal.Load())

	var cumulative uint64
	for i, bound := range metricBuckets {
		cumulative += m.buckets[i].Load()
		fmt.Fprintf(w, "open_ads_http_request_duration_seconds_bucket{le=\"%.3f\"} %d\n", bound, cumulative)
	}
	cumulative += m.buckets[len(metricBuckets)].Load()
	fmt.Fprintf(w, "open_ads_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", cumulative)
}
