package domain_test

import (
	"testing"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/domain"
)

func TestRoundHalfEven(t *testing.T) {
	cases := []struct {
		in   float64
		want int64
	}{
		{2.5, 2},   // nearest even
		{3.5, 4},   // nearest even
		{2.4, 2},
		{2.6, 3},
		{-2.5, -2},
		{-3.5, -4},
		{0.0, 0},
	}

	for _, tc := range cases {
		got := domain.RoundHalfEven(tc.in)
		if got != tc.want {
			t.Errorf("RoundHalfEven(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCalculateBillableFromCPM(t *testing.T) {
	// 2.50 USD CPM = 2,500,000 micros
	// For quantity 1: 2500000 * 1 / 1000 = 2500 micros
	cpm := domain.Micros(2_500_000)
	got, err := domain.CalculateBillableFromCPM(cpm, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2500 {
		t.Errorf("expected 2500 micros, got %d", got)
	}

	// Negative CPM should return error
	if _, err := domain.CalculateBillableFromCPM(-100, 1); err == nil {
		t.Errorf("expected error on negative CPM, got nil")
	}

	// Negative quantity should return error
	if _, err := domain.CalculateBillableFromCPM(cpm, -1); err == nil {
		t.Errorf("expected error on negative quantity, got nil")
	}

	// Quantity 0 should return 0
	zero, err := domain.CalculateBillableFromCPM(cpm, 0)
	if err != nil || zero != 0 {
		t.Errorf("expected 0 for quantity 0, got %d, err: %v", zero, err)
	}
}
