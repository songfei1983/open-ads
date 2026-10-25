package observability

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecisionMetricsUseFixedOutcomeNames(t *testing.T) {
	metrics := &Metrics{}
	metrics.ObserveDecision(true, true, context.DeadlineExceeded, 12*time.Millisecond)

	response := httptest.NewRecorder()
	metrics.Handler(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	for _, want := range []string{
		"open_ads_dsp_decision_candidates_total 1",
		"open_ads_dsp_decision_no_bids_total 1",
		"open_ads_dsp_decision_errors_total 1",
		"open_ads_dsp_decision_timeouts_total 1",
		"open_ads_dsp_decision_duration_seconds_count 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics response missing %q", want)
		}
	}
	if strings.Contains(body, "request_id") {
		t.Fatal("decision metrics must not expose request identifiers")
	}
}
