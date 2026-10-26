package lease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/valkey"
)

const maxLuaSafeMicros domain.Micros = 1<<53 - 1

const reserveBudgetScript = `
local existing = redis.call('GET', KEYS[1])
if existing then
  local decoded = cjson.decode(existing)
  if decoded.fingerprint ~= ARGV[1] then return redis.error_reply('RESERVATION_CONFLICT') end
  return existing
end
local now_ms = tonumber(ARGV[2])
local count = tonumber(ARGV[3])
for i = 1, count do
  local key = KEYS[i + 1]
  local expires = tonumber(redis.call('HGET', key, 'expires_ms') or '0')
  local available = tonumber(redis.call('HGET', key, 'available_micros') or '0')
  local requested = tonumber(ARGV[3 + i])
  if expires <= now_ms or available < requested then return redis.error_reply('INSUFFICIENT_BUDGET') end
end
for i = 1, count do
  local key = KEYS[i + 1]
  local requested = tonumber(ARGV[3 + i])
  redis.call('HINCRBY', key, 'available_micros', -requested)
  redis.call('HINCRBY', key, 'held_micros', requested)
end
redis.call('SET', KEYS[1], ARGV[4 + count], 'PX', ARGV[4 + count + 1])
return ARGV[4 + count]
`

const settleBudgetScript = `
local encoded = redis.call('GET', KEYS[1])
if not encoded then return redis.error_reply('RESERVATION_NOT_FOUND') end
local stored = cjson.decode(encoded)
if stored.fingerprint ~= ARGV[4] then return redis.error_reply('RESERVATION_CONFLICT') end
local reservation = stored.reservation
local outcome = ARGV[1]
local actual = tonumber(ARGV[2])
if reservation.outcome and reservation.outcome ~= '' then
  if reservation.outcome == outcome and tonumber(reservation.actual_micros) == actual then return encoded end
  return redis.error_reply('RESERVATION_CONFLICT')
end
if outcome == 'loss' and actual ~= 0 then return redis.error_reply('INVALID_SETTLEMENT') end
for i, demand in ipairs(reservation.demands) do
  if actual > tonumber(demand.amount_micros) then return redis.error_reply('INVALID_SETTLEMENT') end
  local held = tonumber(redis.call('HGET', KEYS[i + 1], 'held_micros') or '0')
  if held < tonumber(demand.amount_micros) then return redis.error_reply('INVALID_LEASE_STATE') end
end
for i, demand in ipairs(reservation.demands) do
  redis.call('HINCRBY', KEYS[i + 1], 'held_micros', -tonumber(demand.amount_micros))
end
reservation.outcome = outcome
reservation.actual_micros = actual
reservation.settled_at = ARGV[3]
stored.reservation = reservation
local updated = cjson.encode(stored)
redis.call('SET', KEYS[1], updated, 'KEEPTTL')
return updated
`

// LeaseGrant is an instance-owned Valkey lease installed by the background allocator.
type LeaseGrant struct {
	ScopeID   string
	LeaseID   string
	Available domain.Micros
	Held      domain.Micros
	ExpiresAt time.Time
}

// ValkeyBudgetAllocator performs cross-scope reservations in one Lua operation.
type ValkeyBudgetAllocator struct {
	client *valkey.Client
	tenant string
	owner  string
}

func NewValkeyBudgetAllocator(client *valkey.Client, tenant, owner string) (*ValkeyBudgetAllocator, error) {
	if client == nil || tenant == "" || owner == "" {
		return nil, errors.New("valkey client, tenant, and owner are required")
	}
	return &ValkeyBudgetAllocator{client: client, tenant: tenant, owner: owner}, nil
}

func (a *ValkeyBudgetAllocator) Install(ctx context.Context, grant LeaseGrant) error {
	if grant.ScopeID == "" || grant.LeaseID == "" || grant.Available < 0 || grant.Available > maxLuaSafeMicros || grant.Held < 0 || grant.Held > maxLuaSafeMicros || grant.ExpiresAt.IsZero() {
		return ErrInvalidLease
	}
	key := a.leaseKey(grant.ScopeID, grant.LeaseID)
	_, err := a.client.Do(ctx, "HSET", key,
		"lease_id", grant.LeaseID,
		"owner_instance", a.owner,
		"available_micros", strconv.FormatInt(int64(grant.Available), 10),
		"held_micros", strconv.FormatInt(int64(grant.Held), 10),
		"expires_ms", strconv.FormatInt(grant.ExpiresAt.UnixMilli(), 10))
	if err != nil {
		return fmt.Errorf("install Valkey budget lease: %w", err)
	}
	if _, err := a.client.Do(ctx, "PEXPIREAT", key, strconv.FormatInt(grant.ExpiresAt.UnixMilli(), 10)); err != nil {
		return fmt.Errorf("set Valkey budget lease expiry: %w", err)
	}
	return nil
}

