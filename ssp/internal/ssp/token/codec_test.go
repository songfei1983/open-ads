package token_test

import (
	"strings"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/token"
)

func testCodec(t *testing.T) *token.Codec {
	t.Helper()
	codec, err := token.NewCodec([]byte(strings.Repeat("k", 32)), 3)
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func TestSealOpenRoundTrip(t *testing.T) {
	codec := testCodec(t)
	claims := token.EventClaims{TenantID: "t", AuctionID: "a", BidID: "b", ImpID: "1",
		Currency: "USD", ExpiresAt: time.Now().Add(time.Minute)}
	sealed, err := codec.Seal(token.AudienceEvent, claims)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "USD") {
		t.Fatal("token must not expose claims in plaintext")
	}
	var opened token.EventClaims
	if err := codec.Open(token.AudienceEvent, sealed, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.AuctionID != "a" || opened.Currency != "USD" {
		t.Fatalf("unexpected claims: %+v", opened)
	}
}

func TestOpenRejectsWrongAudienceAndTampering(t *testing.T) {
	codec := testCodec(t)
	sealed, err := codec.Seal(token.AudienceRender, token.RenderClaims{TenantID: "t", CreativeID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	var claims token.RenderClaims
	if err := codec.Open(token.AudienceClick, sealed, &claims); err == nil {
		t.Fatal("expected audience mismatch to fail")
	}
	tampered := []byte(sealed)
	if tampered[len(tampered)-1] == 'A' {
		tampered[len(tampered)-1] = 'B'
	} else {
		tampered[len(tampered)-1] = 'A'
	}
	if err := codec.Open(token.AudienceRender, string(tampered), &claims); err == nil {
		t.Fatal("expected tampered token to fail")
	}
}

func TestOpenRejectsUnknownKeyVersion(t *testing.T) {
	codec := testCodec(t)
	sealed, err := codec.Seal(token.AudienceClick, token.ClickClaims{TenantID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := token.NewCodec([]byte(strings.Repeat("k", 32)), 4)
	if err != nil {
		t.Fatal(err)
	}
	var claims token.ClickClaims
	if err := other.Open(token.AudienceClick, sealed, &claims); err == nil {
		t.Fatal("expected key version mismatch")
	}
}

func TestClaimsExpiry(t *testing.T) {
	claims := token.RenderClaims{ExpiresAt: time.Now().Add(-time.Second)}
	if !claims.Expired(time.Now().UTC()) {
		t.Fatal("expected expired claims")
	}
}
