// Package ledger owns the SSP financial facts: one delivery event, one
// publisher payable entry and one durable billable outbox row per billable
// impression, all committed in a single MariaDB transaction.
package ledger

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

// Event types recorded in ssp_delivery_event.
const (
	EventTypeImpression = "impression"
)

// SourceEventKey derives the stable, unbounded-collision key defined by the
// LLD: SHA-256 over length-prefixed fields, never a raw ":" join. Retries reuse
// the key, so it must depend only on immutable identifiers.
func SourceEventKey(tenantID, auctionID, bidID, impID, eventType string, sequence uint32) [32]byte {
	digest := sha256.New()
	writeField := func(value string) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		digest.Write(length[:])
		digest.Write([]byte(value))
	}
	digest.Write([]byte("openads.event.v1"))
	writeField(normalizeID(tenantID))
	writeField(auctionID)
	writeField(bidID)
	writeField(impID)
	writeField(eventType)
	var seq [4]byte
	binary.BigEndian.PutUint32(seq[:], sequence)
	digest.Write(seq[:])
	var key [32]byte
	copy(key[:], digest.Sum(nil))
	return key
}

// PayableIdempotencyKey is derived from the delivery event key so a replay of
// the same impression can never create a second payable entry.
func PayableIdempotencyKey(sourceEventKey [32]byte) [32]byte {
	digest := sha256.New()
	digest.Write([]byte("openads.payable.v1"))
	digest.Write(sourceEventKey[:])
	var key [32]byte
	copy(key[:], digest.Sum(nil))
	return key
}

func normalizeID(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
}

func encodeID(value string) ([]byte, bool) {
	clean := normalizeID(value)
	if len(clean) != 32 {
		return nil, false
	}
	decoded, err := hex.DecodeString(clean)
	if err != nil {
		return nil, false
	}
	return decoded, true
}
