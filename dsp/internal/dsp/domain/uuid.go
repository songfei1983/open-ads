package domain

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidUUID indicates an invalid UUID string representation.
var ErrInvalidUUID = errors.New("invalid uuid format")

// NewUUIDv7 generates a 128-bit UUID Version 7 (RFC 9562).
func NewUUIDv7() (string, error) {
	var b [16]byte
	now := time.Now().UTC().UnixMilli()

	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)

	if _, err := rand.Read(b[6:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}

	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80

	return hex.EncodeToString(b[:]), nil
}

// ValidateUUID32 validates that s is a 32-character lowercase hex UUID representation.
func ValidateUUID32(s string) error {
	s = strings.TrimSpace(s)
	if len(s) != 32 {
		return fmt.Errorf("%w: length must be 32, got %d", ErrInvalidUUID, len(s))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("%w: invalid character %q at index %d", ErrInvalidUUID, c, i)
		}
	}
	return nil
}

// TimestampFromUUIDv7 extracts the UTC timestamp from a 32-hex UUIDv7.
func TimestampFromUUIDv7(s string) (time.Time, error) {
	if err := ValidateUUID32(s); err != nil {
		return time.Time{}, err
	}
	b, err := hex.DecodeString(s[:12])
	if err != nil {
		return time.Time{}, err
	}
	var buf [8]byte
	copy(buf[2:], b)
	ms := int64(binary.BigEndian.Uint64(buf[:]))
	return time.UnixMilli(ms).UTC(), nil
}
