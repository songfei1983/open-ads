package render

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/observability"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/token"
)

// Renderer serves the SSP-hosted creative shell, its image bytes and clicks.
// It never executes DSP supplied HTML or JavaScript.
type Renderer struct {
	Codec    *token.Codec
	Store    *ReadStore
	TenantID string
	Metrics  *observability.Metrics
}

// Render serves the HTML shell for a signed render token.
func (r *Renderer) Render(w http.ResponseWriter, req *http.Request, rawToken string) {
	claims, ok := r.openRender(w, rawToken)
	if !ok {
		return
	}
	nonce, err := newNonce()
	if err != nil {
		writeRenderError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	frameAncestors := strings.TrimSpace(claims.FrameAncestor)
	if frameAncestors == "" {
		frameAncestors = "*"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; script-src 'nonce-"+nonce+
		"'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors "+frameAncestors)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(renderShell(claims, rawToken, nonce)))
}

// Asset streams the immutable image bytes bound to the render token.
func (r *Renderer) Asset(w http.ResponseWriter, req *http.Request, rawToken string) {
	claims, ok := r.openRender(w, rawToken)
	if !ok {
		return
	}
	if r.Store == nil {
		writeRenderError(w, http.StatusServiceUnavailable, "asset_unavailable")
		return
	}
	payload, err := r.Store.Read(claims.AssetRef, claims.AssetSHA256)
	if err != nil {
		if err == ErrAssetTampered {
			r.observe("tampered")
			writeRenderError(w, http.StatusServiceUnavailable, "asset_tampered_detected")
			return
		}
		r.observe("failed")
		writeRenderError(w, http.StatusServiceUnavailable, "asset_unavailable")
		return
	}
	mime, ok := normalizeMIME(claims.MIMEType)
	if !ok {
		r.observe("failed")
		writeRenderError(w, http.StatusServiceUnavailable, "asset_unavailable")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; base-uri 'none'; form-action 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
	r.observe("served")
}

func (r *Renderer) observe(outcome string) {
	if r.Metrics != nil {
		r.Metrics.ObserveRender(outcome)
	}
}

// Click validates the click token and redirects to the allowlisted landing URL.
func (r *Renderer) Click(w http.ResponseWriter, req *http.Request, rawToken string) {
	var claims token.ClickClaims
	if r.Codec == nil || r.Codec.Open(token.AudienceClick, rawToken, &claims) != nil {
		writeRenderError(w, http.StatusNotFound, "not_found")
		return
	}
	if claims.TenantID != r.TenantID || claims.Expired(time.Now().UTC()) || claims.LandingURL == "" {
		writeRenderError(w, http.StatusNotFound, "not_found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex")
	http.Redirect(w, req, claims.LandingURL, http.StatusFound)
}

func (r *Renderer) openRender(w http.ResponseWriter, rawToken string) (token.RenderClaims, bool) {
	var claims token.RenderClaims
	if r.Codec == nil || r.Codec.Open(token.AudienceRender, rawToken, &claims) != nil {
		writeRenderError(w, http.StatusNotFound, "not_found")
		return claims, false
	}
	if claims.TenantID != r.TenantID || claims.Expired(time.Now().UTC()) {
		writeRenderError(w, http.StatusNotFound, "not_found")
		return claims, false
	}
	return claims, true
}

func renderShell(claims token.RenderClaims, rawToken, nonce string) string {
	eventID := sha256.Sum256([]byte(claims.TenantID + "|" + claims.AuctionID + "|" + claims.BidID + "|" + claims.ImpID))
	occurredAt := time.Now().UTC().Format(time.RFC3339)
	clickHref := "/click/v1/" + claims.ClickToken
	var b strings.Builder
	b.WriteString("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	b.WriteString("<meta name=\"referrer\" content=\"no-referrer\">")
	b.WriteString("<style>html,body{margin:0;padding:0;overflow:hidden;background:transparent}img{display:block;width:100%;height:100%;object-fit:contain;border:0}</style>")
	b.WriteString("</head><body>")
	if claims.ClickToken != "" && claims.LandingURL != "" {
		b.WriteString("<a href=\"" + html.EscapeString(clickHref) + "\" target=\"_blank\" rel=\"noopener noreferrer\">")
	}
	b.WriteString("<img src=\"/render/v1/assets/" + html.EscapeString(rawToken) + "\"")
	b.WriteString(" width=\"" + strconv.Itoa(claims.Width) + "\" height=\"" + strconv.Itoa(claims.Height) + "\" alt=\"\">")
	if claims.ClickToken != "" && claims.LandingURL != "" {
		b.WriteString("</a>")
	}
	b.WriteString("<script nonce=\"" + nonce + "\">")
	b.WriteString("(function(){var img=document.querySelector('img');if(!img)return;")
	b.WriteString("img.addEventListener('load',function(){var body=JSON.stringify({event_token:" + jsonString(claims.EventToken) +
		",event_id:" + jsonString(hex.EncodeToString(eventID[:16])) + ",occurred_at:" + jsonString(occurredAt) + "});")
	b.WriteString("try{fetch('/sdk/v1/impressions',{method:'POST',headers:{'Content-Type':'text/plain'},body:body,credentials:'omit',keepalive:true});}catch(e){}},{once:true});})();")
	b.WriteString("</script></body></html>")
	return b.String()
}

func newNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func normalizeMIME(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image/png":
		return "image/png", true
	case "image/jpeg":
		return "image/jpeg", true
	case "image/webp":
		return "image/webp", true
	default:
		return "", false
	}
}

func jsonString(value string) string {
	escaped := strings.ReplaceAll(value, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	return "\"" + escaped + "\""
}

func writeRenderError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":"` + code + `"}`))
}
