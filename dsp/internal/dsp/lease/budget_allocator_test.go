package lease

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
)

func TestBudgetAllocatorReservesAllScopesAtomically(t *testing.T) {
	now := time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)
	allocator := NewBudgetAllocator()
	if err := allocator.InstallLease("daily", "lease-daily", 100, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := allocator.InstallLease("lifetime", "lease-life", 40, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	_, err := allocator.Reserve("reservation-fail", []Demand{
		{ScopeID: "daily", AmountMicros: 30},
		{ScopeID: "lifetime", AmountMicros: 41},
	}, now)
	if !errors.Is(err, ErrInsufficientBudget) {
		t.Fatalf("Reserve() error = %v, want ErrInsufficientBudget", err)
	}
	if got := allocator.Available("daily", now); got != 100 {
		t.Fatalf("failed multi-scope reservation changed daily balance: %d", got)
	}

	demands := []Demand{
		{ScopeID: "lifetime", AmountMicros: 40},
		{ScopeID: "daily", AmountMicros: 30},
	}
	reservation, err := allocator.Reserve("reservation-1", demands, now)
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if got := allocator.Available("daily", now); got != 70 {
		t.Fatalf("daily available = %d, want 70", got)
	}
	if got := allocator.Available("lifetime", now); got != 0 {
		t.Fatalf("lifetime available = %d, want 0", got)
	}

	replayed, err := allocator.Reserve("reservation-1", []Demand{demands[1], demands[0]}, now)
	if err != nil || replayed.ID != reservation.ID {
		t.Fatalf("idempotent Reserve() = (%+v, %v)", replayed, err)
	}
	_, err = allocator.Reserve("reservation-1", []Demand{{ScopeID: "daily", AmountMicros: 31}}, now)
	if !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("conflicting reservation replay error = %v", err)
	}
}

func TestBudgetAllocatorSettlementIsIdempotentAndFailClosed(t *testing.T) {
	now := time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)
	allocator := NewBudgetAllocator()
	if err := allocator.InstallLease("daily", "lease-daily", 100, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := allocator.Reserve("reservation-1", []Demand{{ScopeID: "daily", AmountMicros: 60}}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := allocator.Settle("reservation-1", OutcomeBillable, 61, now); !errors.Is(err, ErrInvalidSettlement) {
		t.Fatalf("over-reservation settlement error = %v", err)
	}

	settled, err := allocator.Settle("reservation-1", OutcomeLoss, 0, now)
	if err != nil || settled.Outcome != OutcomeLoss {
		t.Fatalf("loss settlement = (%+v, %v)", settled, err)
	}
	if _, err := allocator.Settle("reservation-1", OutcomeLoss, 0, now.Add(time.Second)); err != nil {
		t.Fatalf("replayed loss settlement error = %v", err)
	}
	if _, err := allocator.Settle("reservation-1", OutcomeBillable, 10, now); !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("conflicting terminal outcome error = %v", err)
	}
	if got := allocator.Available("daily", now); got != 40 {
		t.Fatalf("loss replenished old local lease, available = %d, want 40", got)
	}
	if got := allocator.Available("daily", now.Add(2*time.Minute)); got != 0 {
		t.Fatalf("expired lease available = %d, want 0", got)
	}
}

func TestBudgetAllocatorRejectsDuplicateAndNegativeDemands(t *testing.T) {
	allocator := NewBudgetAllocator()
	now := time.Now().UTC()
	_, err := allocator.Reserve("reservation-1", []Demand{
		{ScopeID: "daily", AmountMicros: domain.Micros(1)},
		{ScopeID: "daily", AmountMicros: domain.Micros(1)},
	}, now)
	if !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("duplicate scope error = %v", err)
	}
	_, err = allocator.Reserve("reservation-2", []Demand{{ScopeID: "daily", AmountMicros: -1}}, now)
	if !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("negative demand error = %v", err)
	}
}

func TestBudgetAllocatorConcurrentReservationsDoNotOverdraw(t *testing.T) {
	now := time.Now().UTC()
	allocator := NewBudgetAllocator()
	if err := allocator.InstallLease("daily", "lease-daily", 1_000, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := allocator.Reserve(fmt.Sprintf("reservation-%d", i), []Demand{{ScopeID: "daily", AmountMicros: 10}}, now)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrInsufficientBudget) {
				t.Errorf("Reserve() error = %v", err)
			}
		}(i)
	}
	wg.Wait()
	if got := successes.Load(); got != 100 {
		t.Fatalf("successful reservations = %d, want 100", got)
	}
	if got := allocator.Available("daily", now); got != 0 {
		t.Fatalf("available after concurrent reservations = %d, want 0", got)
	}
}