func (a *ValkeyBudgetAllocator) Reserve(ctx context.Context, id string, demands []Demand, grants map[string]LeaseGrant, expiresAt time.Time) (Reservation, error) {
	if id == "" || len(demands) == 0 || len(demands) > 64 || !expiresAt.After(time.Now()) {
		return Reservation{}, ErrInvalidReservation
	}
	canonical, err := canonicalDemands(demands)
	if err != nil {
		return Reservation{}, err
	}
	reservation := Reservation{ID: id, Demands: canonical, LeaseIDs: make(map[string]string, len(canonical))}
	keys := []string{a.reservationKey(id)}
	args := []string{strconv.FormatInt(time.Now().UnixMilli(), 10), strconv.Itoa(len(canonical))}
	for _, demand := range canonical {
		if demand.AmountMicros > maxLuaSafeMicros {
			return Reservation{}, ErrInvalidReservation
		}
		grant, ok := grants[demand.ScopeID]
		if !ok || grant.ScopeID != demand.ScopeID || grant.LeaseID == "" {
			return Reservation{}, fmt.Errorf("%w: missing lease for scope %q", ErrInsufficientBudget, demand.ScopeID)
		}
		keys = append(keys, a.leaseKey(demand.ScopeID, grant.LeaseID))
		args = append(args, strconv.FormatInt(int64(demand.AmountMicros), 10))
		reservation.LeaseIDs[demand.ScopeID] = grant.LeaseID
	}
	fingerprint := reservationFingerprint(canonical, reservation.LeaseIDs)
	payload, err := json.Marshal(struct {
		Fingerprint string      `json:"fingerprint"`
		Reservation Reservation `json:"reservation"`
	}{hex.EncodeToString(fingerprint[:]), reservation})
	if err != nil {
		return Reservation{}, err
	}
	ttl := time.Until(expiresAt).Milliseconds()
	args = append([]string{hex.EncodeToString(fingerprint[:])}, args...)
	args = append(args, string(payload), strconv.FormatInt(ttl, 10))
	command := []string{"EVAL", reserveBudgetScript, strconv.Itoa(len(keys))}
	command = append(command, keys...)
	command = append(command, args...)
	result, err := a.client.Do(ctx, command...)
	if err != nil {
		if strings.Contains(err.Error(), "RESERVATION_CONFLICT") {
			return Reservation{}, ErrReservationConflict
		}
		if strings.Contains(err.Error(), "INSUFFICIENT_BUDGET") {
			return Reservation{}, ErrInsufficientBudget
		}
		return Reservation{}, fmt.Errorf("reserve Valkey budget: %w", err)
	}
	serialized, ok := result.(string)
	if !ok {
		return Reservation{}, fmt.Errorf("unexpected Valkey reservation response %T", result)
	}
	var stored struct {
		Fingerprint string      `json:"fingerprint"`
		Reservation Reservation `json:"reservation"`
	}
	if err := json.Unmarshal([]byte(serialized), &stored); err != nil {
		return Reservation{}, fmt.Errorf("decode Valkey reservation: %w", err)
	}
	if stored.Fingerprint != hex.EncodeToString(fingerprint[:]) {
		return Reservation{}, ErrReservationConflict
	}
	return stored.Reservation, nil
}

// Settle atomically records one terminal outcome and releases held risk from all lease scopes.
func (a *ValkeyBudgetAllocator) Settle(ctx context.Context, reservation Reservation, outcome Outcome, actual domain.Micros, now time.Time) (Reservation, error) {
	if reservation.ID == "" || (outcome != OutcomeLoss && outcome != OutcomeBillable) || actual < 0 || actual > maxLuaSafeMicros || outcome == OutcomeLoss && actual != 0 {
		return Reservation{}, ErrInvalidSettlement
	}
	demands, err := canonicalDemands(reservation.Demands)
	if err != nil {
		return Reservation{}, err
	}
	keys := []string{a.reservationKey(reservation.ID)}
	for _, demand := range demands {
		leaseID := reservation.LeaseIDs[demand.ScopeID]
		if leaseID == "" {
			return Reservation{}, ErrInvalidSettlement
		}
		keys = append(keys, a.leaseKey(demand.ScopeID, leaseID))
	}
	command := []string{"EVAL", settleBudgetScript, strconv.Itoa(len(keys))}
	command = append(command, keys...)
	fingerprint := reservationFingerprint(demands, reservation.LeaseIDs)
	command = append(command, string(outcome), strconv.FormatInt(int64(actual), 10), now.UTC().Format(time.RFC3339Nano), hex.EncodeToString(fingerprint[:]))
	result, err := a.client.Do(ctx, command...)
	if err != nil {
		if strings.Contains(err.Error(), "RESERVATION_CONFLICT") {
			return Reservation{}, ErrReservationConflict
		}
		if strings.Contains(err.Error(), "RESERVATION_NOT_FOUND") || strings.Contains(err.Error(), "INVALID_LEASE_STATE") {
			return Reservation{}, ErrInvalidSettlement
		}
		return Reservation{}, fmt.Errorf("settle Valkey budget reservation: %w", err)
	}
	serialized, ok := result.(string)
	if !ok {
		return Reservation{}, fmt.Errorf("unexpected Valkey settlement response %T", result)
	}
	var stored struct {
		Reservation Reservation `json:"reservation"`
	}
	if err := json.Unmarshal([]byte(serialized), &stored); err != nil {
		return Reservation{}, fmt.Errorf("decode Valkey settlement: %w", err)
	}
	return stored.Reservation, nil
}

