package http

import (
	_ "embed"
	"net/http"
)

//go:embed sdk/openads-sdk.js
var sdkSource []byte

func (s *Server) handleSDK(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(sdkSource)
}
