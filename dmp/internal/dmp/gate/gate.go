// Package gate implements the Valkey deny gate: an epoch-scoped, atomic
// admission barrier that stops new audience lookups for a subject the moment a
// revocation starts, and lets an in-flight lookup re-check the barrier before
// it answers.
package gate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/valkey"
)

// Gate failures. Any uncertainty is reported as ErrUnavailable so callers fail
// closed instead of guessing that a subject is not revoked.
var (
	ErrUnavailable   = errors.New("deny gate unavailable")
	ErrEpochMismatch = errors.New("deny gate epoch mismatch")
	ErrSubjectDenied = errors.New("subject is revoked")
	ErrNotReady      = errors.New("deny gate is not ready")
)

// Admission outcomes.
const (
	OutcomeAdmitted = "admitted"
	OutcomeDenied   = "denied"
)

// A lookup may stay in flight for a bounded time (2s, keys expire after 4s) so a
// crashed instance cannot wedge the gate forever.
const (
	leaseTTLMillis  = "2000"
	leaseTTLBackoff = "4000"
)

// admitScript atomically checks the gate epoch and the subject deny key, then
// registers a bounded in-flight lease.
//
//	KEYS[1] epoch key, KEYS[2] in-flight key, KEYS[3] deny key
//	ARGV[1] expected epoch, ARGV[2] lease id, ARGV[3] now (ms)
//	returns {1, epoch} admitted, {0, epoch} denied, {-2, actual} epoch mismatch,
//	        {-1, ''} gate not initialised
var admitScript = `
local epoch = redis.call('GET', KEYS[1])
if not epoch then return {-1, ''} end
if epoch ~= ARGV[1] then return {-2, epoch} end
if redis.call('EXISTS', KEYS[3]) == 1 then return {0, epoch} end
redis.call('ZADD', KEYS[2], tonumber(ARGV[3]) + ` + leaseTTLMillis + `, ARGV[2])
redis.call('PEXPIRE', KEYS[2], ` + leaseTTLBackoff + `)
return {1, epoch}
`

// finalizeScript re-checks the deny key before the caller may use its result and
// always releases the lease.
//
//	KEYS[1] epoch key, KEYS[2] in-flight key, KEYS[3] deny key
//	ARGV[1] lease id, ARGV[2] expected epoch
//	returns {1} allowed, {0} denied, {-2} epoch mismatch
var finalizeScript = `
local epoch = redis.call('GET', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[1])
if not epoch then return {-2} end
if epoch ~= ARGV[2] then return {-2} end
if redis.call('EXISTS', KEYS[3]) == 1 then return {0} end
return {1}
`

// denyScript sets the deny key and returns the number of in-flight leases that
// were already registered when the revocation started.
//
//	KEYS[1] deny key, KEYS[2] in-flight key
//	ARGV[1] deny marker, ARGV[2] now (ms)
var denyScript = `
redis.call('SET', KEYS[1], ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[2])
return redis.call('ZCARD', KEYS[2])
`

// drainScript prunes expired leases and counts the ones still live.
//
//	KEYS[1] in-flight key, ARGV[1] now (ms)
var drainScript = `
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
return redis.call('ZCARD', KEYS[1])
`

// Gate is the Valkey-backed deny barrier for one tenant.
type Gate struct {
	client *valkey.Client
	tenant string
	now    func() time.Time
}

// New builds a gate for a single tenant; tenant scoping is part of every key.
func New(client *valkey.Client, tenantID string) *Gate {
	return &Gate{client: client, tenant: tenantID, now: func() time.Time { return time.Now().UTC() }}
}

func (g *Gate) epochKey() string { return "dmp:" + g.tenant + ":gate:epoch" }
func (g *Gate) inflightKey(ns, token string) string {
	return "dmp:" + g.tenant + ":gate:inflight:" + ns + ":" + token
}
func (g *Gate) denyKey(ns, token string) string {
	return "dmp:" + g.tenant + ":gate:deny:" + ns + ":" + token
}

// DenyEntry is one persisted revocation fact used to rebuild the gate.
type DenyEntry struct {
	Namespace  string
	Token      string
	ReasonCode string
}

// buildScript writes a batch of deny keys in one atomic script call.
//
//	KEYS[1..n] deny keys, ARGV[1] reason
var buildScript = `
for i = 1, #KEYS do
  redis.call('SET', KEYS[i], ARGV[1])
end
return #KEYS
`

// EnsureEpoch returns the current epoch, recreating the keyspace from the
// persisted tombstones when the epoch is missing. The epoch is only published
// after every deny key is in place, so admissions keep failing closed while the
// gate is rebuilt.
func (g *Gate) EnsureEpoch(ctx context.Context, tombstones []DenyEntry) (uint64, error) {
	current, err := g.Epoch(ctx)
	if err == nil {
		return current, nil
	}
	if !errors.Is(err, ErrNotReady) {
		return 0, err
	}
	const batchSize = 500
	for start := 0; start < len(tombstones); start += batchSize {
		end := start + batchSize
		if end > len(tombstones) {
			end = len(tombstones)
		}
		args := []string{"EVAL", buildScript, strconv.Itoa(end - start)}
		for _, entry := range tombstones[start:end] {
			args = append(args, g.denyKey(entry.Namespace, entry.Token))
		}
		reason := "rebuild"
		if tombstones[start].ReasonCode != "" {
			reason = tombstones[start].ReasonCode
		}
		args = append(args, reason)
		if _, err := g.client.Do(ctx, args...); err != nil {
			return 0, fmt.Errorf("%w: rebuild deny keys: %v", ErrUnavailable, err)
		}
	}
	if _, err := g.client.Do(ctx, "SET", g.epochKey(), "1", "NX"); err != nil {
		return 0, fmt.Errorf("%w: publish epoch: %v", ErrUnavailable, err)
	}
	epoch, err := g.Epoch(ctx)
	if err != nil {
		return 0, err
	}
	return epoch, nil
}

