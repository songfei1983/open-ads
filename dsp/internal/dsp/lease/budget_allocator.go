package lease

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
)

var (
	ErrInvalidLease        = errors.New("invalid budget lease")
	ErrLeaseExists         = errors.New("budget lease already installed")
	ErrInvalidReservation  = errors.New("invalid budget reservation")
	ErrInsufficientBudget  = errors.New("insufficient or unavailable budget lease")
	ErrReservationConflict = errors.New("reservation ID conflicts with an existing reservation")
	ErrInvalidSettlement   = errors.New("invalid reservation settlement")
)

type Outcome string

const (
	OutcomeLoss     Outcome = "loss"
	OutcomeBillable Outcome = "billable"
)

// Demand is the maximum monetary risk to reserve from one budget scope.
type Demand struct {
	ScopeID      string
	AmountMicros domain.Micros
}

// Reservation is an immutable copy of a reservation's scopes and terminal state.
type Reservation struct {
	ID           string
	Demands      []Demand
	LeaseIDs     map[string]string
	Outcome      Outcome
	ActualMicros domain.Micros
	SettledAt    time.Time
}

type localLease struct {
	id              string
	availableMicros domain.Micros
	heldMicros      domain.Micros
	expiresAt       time.Time
}

// BudgetAllocator atomically reserves amounts across the process-local view of
// budget leases. It provides single-process semantics only; production
// multi-instance coordination must use an atomic Valkey implementation.
type BudgetAllocator struct {
	mu           sync.Mutex
	leases       map[string]localLease
	reservations map[string]Reservation
}

func NewBudgetAllocator() *BudgetAllocator {
	return &BudgetAllocator{
		leases:       make(map[string]localLease),
		reservations: make(map[string]Reservation),
	}
}

// InstallLease installs a locally owned lease. Replacing leases in place is
// prohibited so existing reservations cannot lose their lease identity.
func (a *BudgetAllocator) InstallLease(scopeID, leaseID string, available domain.Micros, expiresAt time.Time) error {
	if scopeID == "" || leaseID == "" || available <= 0 || expiresAt.IsZero() {
		return ErrInvalidLease
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.leases[scopeID]; exists {
		return fmt.Errorf("%w: scope %q", ErrLeaseExists, scopeID)
	}
	a.leases[scopeID] = localLease{id: leaseID, availableMicros: available, expiresAt: expiresAt}
	return nil
}

// Reserve atomically deducts every requested scope or changes none. A repeated
// reservation ID with the same demands returns the original reservation.
func (a *BudgetAllocator) Reserve(id string, demands []Demand, now time.Time) (Reservation, error) {
	if id == "" || len(demands) == 0 {
		return Reservation{}, ErrInvalidReservation
	}
	canonical, err := canonicalDemands(demands)
	if err != nil {
		return Reservation{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if existing, ok := a.reservations[id]; ok {
		if sameDemands(existing.Demands, canonical) {
			return cloneReservation(existing), nil
		}
		return Reservation{}, fmt.Errorf("%w: %q", ErrReservationConflict, id)
	}
	leaseIDs := make(map[string]string, len(canonical))
	for _, demand := range canonical {
		lease, ok := a.leases[demand.ScopeID]
		if !ok || !now.Before(lease.expiresAt) || lease.availableMicros < demand.AmountMicros {
			return Reservation{}, fmt.Errorf("%w: scope %q", ErrInsufficientBudget, demand.ScopeID)
		}
		leaseIDs[demand.ScopeID] = lease.id
	}
	for _, demand := range canonical {
		lease := a.leases[demand.ScopeID]
		lease.availableMicros -= demand.AmountMicros
		lease.heldMicros += demand.AmountMicros
		a.leases[demand.ScopeID] = lease
	}
	reservation := Reservation{ID: id, Demands: canonical, LeaseIDs: leaseIDs}
	a.reservations[id] = reservation
	return cloneReservation(reservation), nil
}

// Settle makes loss or billable the reservation's single terminal outcome.
// Released risk is not restored to the old local lease; it returns to the
// central allocator during settlement. Replaying the same result is idempotent.
func (a *BudgetAllocator) Settle(id string, outcome Outcome, actual domain.Micros, now time.Time) (Reservation, error) {
	if id == "" || (outcome != OutcomeLoss && outcome != OutcomeBillable) || actual < 0 {
		return Reservation{}, ErrInvalidSettlement
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	reservation, ok := a.reservations[id]
	if !ok {
		return Reservation{}, fmt.Errorf("%w: unknown reservation %q", ErrInvalidSettlement, id)
	}
	if outcome == OutcomeLoss && actual != 0 {
		return Reservation{}, ErrInvalidSettlement
	}
	for _, demand := range reservation.Demands {
		if actual > demand.AmountMicros {
			return Reservation{}, fmt.Errorf("%w: actual amount exceeds reserved risk", ErrInvalidSettlement)
		}
	}
	if reservation.Outcome != "" {
		if reservation.Outcome == outcome && reservation.ActualMicros == actual {
			return cloneReservation(reservation), nil
		}
		return Reservation{}, fmt.Errorf("%w: %q", ErrReservationConflict, id)
	}
	for _, demand := range reservation.Demands {
		lease, ok := a.leases[demand.ScopeID]
		if !ok || lease.id != reservation.LeaseIDs[demand.ScopeID] || lease.heldMicros < demand.AmountMicros {
			return Reservation{}, fmt.Errorf("%w: lease state changed for scope %q", ErrInvalidSettlement, demand.ScopeID)
		}
	}
	for _, demand := range reservation.Demands {
		lease := a.leases[demand.ScopeID]
		lease.heldMicros -= demand.AmountMicros
		a.leases[demand.ScopeID] = lease
	}
	reservation.Outcome = outcome
	reservation.ActualMicros = actual
	reservation.SettledAt = now.UTC()
	a.reservations[id] = reservation
	return cloneReservation(reservation), nil
}

// Available returns the current local spendable amount for a scope.
func (a *BudgetAllocator) Available(scopeID string, now time.Time) domain.Micros {
	a.mu.Lock()
	defer a.mu.Unlock()
	lease, ok := a.leases[scopeID]
	if !ok || !now.Before(lease.expiresAt) {
		return 0
	}
	return lease.availableMicros
}

func canonicalDemands(demands []Demand) ([]Demand, error) {
	canonical := append([]Demand(nil), demands...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].ScopeID < canonical[j].ScopeID })
	for i, demand := range canonical {
		if demand.ScopeID == "" || demand.AmountMicros <= 0 {
			return nil, ErrInvalidReservation
		}
		if i > 0 && canonical[i-1].ScopeID == demand.ScopeID {
			return nil, fmt.Errorf("%w: duplicate scope %q", ErrInvalidReservation, demand.ScopeID)
		}
	}
	return canonical, nil
}

func sameDemands(a, b []Demand) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneReservation(reservation Reservation) Reservation {
	reservation.Demands = append([]Demand(nil), reservation.Demands...)
	reservation.LeaseIDs = make(map[string]string, len(reservation.LeaseIDs))
	for scope, leaseID := range reservation.LeaseIDs {
		reservation.LeaseIDs[scope] = leaseID
	}
	return reservation
}
