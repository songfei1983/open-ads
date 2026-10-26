package lease

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
)

func TestSQLBudgetStoreValidation(t *testing.T) {
	_, err := NewSQLBudgetStore(nil, "018f7a3b2c9d7e1fa3b4c5d6e7f89010")
	if err == nil {
		t.Fatal("expected error with nil db")
	}

	// Invalid tenant
	_, err = NewSQLBudgetStore(nil, "")
	if err == nil {
		t.Fatal("expected error with empty tenant")
	}
}

func TestNormalizePeriodStart(t *testing.T) {
	testTime := time.Date(2026, time.October, 25, 14, 30, 45, 123456, time.FixedZone("CST", 8*3600))
	daily := normalizePeriodStart("daily", testTime)
	if daily.Hour() != 0 || daily.Minute() != 0 || daily.Second() != 0 || daily.Nanosecond() != 0 {
		t.Fatalf("expected midnight, got %v", daily)
	}
	if daily.Location() != time.UTC {
		t.Fatalf("expected UTC location, got %v", daily.Location())
	}

	lifetime := normalizePeriodStart("lifetime", testTime)
	if lifetime.Year() != 1970 || lifetime.Month() != 1 || lifetime.Day() != 1 {
		t.Fatalf("expected 1970-01-01, got %v", lifetime)
	}
}

func TestNormalizeUUIDAndBytes(t *testing.T) {
	b1, err := normalizeUUIDBytes("018f7a3b2c9d7e1fa3b4c5d6e7f89010")
	if err != nil || len(b1) != 16 {
		t.Fatalf("expected 16 bytes, got len %d, err %v", len(b1), err)
	}

	// With prefix
	b2, err := normalizeUUIDBytes("res-018f7a3b2c9d7e1fa3b4c5d6e7f89010")
	if err != nil || len(b2) != 16 {
		t.Fatalf("expected 16 bytes for prefixed UUID, got len %d, err %v", len(b2), err)
	}

	// 32-byte normalization
	b32 := normalize32Bytes("some-arbitrary-string")
	if len(b32) != 32 {
		t.Fatalf("expected 32 bytes, got len %d", len(b32))
	}
}

func TestSettleReservationInputValidation(t *testing.T) {
	store := &SQLBudgetStore{
		tenantID: make([]byte, 16),
	}
	ctx := context.Background()

	// Missing reservation ID
	err := store.SettleReservation(ctx, SettlementParams{
		Outcome: OutcomeLoss,
	})
	if !errors.Is(err, ErrInvalidSettlement) {
		t.Fatalf("expected ErrInvalidSettlement on missing reservation ID, got %v", err)
	}

	// Invalid outcome
	err = store.SettleReservation(ctx, SettlementParams{
		ReservationID: "018f7a3b2c9d7e1fa3b4c5d6e7f89010",
		Outcome:       "unknown",
	})
	if !errors.Is(err, ErrInvalidSettlement) {
		t.Fatalf("expected ErrInvalidSettlement on unknown outcome, got %v", err)
	}

	// Loss with non-zero amount
	err = store.SettleReservation(ctx, SettlementParams{
		ReservationID: "018f7a3b2c9d7e1fa3b4c5d6e7f89010",
		Outcome:       OutcomeLoss,
		ActualAmount:  100,
	})
	if !errors.Is(err, ErrInvalidSettlement) {
		t.Fatalf("expected ErrInvalidSettlement on loss with non-zero actual amount, got %v", err)
	}
}

func TestEnsureAccountAndAllocateValidation(t *testing.T) {
	store := &SQLBudgetStore{
		tenantID: make([]byte, 16),
	}
	ctx := context.Background()

	// Negative limit
	err := store.EnsureAccount(ctx, "018f7a3b2c9d7e1fa3b4c5d6e7f89010", "daily", time.Now(), "USD", domain.Micros(-10))
	if !errors.Is(err, domain.ErrNegativeAmount) {
		t.Fatalf("expected ErrNegativeAmount, got %v", err)
	}

	// Allocate with negative or zero requested
	_, err = store.AllocateLease(ctx, "018f7a3b2c9d7e1fa3b4c5d6e7f89010", "daily", time.Now(), "inst-1", 0, time.Hour)
	if !errors.Is(err, ErrInvalidLease) {
		t.Fatalf("expected ErrInvalidLease on zero requested, got %v", err)
	}
}
