package observability

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Business holds DMP business counters and gauges. Label values come from fixed
// vocabularies (outcomes, result kinds), so cardinality stays bounded.
type Business struct {
	mu       sync.Mutex
	counters map[string]float64
	gauges   map[string]float64
	render   map[string]func(io.Writer)
}

// NewBusiness returns an empty business metric registry.
func NewBusiness() *Business {
	return &Business{counters: map[string]float64{}, gauges: map[string]float64{}, render: map[string]func(io.Writer){}}
}

func series(name, label, value string) string {
	if label == "" {
		return name
	}
	return fmt.Sprintf("%s{%s=%q}", name, label, value)
}

// Inc increments a counter by one.
func (b *Business) Inc(name, label, value string) { b.Add(name, 1, label, value) }

// Add increments a counter.
func (b *Business) Add(name string, delta float64, label, value string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.counters[series(name, label, value)] += delta
}

// Set assigns a gauge.
func (b *Business) Set(name string, value float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gauges[name] = value
}

// OnRender registers a custom renderer (for histograms) once per name, so
// sharing a registry between metric sets never duplicates a family.
func (b *Business) OnRender(name string, f func(io.Writer)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.render == nil {
		b.render = map[string]func(io.Writer){}
	}
	b.render[name] = f
}

// Write renders every series in Prometheus text format, sorted for stable output.
func (b *Business) Write(w io.Writer) {
	b.mu.Lock()
	counters := make(map[string]float64, len(b.counters))
	for key, value := range b.counters {
		counters[key] = value
	}
	gauges := make(map[string]float64, len(b.gauges))
	for key, value := range b.gauges {
		gauges[key] = value
	}
	b.mu.Unlock()

	keys := make([]string, 0, len(counters)+len(gauges))
	for key := range counters {
		keys = append(keys, key)
	}
	for key := range gauges {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, ok := counters[key]
		if !ok {
			value = gauges[key]
		}
		fmt.Fprintf(w, "%s %g\n", key, value)
	}

	b.mu.Lock()
	renderers := make([]func(io.Writer), 0, len(b.render))
	for _, f := range b.render {
		renderers = append(renderers, f)
	}
	b.mu.Unlock()
	for _, f := range renderers {
		f(w)
	}
}

// DMPMetrics records the business signals of the data plane: audience
// resolution, the revocation gate, import verification and retention.
type DMPMetrics struct {
	business *Business
	latency  []atomic.Uint64
	seconds  atomic.Uint64
	lookups  atomic.Uint64
}

// resolveLatencyBuckets are the audience-lookup latency boundaries in seconds.
var resolveLatencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25}

// NewDMPMetrics returns the DMP business metric set bound to a registry.
func NewDMPMetrics(business *Business) *DMPMetrics {
	if business == nil {
		business = NewBusiness()
	}
	m := &DMPMetrics{business: business, latency: make([]atomic.Uint64, len(resolveLatencyBuckets)+1)}
	business.OnRender("dmp_audience_resolve_duration_seconds", m.writeLatency)
	return m
}

// Registry exposes the underlying registry so /metrics can render it.
func (m *DMPMetrics) Registry() *Business { return m.business }

func (m *DMPMetrics) writeLatency(w io.Writer) {
	var cumulative uint64
	for i, bound := range resolveLatencyBuckets {
		cumulative += m.latency[i].Load()
		fmt.Fprintf(w, "dmp_audience_resolve_duration_seconds_bucket{le=\"%.3f\"} %d\n", bound, cumulative)
	}
	cumulative += m.latency[len(resolveLatencyBuckets)].Load()
	fmt.Fprintf(w, "dmp_audience_resolve_duration_seconds_bucket{le=\"+Inf\"} %d\n", cumulative)
	fmt.Fprintf(w, "dmp_audience_resolve_duration_seconds_sum %.9f\n", float64(m.seconds.Load())/1e9)
	fmt.Fprintf(w, "dmp_audience_resolve_duration_seconds_count %d\n", m.lookups.Load())
}

// Resolve records one audience lookup, its result and how many segments it
// returned.
func (m *DMPMetrics) Resolve(result string, segments int, duration time.Duration) {
	m.business.Inc("dmp_audience_resolve_total", "result", result)
	m.business.Add("dmp_audience_resolve_segments_total", float64(segments), "", "")
	m.seconds.Add(uint64(duration))
	m.lookups.Add(1)
	seconds := duration.Seconds()
	for i, bound := range resolveLatencyBuckets {
		if seconds <= bound {
			m.latency[i].Add(1)
			return
		}
	}
	m.latency[len(resolveLatencyBuckets)].Add(1)
}

// GateAdmission records one deny-gate admission decision.
func (m *DMPMetrics) GateAdmission(result string) {
	m.business.Inc("dmp_gate_admissions_total", "result", result)
}

// GateDenial records one subject moved into the deny set.
func (m *DMPMetrics) GateDenial(subjects int) {
	m.business.Add("dmp_gate_denied_subjects_total", float64(subjects), "", "")
}

// GateEpoch publishes the current gate epoch so a rebuild is visible.
func (m *DMPMetrics) GateEpoch(epoch uint64) {
	m.business.Set("dmp_gate_epoch", float64(epoch))
}

// Import records accepted membership rows.
func (m *DMPMetrics) Import(rows uint64) {
	m.business.Add("dmp_snapshot_imported_rows_total", float64(rows), "", "")
}

// Verify records one snapshot verification outcome.
func (m *DMPMetrics) Verify(result string) {
	m.business.Inc("dmp_snapshot_verify_total", "result", result)
}

// Publish records one snapshot publication outcome.
func (m *DMPMetrics) Publish(result string) {
	m.business.Inc("dmp_snapshot_publish_total", "result", result)
}

// RawIdentityRejected records a request refused because it carried a raw
// personal identifier. kind is the closed identity.Kind vocabulary.
func (m *DMPMetrics) RawIdentityRejected(kind string) {
	m.business.Inc("dmp_raw_identity_rejected_total", "kind", kind)
}

// Revocation records how many subject tokens a revocation request carried.
func (m *DMPMetrics) Revocation(subjects int) {
	m.business.Add("dmp_revocation_subjects_total", float64(subjects), "", "")
}

// Retention records one sweep and what it deleted.
func (m *DMPMetrics) Retention(result string, deletedRows uint64, purgedSubjects, pendingPurges int) {
	m.business.Inc("dmp_retention_sweeps_total", "result", result)
	m.business.Add("dmp_retention_deleted_rows_total", float64(deletedRows), "", "")
	m.business.Add("dmp_retention_purged_subjects_total", float64(purgedSubjects), "", "")
	m.business.Set("dmp_retention_pending_purges", float64(pendingPurges))
}
