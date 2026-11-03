package observability_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/observability"
)

func TestBusinessMetricsRenderBoundedSeries(t *testing.T) {
	business := observability.NewBusiness()
	metrics := observability.NewDMPMetrics(business)

	metrics.Resolve("resolved", 2, 12*time.Millisecond)
	metrics.Resolve("denied", 0, 3*time.Millisecond)
	metrics.GateAdmission("admitted")
	metrics.GateEpoch(7)
	metrics.Import(120)
	metrics.Verify("verified")
	metrics.Publish("published")
	metrics.Revocation(3)
	metrics.Retention("ok", 450, 2, 1)

	var buffer bytes.Buffer
	business.Write(&buffer)
	rendered := buffer.String()
	for _, want := range []string{
		`dmp_audience_resolve_total{result="resolved"} 1`,
		`dmp_audience_resolve_total{result="denied"} 1`,
		`dmp_audience_resolve_segments_total 2`,
		`dmp_audience_resolve_duration_seconds_bucket{le="0.005"} 1`,
		`dmp_audience_resolve_duration_seconds_bucket{le="0.025"} 2`,
		`dmp_audience_resolve_duration_seconds_bucket{le="+Inf"} 2`,
		`dmp_audience_resolve_duration_seconds_count 2`,
		`dmp_gate_admissions_total{result="admitted"} 1`,
		`dmp_gate_epoch 7`,
		`dmp_snapshot_imported_rows_total 120`,
		`dmp_snapshot_verify_total{result="verified"} 1`,
		`dmp_snapshot_publish_total{result="published"} 1`,
		`dmp_revocation_subjects_total 3`,
		`dmp_retention_sweeps_total{result="ok"} 1`,
		`dmp_retention_deleted_rows_total 450`,
		`dmp_retention_purged_subjects_total 2`,
		`dmp_retention_pending_purges 1`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("metric %q missing from:\n%s", want, rendered)
		}
	}
}

func TestMetricsHandlerPublishesBusinessSeries(t *testing.T) {
	metrics := &observability.Metrics{}
	observability.NewDMPMetrics(metrics.Business()).Resolve("empty", 0, time.Millisecond)
	metrics.Observe(http.StatusOK, 2*time.Millisecond)

	recorder := httptest.NewRecorder()
	metrics.Handler(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "open_ads_http_requests_total 1") {
		t.Fatalf("HTTP metrics missing:\n%s", body)
	}
	if !strings.Contains(body, `dmp_audience_resolve_total{result="empty"} 1`) {
		t.Fatalf("business metrics missing:\n%s", body)
	}
}
