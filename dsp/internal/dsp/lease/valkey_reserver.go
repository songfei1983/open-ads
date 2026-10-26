package lease

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
)

// ValkeyReserver adapts ValkeyBudgetAllocator to the Reserver interface the
// bidder decisioner consumes. The process keeps the identity of the leases it
// installed; budget amounts are authoritative in Valkey, never locally cached.
// The lease supervisor in the service entrypoint installs and renews grants.
type ValkeyReserver struct {
	allocator     *ValkeyBudgetAllocator
	leaseTTL      time.Duration
	settlementTTL time.Duration
	operationTTL  time.Duration

	mu     sync.Mutex
	grants map[string]LeaseGrant
}

// NewValkeyReserver wraps an allocator. leaseTTL defines how long installed
// leases survive without renewal, settlementTTL how long reservations must be
// settleable after a bid, and callTTL bounds each Valkey round trip.
func NewValkeyReserver(allocator *ValkeyBudgetAllocator, leaseTTL, settlementTTL, callTTL time.Duration) (*ValkeyReserver, error) {
	if allocator == nil {
		return nil, errors.New("valkey allocator is required")
	}
	if leaseTTL <= 2*callTTL || settlementTTL <= 0 {
		return nil, errors.New("valkey reserver TTLs are invalid")
	}
	return &ValkeyReserver{
		allocator:     allocator,
		leaseTTL:      leaseTTL,
		settlementTTL: settlementTTL,
		operationTTL:  callTTL,
		grants:        make(map[string]LeaseGrant),
	}, nil
}

// callCtx bounds an individual Valkey round trip; the Reserver interface
// carries no request context.
func (r *ValkeyReserver) callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), r.operationTTL)
}

// InstallLease installs a new owned lease or, when the scope is already owned,
// only extends its expiry so in-flight holds are preserved. Amounts come from
// the supervisor's database view; field drift repair is stage-two work.
func (r *ValkeyReserver) InstallLease(ctx context.Context, grant LeaseGrant) error {
	if grant.ScopeID == "" || grant.LeaseID == "" || grant.Available < 0 || grant.Held != 0 || grant.ExpiresAt.IsZero() {
		return ErrInvalidLease
	}
	r.mu.Lock()
	_, installed := r.grants[grant.ScopeID]
	// Refresh the local snapshot (in particular ExpiresAt) on every renewal;
	// only the Valkey key TTL alone would leave stale local expiry state.
	r.grants[grant.ScopeID] = grant
	r.mu.Unlock()
	if installed {
		return r.renew(ctx, grant.ScopeID, grant.ExpiresAt)
	}
	return r.allocator.Install(ctx, grant)
}

func (r *ValkeyReserver) renew(ctx context.Context, scopeID string, expiresAt time.Time) error {
	r.mu.Lock()
	leaseID := r.grants[scopeID].LeaseID
	r.mu.Unlock()
	return r.allocator.RenewLease(ctx, scopeID, leaseID, expiresAt)
}

// Reserve satisfies lease.Reserver for the decisioner hot path.
func (r *ValkeyReserver) Reserve(id string, demands []Demand, now time.Time) (Reservation, error) {
	if len(demands) == 0 {
		return Reservation{}, ErrInvalidReservation
	}
	grants := make(map[string]LeaseGrant, len(r.grants))
	r.mu.Lock()
	for scope, grant := range r.grants {
		grants[scope] = grant
	}
	r.mu.Unlock()
	ctx, cancel := r.callCtx()
	defer cancel()
	// The settlement window must outlive the reservation so win/loss notices
	// can always find the booked risk.
	return r.allocator.Reserve(ctx, id, demands, grants, now.Add(r.settlementTTL))
}

// Settle satisfies lease.Reserver for the notice endpoints.
func (r *ValkeyReserver) Settle(id string, outcome Outcome, actual domain.Micros, now time.Time) (Reservation, error) {
	ctx, cancel := r.callCtx()
	defer cancel()
	reservation, err := r.allocator.LoadReservation(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	return r.allocator.Settle(ctx, reservation, outcome, actual, now)
}

// Available satisfies lease.Reserver and reports the live Valkey balance of an
// owned scope; scopes this instance does not own report zero (fail-closed).
func (r *ValkeyReserver) Available(scopeID string, now time.Time) domain.Micros {
	r.mu.Lock()
	grant, ok := r.grants[scopeID]
	r.mu.Unlock()
	if !ok || leaseExpired(grant, now) {
		return 0
	}
	ctx, cancel := r.callCtx()
	defer cancel()
	available, err := r.allocator.ScopeAvailable(ctx, scopeID, grant.LeaseID)
	if err != nil || available < 0 {
		return 0
	}
	return available
}

func leaseExpired(grant LeaseGrant, now time.Time) bool {
	return grant.ExpiresAt.IsZero() || grant.ExpiresAt.UnixMilli() <= now.UnixMilli()
}
