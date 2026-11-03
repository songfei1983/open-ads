package observability

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

var metricBuckets = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25}

// Metrics contains low-cardinality SSP metrics. Labels are fixed so a hostile
// request cannot create unbounded series.
type Metrics struct {
	requests      atomic.Uint64
	errors        atomic.Uint64
	audienceCalls atomic.Uint64
	durationNanos atomic.Uint64
	buckets       [len(metricBuckets) + 1]atomic.Uint64

	auctions          atomic.Uint64
	filled            atomic.Uint64
	noFill            atomic.Uint64
	dspCalls          atomic.Uint64
	dspErrors         atomic.Uint64
	dspTimeouts       atomic.Uint64
	invalidResponse   atomic.Uint64
	belowFloor        atomic.Uint64
	currencyMismatch  atomic.Uint64
	creativeRejected  atomic.Uint64
	deadlineExpired   atomic.Uint64
	noticesSent       atomic.Uint64
	noticesFailed     atomic.Uint64
	rendersServed     atomic.Uint64
	rendersFailed     atomic.Uint64
	assetsTampered    atomic.Uint64
	snapshotLoads     atomic.Uint64
	snapshotErrors    atomic.Uint64
	configSyncs       atomic.Uint64
	snapshotStaleMS   atomic.Int64
	loadDurationNanos atomic.Uint64
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

// ObserveAudienceResolve records one DMP audience resolution call.
func (m *Metrics) ObserveAudienceResolve() { m.audienceCalls.Add(1) }

// ObserveAuction records one auction outcome.
func (m *Metrics) ObserveAuction(outcome string) {
	m.auctions.Add(1)
	switch outcome {
	case "filled":
		m.filled.Add(1)
	case "no_fill":
		m.noFill.Add(1)
	}
}

// ObserveDSPCall records one bidder call outcome.
func (m *Metrics) ObserveDSPCall(outcome string) {
	m.dspCalls.Add(1)
	switch outcome {
	case "error":
		m.dspErrors.Add(1)
	case "timeout":
		m.dspTimeouts.Add(1)
	}
}

// ObserveBidRejected records why a bid was refused.
func (m *Metrics) ObserveBidRejected(reason string) {
	switch reason {
	case "below_floor":
		m.belowFloor.Add(1)
	case "currency_mismatch":
		m.currencyMismatch.Add(1)
	case "creative_not_whitelisted":
		m.creativeRejected.Add(1)
	case "deadline_expired":
		m.deadlineExpired.Add(1)
	case "invalid_response":
		m.invalidResponse.Add(1)
	}
}

// ObserveNotice records win/loss notice delivery.
func (m *Metrics) ObserveNotice(ok bool) {
	if ok {
		m.noticesSent.Add(1)
		return
	}
	m.noticesFailed.Add(1)
}

// ObserveRender records renderer outcomes.
func (m *Metrics) ObserveRender(outcome string) {
	switch outcome {
	case "served":
		m.rendersServed.Add(1)
	case "tampered":
		m.assetsTampered.Add(1)
	default:
		m.rendersFailed.Add(1)
	}
}

// ObserveSnapshotLoad records one config refresh attempt.
func (m *Metrics) ObserveSnapshotLoad(ok bool, staleness time.Duration) {
	if ok {
		m.snapshotLoads.Add(1)
		m.configSyncs.Add(1)
		if staleness < 0 {
			staleness = 0
		}
		m.snapshotStaleMS.Store(staleness.Milliseconds())
		return
	}
	m.snapshotErrors.Add(1)
}

// ObserveSnapshotLoadDuration records one manifest load duration.
func (m *Metrics) ObserveSnapshotLoadDuration(duration time.Duration) {
	if duration > 0 {
		m.loadDurationNanos.Add(uint64(duration))
	}
}

// SetSnapshotStaleness reports how long the instance has been out of sync.
func (m *Metrics) SetSnapshotStaleness(staleness time.Duration) {
	if staleness < 0 {
		staleness = 0
	}
	m.snapshotStaleMS.Store(staleness.Milliseconds())
}

// Handler exposes metrics in Prometheus text format.
func (m *Metrics) Handler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	requests := m.requests.Load()
	fmt.Fprintf(w, "open_ads_http_requests_total %d\n", requests)
	fmt.Fprintf(w, "open_ads_http_errors_total %d\n", m.errors.Load())
	fmt.Fprintf(w, "open_ads_audience_resolves_total %d\n", m.audienceCalls.Load())
	fmt.Fprintf(w, "open_ads_ssp_auctions_total %d\n", m.auctions.Load())
	fmt.Fprintf(w, "open_ads_ssp_auctions_filled_total %d\n", m.filled.Load())
	fmt.Fprintf(w, "open_ads_ssp_auctions_no_fill_total %d\n", m.noFill.Load())
	fmt.Fprintf(w, "open_ads_ssp_dsp_calls_total %d\n", m.dspCalls.Load())
	fmt.Fprintf(w, "open_ads_ssp_dsp_errors_total %d\n", m.dspErrors.Load())
	fmt.Fprintf(w, "open_ads_ssp_dsp_timeouts_total %d\n", m.dspTimeouts.Load())
	fmt.Fprintf(w, "open_ads_ssp_bids_invalid_response_total %d\n", m.invalidResponse.Load())
	fmt.Fprintf(w, "open_ads_ssp_bids_below_floor_total %d\n", m.belowFloor.Load())
	fmt.Fprintf(w, "open_ads_ssp_bids_currency_mismatch_total %d\n", m.currencyMismatch.Load())
	fmt.Fprintf(w, "open_ads_ssp_bids_creative_rejected_total %d\n", m.creativeRejected.Load())
	fmt.Fprintf(w, "open_ads_ssp_bids_deadline_expired_total %d\n", m.deadlineExpired.Load())
	fmt.Fprintf(w, "open_ads_ssp_notices_sent_total %d\n", m.noticesSent.Load())
	fmt.Fprintf(w, "open_ads_ssp_notices_failed_total %d\n", m.noticesFailed.Load())
	fmt.Fprintf(w, "open_ads_ssp_renders_served_total %d\n", m.rendersServed.Load())
	fmt.Fprintf(w, "open_ads_ssp_renders_failed_total %d\n", m.rendersFailed.Load())
	fmt.Fprintf(w, "open_ads_ssp_assets_tampered_total %d\n", m.assetsTampered.Load())
	fmt.Fprintf(w, "open_ads_ssp_snapshot_loads_total %d\n", m.snapshotLoads.Load())
	fmt.Fprintf(w, "open_ads_ssp_snapshot_load_errors_total %d\n", m.snapshotErrors.Load())
	fmt.Fprintf(w, "open_ads_ssp_config_syncs_total %d\n", m.configSyncs.Load())
	fmt.Fprintf(w, "open_ads_ssp_snapshot_staleness_seconds %.3f\n", float64(m.snapshotStaleMS.Load())/1000)
	fmt.Fprintf(w, "open_ads_ssp_snapshot_load_duration_seconds_sum %.9f\n", float64(m.loadDurationNanos.Load())/float64(time.Second))
	fmt.Fprintf(w, "open_ads_ssp_snapshot_load_duration_seconds_count %d\n", m.snapshotLoads.Load())
	fmt.Fprintf(w, "open_ads_http_request_duration_seconds_sum %.9f\n", float64(m.durationNanos.Load())/float64(time.Second))
	fmt.Fprintf(w, "open_ads_http_request_duration_seconds_count %d\n", requests)

	var cumulative uint64
	for i, bound := range metricBuckets {
		cumulative += m.buckets[i].Load()
		fmt.Fprintf(w, "open_ads_http_request_duration_seconds_bucket{le=\"%.3f\"} %d\n", bound, cumulative)
	}
	cumulative += m.buckets[len(metricBuckets)].Load()
	fmt.Fprintf(w, "open_ads_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", cumulative)
}
