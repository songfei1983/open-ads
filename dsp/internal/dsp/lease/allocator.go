package lease

import "sync"

// Allocator provides a very lightweight lease allocation mechanism used for
// budget reservation in the current iteration. It tracks a map of scope IDs
// to a boolean indicating whether the lease is currently held.
//
// The implementation is deliberately simple – it supports "all‑or‑nothing"
// semantics across multiple scopes as required by task I3‑05.
type Allocator struct {
    mu    sync.Mutex
    leases map[string]bool // true = allocated
}

// NewAllocator creates an empty allocator.
func NewAllocator() *Allocator {
    return &Allocator{leases: make(map[string]bool)}
}

// Acquire attempts to allocate leases for all provided scope IDs. If any of the
// scopes are already allocated, the call fails and returns false without
// modifying existing leases (all‑or‑nothing semantics).
func (a *Allocator) Acquire(scopes []string) bool {
    a.mu.Lock()
    defer a.mu.Unlock()
    // Check feasibility first.
    for _, s := range scopes {
        if a.leases[s] {
            return false
        }
    }
    // Allocate.
    for _, s := range scopes {
        a.leases[s] = true
    }
    return true
}

// Release frees the leases for the given scopes. It is idempotent – releasing
// an already‑released scope has no effect, satisfying the idempotency requirement.
func (a *Allocator) Release(scopes []string) {
    a.mu.Lock()
    defer a.mu.Unlock()
    for _, s := range scopes {
        delete(a.leases, s)
    }
}