func (a *ValkeyBudgetAllocator) leaseKey(scope, leaseID string) string {
	digest := sha256.Sum256([]byte(scope))
	return "dsp:budget:lease:{" + a.tenant + "}:" + a.owner + ":" + hex.EncodeToString(digest[:]) + ":" + leaseID
}

func (a *ValkeyBudgetAllocator) reservationKey(id string) string {
	digest := sha256.Sum256([]byte(id))
	return "dsp:budget:reservation:{" + a.tenant + "}:" + hex.EncodeToString(digest[:])
}

func reservationFingerprint(demands []Demand, leaseIDs map[string]string) [32]byte {
	parts := make([]string, 0, len(demands))
	for _, demand := range demands {
		parts = append(parts, demand.ScopeID+":"+leaseIDs[demand.ScopeID]+":"+strconv.FormatInt(int64(demand.AmountMicros), 10))
	}
	return sha256.Sum256([]byte(fmt.Sprint(parts)))
}

// RenewLease refreshes an owned lease: both the authoritative expires_ms
// field (checked by the reserve script) and the key TTL must advance, so a
// renewed lease does not silently fail budget checks 30s after install.
func (a *ValkeyBudgetAllocator) RenewLease(ctx context.Context, scopeID, leaseID string, expiresAt time.Time) error {
	key := a.leaseKey(scopeID, leaseID)
	if _, err := a.client.Do(ctx, "HSET", key, "expires_ms", strconv.FormatInt(expiresAt.UnixMilli(), 10)); err != nil {
		return fmt.Errorf("refresh Valkey budget lease field: %w", err)
	}
	if _, err := a.client.Do(ctx, "PEXPIREAT", key, strconv.FormatInt(expiresAt.UnixMilli(), 10)); err != nil {
		return fmt.Errorf("renew Valkey budget lease: %w", err)
	}
	return nil
}

// ScopeAvailable reports the remaining spendable micros of one owned lease.
// Unknown or expired leases report zero (fail-closed).
func (a *ValkeyBudgetAllocator) ScopeAvailable(ctx context.Context, scopeID, leaseID string) (domain.Micros, error) {
	key := a.leaseKey(scopeID, leaseID)
	result, err := a.client.Do(ctx, "HGET", key, "available_micros")
	if err != nil {
		return 0, nil // Treat transport problems as no capacity; callers fail closed.
	}
	value, ok := result.(string)
	if !ok || value == "" {
		return 0, nil
	}
	available, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("decode Valkey lease available micros: %w", err)
	}
	return domain.Micros(available), nil
}

// LoadReservation reads a stored reservation for later settlement.
func (a *ValkeyBudgetAllocator) LoadReservation(ctx context.Context, id string) (Reservation, error) {
	key := a.reservationKey(id)
	result, err := a.client.Do(ctx, "GET", key)
	if err != nil || result == nil {
		if result == nil {
			return Reservation{}, ErrInvalidSettlement
		}
		return Reservation{}, fmt.Errorf("read Valkey reservation: %w", err)
	}
	serialized, ok := result.(string)
	if !ok {
		return Reservation{}, fmt.Errorf("unexpected Valkey reservation response %T", result)
	}
	var stored struct {
		Fingerprint string      `json:"fingerprint"`
		Reservation Reservation `json:"reservation"`
	}
	if err := json.Unmarshal([]byte(serialized), &stored); err != nil {
		return Reservation{}, fmt.Errorf("decode Valkey reservation: %w", err)
	}
	return stored.Reservation, nil
}
