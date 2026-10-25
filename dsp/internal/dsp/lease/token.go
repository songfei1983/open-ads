package lease

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var ErrInvalidReservationToken = errors.New("invalid reservation token")

// ReservationClaims binds an encrypted notice reference to one auction bid.
type ReservationClaims struct {
	KeyVersion     string            `json:"key_version"`
	TenantID       string            `json:"tenant_id"`
	ReservationID  string            `json:"reservation_id"`
	AuctionID      string            `json:"auction_id"`
	BidID          string            `json:"bid_id"`
	ImpressionID   string            `json:"imp_id"`
	Currency       string            `json:"currency"`
	ReservedMicros int64             `json:"reserved_micros"`
	ExpiresAt      time.Time         `json:"expires_at"`
	Demands        []Demand          `json:"demands"`
	LeaseIDs       map[string]string `json:"lease_ids"`
}

// TokenCodec encrypts reservation references with AES-256-GCM.
type TokenCodec struct {
	keyVersion string
	aead       cipher.AEAD
}

func NewTokenCodec(keyVersion string, key []byte) (*TokenCodec, error) {
	if strings.TrimSpace(keyVersion) == "" || len(key) != 32 {
		return nil, errors.New("reservation token key version and 32-byte AES key are required")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize reservation token cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize reservation token AEAD: %w", err)
	}
	return &TokenCodec{keyVersion: keyVersion, aead: aead}, nil
}

func (c *TokenCodec) Seal(claims ReservationClaims) (string, error) {
	if c == nil || claims.TenantID == "" || claims.ReservationID == "" || claims.AuctionID == "" || claims.BidID == "" || claims.ImpressionID == "" || claims.Currency == "" || claims.ReservedMicros <= 0 || claims.ExpiresAt.IsZero() {
		return "", ErrInvalidReservationToken
	}
	claims.KeyVersion = c.keyVersion
	plaintext, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode reservation claims: %w", err)
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("create reservation token nonce: %w", err)
	}
	ciphertext := c.aead.Seal(nonce, nonce, plaintext, []byte(c.keyVersion))
	return c.keyVersion + "." + base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

func (c *TokenCodec) Open(token string, now time.Time) (ReservationClaims, error) {
	if c == nil {
		return ReservationClaims{}, ErrInvalidReservationToken
	}
	version, encoded, ok := strings.Cut(token, ".")
	if !ok || version != c.keyVersion {
		return ReservationClaims{}, ErrInvalidReservationToken
	}
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(sealed) < c.aead.NonceSize()+c.aead.Overhead() {
		return ReservationClaims{}, ErrInvalidReservationToken
	}
	nonce := sealed[:c.aead.NonceSize()]
	plaintext, err := c.aead.Open(nil, nonce, sealed[c.aead.NonceSize():], []byte(c.keyVersion))
	if err != nil {
		return ReservationClaims{}, ErrInvalidReservationToken
	}
	var claims ReservationClaims
	if err := json.Unmarshal(plaintext, &claims); err != nil || claims.KeyVersion != c.keyVersion || !now.Before(claims.ExpiresAt) {
		return ReservationClaims{}, ErrInvalidReservationToken
	}
	return claims, nil
}
