package domain_test

import (
	"testing"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
)

func TestCalculateSpendFromCPM(t *testing.T) {
	cpm := domain.Micros(2_500_000) // $2.50 CPM
	got, err := domain.CalculateSpendFromCPM(cpm, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2500 {
		t.Errorf("expected 2500 micros, got %d", got)
	}
}