// Epoch returns the current gate epoch.
func (g *Gate) Epoch(ctx context.Context) (uint64, error) {
	value, err := g.client.Do(ctx, "GET", g.epochKey())
	if err != nil {
		return 0, fmt.Errorf("%w: read epoch: %v", ErrUnavailable, err)
	}
	text, ok := value.(string)
	if !ok || text == "" {
		return 0, ErrNotReady
	}
	epoch, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: epoch is invalid", ErrUnavailable)
	}
	return epoch, nil
}

// Admit registers an in-flight lease for the subject. A denied subject or an
// unusable gate is reported as an error so the caller cannot continue.
func (g *Gate) Admit(ctx context.Context, namespace, token, leaseID string) error {
	epoch, err := g.Epoch(ctx)
	if err != nil {
		return err
	}
	reply, err := g.client.Do(ctx, "EVAL", admitScript, "3",
		g.epochKey(), g.inflightKey(namespace, token), g.denyKey(namespace, token),
		strconv.FormatUint(epoch, 10), leaseID, strconv.FormatInt(g.now().UnixMilli(), 10))
	if err != nil {
		return fmt.Errorf("%w: admit: %v", ErrUnavailable, err)
	}
	code := firstInt(reply)
	switch code {
	case 1:
		return nil
	case 0:
		return ErrSubjectDenied
	case -2:
		return fmt.Errorf("%w: %s", ErrEpochMismatch, epochText(reply))
	default:
		return fmt.Errorf("%w: gate epoch is not initialised", ErrNotReady)
	}
}

// Finalize re-checks the barrier and always releases the lease. A denied answer
// must discard the resolved segments.
func (g *Gate) Finalize(ctx context.Context, namespace, token, leaseID string) error {
	epoch, err := g.Epoch(ctx)
	if err != nil {
		return err
	}
	reply, err := g.client.Do(ctx, "EVAL", finalizeScript, "3",
		g.epochKey(), g.inflightKey(namespace, token), g.denyKey(namespace, token),
		leaseID, strconv.FormatUint(epoch, 10))
	if err != nil {
		return fmt.Errorf("%w: finalize: %v", ErrUnavailable, err)
	}
	switch firstInt(reply) {
	case 1:
		return nil
	case 0:
		return ErrSubjectDenied
	default:
		return ErrEpochMismatch
	}
}

// Deny atomically denies a subject for a namespace and returns how many lookups
// were already in flight. The deny marker is kept even if later steps fail.
func (g *Gate) Deny(ctx context.Context, namespace, token, reason string) (uint64, error) {
	reply, err := g.client.Do(ctx, "EVAL", denyScript, "2",
		g.denyKey(namespace, token), g.inflightKey(namespace, token), reason,
		strconv.FormatInt(g.now().UnixMilli(), 10))
	if err != nil {
		return 0, fmt.Errorf("%w: deny: %v", ErrUnavailable, err)
	}
	count, ok := reply.(int64)
	if !ok || count < 0 {
		return 0, fmt.Errorf("%w: deny reply is invalid", ErrUnavailable)
	}
	return uint64(count), nil
}

// InFlight reports the number of live leases for a subject.
func (g *Gate) InFlight(ctx context.Context, namespace, token string) (uint64, error) {
	return g.inFlight(ctx, namespace, token)
}

func (g *Gate) inFlight(ctx context.Context, namespace, token string) (uint64, error) {
	value, err := g.client.Do(ctx, "EVAL", drainScript, "1", g.inflightKey(namespace, token),
		strconv.FormatInt(g.now().UnixMilli(), 10))
	if err != nil {
		return 0, fmt.Errorf("%w: count in-flight lookups: %v", ErrUnavailable, err)
	}
	count, ok := value.(int64)
	if !ok || count < 0 {
		return 0, fmt.Errorf("%w: in-flight count is invalid", ErrUnavailable)
	}
	return uint64(count), nil
}

// Ready reports whether the gate can serve admissions. It is used by readiness
// probes so an instance never advertises readiness with an unusable gate.
func (g *Gate) Ready(ctx context.Context) error {
	_, err := g.Epoch(ctx)
	return err
}

// ResetForTest removes the epoch marker so tests can exercise fail-closed paths.
func (g *Gate) ResetForTest(ctx context.Context) error {
	_, err := g.client.Do(ctx, "DEL", g.epochKey())
	return err
}

// KeyPrefix exposes the tenant keyspace prefix for diagnostics.
func (g *Gate) KeyPrefix() string {
	return "dmp:" + g.tenant + ":gate"
}

func firstInt(reply any) int64 {
	values, ok := reply.([]any)
	if !ok || len(values) == 0 {
		return 0
	}
	if value, ok := values[0].(int64); ok {
		return value
	}
	return 0
}

func epochText(reply any) string {
	values, ok := reply.([]any)
	if !ok || len(values) < 2 {
		return ""
	}
	if value, ok := values[1].(string); ok {
		return value
	}
	return ""
}

// NormalizeReason bounds the audit reason code to a short, log-safe value.
func NormalizeReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "user_request"
	}
	if len(reason) > 32 {
		return reason[:32]
	}
	return reason
}
