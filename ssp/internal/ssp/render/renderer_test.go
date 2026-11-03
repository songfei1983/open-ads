package render_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/render"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/token"
)

const (
	testTenant  = "018f7a3b2c9d7e1fa3b4c5d6e7f89001"
	testAuction = "018f7a3b2c9d7e1fa3b4c5d6e7f89002"
)

func setup(t *testing.T) (*render.Renderer, *token.Codec, string) {
	t.Helper()
	root := t.TempDir()
	payload := []byte("fake-png-bytes")
	digest := sha256Hex(payload)
	dir := filepath.Join(root, testTenant, "assets", digest[0:2], digest[2:4])
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, digest), payload, 0o640); err != nil {
		t.Fatal(err)
	}
	codec, err := token.NewCodec([]byte(strings.Repeat("k", 32)), 1)
	if err != nil {
		t.Fatal(err)
	}
	store, err := render.NewReadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return &render.Renderer{Codec: codec, Store: store, TenantID: testTenant}, codec, digest
}

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func renderClaims(digest string) token.RenderClaims {
	return token.RenderClaims{
		TenantID: testTenant, PlacementID: "p", AuctionID: testAuction, BidID: "b", ImpID: "1",
		CreativeID: "c", AssetRef: "as:" + testTenant + ":" + digest, AssetSHA256: digest,
		MIMEType: "image/png", Width: 728, Height: 90, LandingURL: "https://publisher.test/landing",
		FrameAncestor: "https://publisher.test", ClickToken: "click-token", EventToken: "event-token",
		ExpiresAt: time.Now().Add(time.Minute),
	}
}

func TestRenderShellAndAsset(t *testing.T) {
	renderer, codec, digest := setup(t)
	claims := renderClaims(digest)
	raw, err := codec.Seal(token.AudienceRender, claims)
	if err != nil {
		t.Fatal(err)
	}
	shell := httptest.NewRecorder()
	renderer.Render(shell, httptest.NewRequest(http.MethodGet, "/render/v1/x", nil), raw)
	if shell.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", shell.Code)
	}
	body := shell.Body.String()
	csp := shell.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'nonce-") || !strings.Contains(csp, "frame-ancestors https://publisher.test") {
		t.Fatalf("missing CSP in shell: %s", csp)
	}
	if !strings.Contains(body, "<script nonce=") {
		t.Fatal("shell script must carry the CSP nonce")
	}
	if strings.Contains(body, "<img src=\"/render/v1/assets/"+digest) {
		t.Fatal("shell must reference the opaque token, not the asset ref")
	}
	if !strings.Contains(body, "/render/v1/assets/"+raw) {
		t.Fatal("shell must reference the signed asset endpoint")
	}
	asset := httptest.NewRecorder()
	renderer.Asset(asset, httptest.NewRequest(http.MethodGet, "/render/v1/assets/x", nil), raw)
	if asset.Code != http.StatusOK || asset.Body.String() != "fake-png-bytes" {
		t.Fatalf("unexpected asset response: %d %q", asset.Code, asset.Body.String())
	}
	if asset.Header().Get("Content-Type") != "image/png" || asset.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unsafe asset headers: %v", asset.Header())
	}
}

func TestRenderRejectsTamperedAsset(t *testing.T) {
	renderer, codec, digest := setup(t)
	raw, _ := codec.Seal(token.AudienceRender, renderClaims(digest))
	path := filepath.Join(renderer.Store.RootForTest(), testTenant, "assets", digest[0:2], digest[2:4], digest)
	if err := os.WriteFile(path, []byte("tampered"), 0o640); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	renderer.Asset(response, httptest.NewRequest(http.MethodGet, "/render/v1/assets/x", nil), raw)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "asset_tampered_detected") {
		t.Fatalf("expected tamper failure, got %d %s", response.Code, response.Body.String())
	}
}

func TestRenderRejectsWrongAudienceAndExpiry(t *testing.T) {
	renderer, codec, digest := setup(t)
	click, _ := codec.Seal(token.AudienceClick, token.ClickClaims{TenantID: testTenant, ExpiresAt: time.Now().Add(time.Minute)})
	response := httptest.NewRecorder()
	renderer.Render(response, httptest.NewRequest(http.MethodGet, "/render/v1/x", nil), click)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected audience mismatch rejection, got %d", response.Code)
	}
	claims := renderClaims(digest)
	claims.ExpiresAt = time.Now().Add(-time.Second)
	expired, _ := codec.Seal(token.AudienceRender, claims)
	response = httptest.NewRecorder()
	renderer.Render(response, httptest.NewRequest(http.MethodGet, "/render/v1/x", nil), expired)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected expired token rejection, got %d", response.Code)
	}
}

func TestClickRedirectsWithoutClientControlledURL(t *testing.T) {
	renderer, codec, _ := setup(t)
	raw, _ := codec.Seal(token.AudienceClick, token.ClickClaims{TenantID: testTenant,
		LandingURL: "https://publisher.test/landing", ExpiresAt: time.Now().Add(time.Minute)})
	recorder := httptest.NewRecorder()
	renderer.Click(recorder, httptest.NewRequest(http.MethodGet, "/click/v1/x?redirect=https://evil.test", nil), raw)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("expected redirect, got %d", response.StatusCode)
	}
	location := response.Header.Get("Location")
	if location != "https://publisher.test/landing" {
		t.Fatalf("click must ignore client redirect parameters: %s", location)
	}
	_, _ = io.Copy(io.Discard, response.Body)
}
