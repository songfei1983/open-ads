// Package token seals the short-lived opaque bearer tokens the SSP hands to
// browsers and DSPs. Tokens are encrypted and authenticated, never merely
// signed, so the claims stay confidential.
package token

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	formatVersion = 1

	// AudienceRender authorizes reading one creative's image bytes.
	AudienceRender = "ssp.render.v1"
	// AudienceClick authorizes one click redirect.
	AudienceClick = "ssp.click.v1"
	// AudienceEvent authorizes one impression event.
	AudienceEvent = "ssp.event.v1"
)

// Codec seals and opens versioned AES-256-GCM tokens.
type Codec struct {
	key        []byte
	keyVersion byte
}

// NewCodec validates the key material and version.
func NewCodec(key []byte, keyVersion uint32) (*Codec, error) {
	if len(key) != 32 {
		return nil, errors.New("token key must be 32 bytes")
	}
	if keyVersion == 0 || keyVersion > 255 {
		return nil, errors.New("token key version must be between 1 and 255")
	}
	return &Codec{key: append([]byte(nil), key...), keyVersion: byte(keyVersion)}, nil
}

// Seal encrypts claims for the given audience.
func (c *Codec) Seal(audience string, claims any) (string, error) {
	if c == nil {
		return "", errors.New("token codec is disabled")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode token claims: %w", err)
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate token nonce: %w", err)
	}
	sealed := aead.Seal(nil, nonce, payload, aad(audience, c.keyVersion))
	wire := make([]byte, 0, 2+len(nonce)+len(sealed))
	wire = append(wire, formatVersion, c.keyVersion)
	wire = append(wire, nonce...)
	wire = append(wire, sealed...)
	return base64.RawURLEncoding.EncodeToString(wire), nil
}

// Open authenticates and decrypts a token for the expected audience.
func (c *Codec) Open(audience, value string, target any) error {
	if c == nil {
		return errors.New("token codec is disabled")
	}
	wire, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return errors.New("token is not valid base64url")
	}
	if len(wire) < 2 {
		return errors.New("token is truncated")
	}
	if wire[0] != formatVersion {
		return fmt.Errorf("unsupported token format %d", wire[0])
	}
	if wire[1] != c.keyVersion {
		return fmt.Errorf("unknown token key version %d", wire[1])
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if len(wire) < 2+aead.NonceSize() {
		return errors.New("token is truncated")
	}
	nonce := wire[2 : 2+aead.NonceSize()]
	ciphertext := wire[2+aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad(audience, c.keyVersion))
	if err != nil {
		return errors.New("token authentication failed")
	}
	if err := json.Unmarshal(plaintext, target); err != nil {
		return fmt.Errorf("decode token claims: %w", err)
	}
	return nil
}

func aad(audience string, keyVersion byte) []byte {
	return []byte(fmt.Sprintf("openads.token.v1|%s|%d", audience, keyVersion))
}

// RenderClaims bind a render token to one immutable creative and auction.
type RenderClaims struct {
	TenantID      string    `json:"tenant_id"`
	PlacementID   string    `json:"placement_id"`
	AuctionID     string    `json:"auction_id"`
	BidID         string    `json:"bid_id"`
	ImpID         string    `json:"imp_id"`
	CreativeID    string    `json:"creative_id"`
	DSPEndpointID string    `json:"dsp_endpoint_id"`
	AssetRef      string    `json:"asset_ref"`
	AssetSHA256   string    `json:"asset_sha256"`
	MIMEType      string    `json:"mime_type"`
	Width         int       `json:"width"`
	Height        int       `json:"height"`
	LandingURL    string    `json:"landing_url"`
	FrameAncestor string    `json:"frame_ancestor"`
	ClickToken    string    `json:"click_token"`
	EventToken    string    `json:"event_token"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// Expired reports whether the claims are outside their validity window.
func (c RenderClaims) Expired(now time.Time) bool { return !now.Before(c.ExpiresAt) }

// ClickClaims bind a click redirect to the auction's allowlisted landing URL.
type ClickClaims struct {
	TenantID   string    `json:"tenant_id"`
	AuctionID  string    `json:"auction_id"`
	BidID      string    `json:"bid_id"`
	LandingURL string    `json:"landing_url"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Expired reports whether the claims are outside their validity window.
func (c ClickClaims) Expired(now time.Time) bool { return !now.Before(c.ExpiresAt) }

// EventClaims carry the financial context recovered at impression time.
type EventClaims struct {
	TenantID      string    `json:"tenant_id"`
	PlacementID   string    `json:"placement_id"`
	AuctionID     string    `json:"auction_id"`
	BidID         string    `json:"bid_id"`
	ImpID         string    `json:"imp_id"`
	CreativeID    string    `json:"creative_id"`
	DSPEndpointID string    `json:"dsp_endpoint_id"`
	Reservation   string    `json:"reservation_ref,omitempty"`
	BidCPMMicros  int64     `json:"bid_cpm_micros"`
	Currency      string    `json:"currency"`
	BURLTarget    string    `json:"burl_target,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// Expired reports whether the claims are outside their validity window.
func (c EventClaims) Expired(now time.Time) bool { return !now.Before(c.ExpiresAt) }

// EventKey returns the stable idempotency key for the impression.
func (c EventClaims) EventKey() string {
	return c.TenantID + "|" + c.AuctionID + "|" + c.BidID + "|" + c.ImpID
}
