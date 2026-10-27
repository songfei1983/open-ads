// Package identity holds the single pseudonymous-token policy every Open Ads
// service applies before storing or resolving an audience identifier. Raw
// identifiers (e-mail addresses, phone numbers, device ids, cookies) never match
// it, so a caller cannot smuggle personal data into the data plane.
package identity

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	// ErrInvalidToken reports a value that is not an acceptable pseudonymous
	// token.
	ErrInvalidToken = errors.New("invalid subject token")
	// ErrRawIdentity reports a value that looks like a raw personal identifier.
	ErrRawIdentity = errors.New("raw identity rejected")
)

// tokenPattern is the only accepted shape: 64 lowercase hex characters, i.e. an
// HMAC-SHA256 (or equivalent) digest.
var tokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// uuidPattern matches a bare UUID, which is an identifier rather than a digest.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Kind classifies a value. The set is closed so it can be used as a metric label;
// the empty kind means "acceptable token".
type Kind string

// Rejection kinds.
const (
	KindEmail       Kind = "email"
	KindPhone       Kind = "phone"
	KindUUID        Kind = "uuid"
	KindDeviceID    Kind = "device_id"
	KindWrongLength Kind = "wrong_length"
	KindNotHex      Kind = "not_hex"
	KindEmpty       Kind = "empty"
)

// Error is a refusal that carries the classified kind, so callers can metric on
// why a value was rejected without re-parsing the message.
type Error struct {
	Kind Kind
	err  error
}

func (e *Error) Error() string { return e.err.Error() }

// Unwrap keeps errors.Is working for ErrInvalidToken and ErrRawIdentity.
func (e *Error) Unwrap() []error { return []error{e.err} }

// KindOf reports the classified kind carried by a rejection.
func KindOf(err error) (Kind, bool) {
	var refusal *Error
	if errors.As(err, &refusal) && refusal.Kind != "" {
		return refusal.Kind, true
	}
	return "", false
}

// Policy is the token policy. The zero value is usable and equals Default.
type Policy struct{}

// Default returns the shared policy.
func Default() Policy { return Policy{} }

// Normalize lowercases and trims a token so callers store one canonical form.
func Normalize(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

// Validate returns nil for an acceptable pseudonymous token, ErrRawIdentity (with
// ErrInvalidToken) when the value looks like a raw identifier, and
// ErrInvalidToken otherwise.
func (Policy) Validate(value string) error {
	// Digests are case-insensitive in practice, so the policy validates the
	// canonical form instead of refusing a correctly-shaped uppercase digest.
	value = Normalize(value)
	if tokenPattern.MatchString(value) {
		return nil
	}
	kind := Classify(value)
	switch kind {
	case KindEmail, KindPhone, KindUUID, KindDeviceID:
		return &Error{Kind: kind, err: fmt.Errorf("%w: %w: subject_token must be a pseudonymous digest, not a raw identifier", ErrInvalidToken, ErrRawIdentity)}
	case KindEmpty:
		return &Error{Kind: kind, err: fmt.Errorf("%w: %w: subject_token is required", ErrInvalidToken, ErrRawIdentity)}
	}
	return &Error{Kind: kind, err: fmt.Errorf("%w: subject_token must be a 64-character lowercase hex digest of a pseudonymous identifier", ErrInvalidToken)}
}

// Classify describes a value: the empty kind for an acceptable token, otherwise
// why it is refused.
func Classify(value string) Kind {
	value = Normalize(value)
	switch {
	case tokenPattern.MatchString(value):
		return ""
	case value == "":
		return KindEmpty
	case strings.Contains(value, "@"):
		return KindEmail
	case uuidPattern.MatchString(value):
		return KindUUID
	case looksLikePhone(value):
		return KindPhone
	case looksLikeDeviceID(value):
		return KindDeviceID
	case isHex(value):
		return KindWrongLength
	default:
		return KindNotHex
	}
}

func isHex(value string) bool {
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return len(value) > 0
}

// looksLikePhone matches a national or international phone number with optional
// separators, for example +8613800138000 or 138-0013-8000.
func looksLikePhone(value string) bool {
	digits := 0
	for i, r := range value {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '+':
			if i != 0 {
				return false
			}
		case r == '-', r == ' ', r == '(', r == ')':
		default:
			return false
		}
	}
	return digits >= 7 && digits <= 15
}

// looksLikeDeviceID matches cookie/vendor identifier shapes: long mixed
// alphanumeric strings with separator characters.
func looksLikeDeviceID(value string) bool {
	if len(value) < 16 {
		return false
	}
	hasDigit, hasAlpha := false, false
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasAlpha = true
		case r == '-', r == '_', r == ':':
		default:
			return false
		}
	}
	return hasDigit && hasAlpha && !isHex(value)
}
