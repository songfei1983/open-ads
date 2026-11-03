package dmp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/dmp"
)

func newAdapter(baseURL string, key []byte) *dmp.Adapter {
	return &dmp.Adapter{BaseURL: baseURL, TenantID: "tenant", Namespace: "publisher-account-v1",
		HMACKey: key, Timeout: time.Second, Client: &http.Client{Timeout: time.Second}}
}

func TestSubjectTokenIsNamespacedHMAC(t *testing.T) {
	adapter := newAdapter("http://127.0.0.1", []byte(strings.Repeat("k", 32)))
	first, err := adapter.SubjectToken("publisher-pseudonym")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || strings.Contains(first, "pseudonym") {
		t.Fatalf("unexpected token: %s", first)
	}
	second, _ := adapter.SubjectToken("publisher-pseudonym")
	if first != second {
		t.Fatal("expected stable HMAC")
	}
	other := newAdapter("http://127.0.0.1", []byte(strings.Repeat("k", 32)))
	other.Namespace = "other-namespace"
	third, _ := other.SubjectToken("publisher-pseudonym")
	if third == first {
		t.Fatal("namespace must be part of the token derivation")
	}
}

func TestResolveSendsOnlyHMACToken(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		_, _ = w.Write([]byte(`{"request_id":"018f7a3b2c9d7e1fa3b4c5d6e7f89070","segments":[{"id":"seg-1","name":"Golf"}]}`))
	}))
	defer server.Close()
	adapter := newAdapter(server.URL, []byte(strings.Repeat("k", 32)))
	segments, err := adapter.Resolve(t.Context(), "018f7a3b2c9d7e1fa3b4c5d6e7f89070", "raw-publisher-pseudonym", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].ID != "seg-1" {
		t.Fatalf("unexpected segments: %+v", segments)
	}
	tokenValue, _ := received["subject_token"].(string)
	if len(tokenValue) != 64 || strings.Contains(tokenValue, "raw-publisher-pseudonym") {
		t.Fatalf("raw pseudonym leaked to the DMP: %v", received["subject_token"])
	}
	if received["namespace"] != "publisher-account-v1" {
		t.Fatalf("unexpected namespace: %v", received["namespace"])
	}
}

func TestResolveFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	adapter := newAdapter(server.URL, []byte(strings.Repeat("k", 32)))
	if _, err := adapter.Resolve(t.Context(), "req", "pseudonym", 10); err == nil {
		t.Fatal("expected fail-closed error on DMP 503")
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"segments":[]}`))
	}))
	defer slow.Close()
	adapter = newAdapter(slow.URL, []byte(strings.Repeat("k", 32)))
	adapter.Timeout = 5 * time.Millisecond
	if _, err := adapter.Resolve(t.Context(), "req", "pseudonym", 10); err == nil {
		t.Fatal("expected fail-closed error on DMP timeout")
	}
}

func TestResolveSkipsWithoutIdentityOrConfiguration(t *testing.T) {
	adapter := newAdapter("http://127.0.0.1:1", []byte(strings.Repeat("k", 32)))
	segments, err := adapter.Resolve(t.Context(), "req", "", 10)
	if err != nil || len(segments) != 0 {
		t.Fatalf("expected no lookup without identity: %v %v", segments, err)
	}
	disabled := &dmp.Adapter{}
	if segments, err := disabled.Resolve(t.Context(), "req", "pseudonym", 10); err != nil || len(segments) != 0 {
		t.Fatalf("expected no lookup when disabled")
	}
}
