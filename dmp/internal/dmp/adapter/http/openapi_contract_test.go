package http_test

import (
	"bufio"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	adapterHTTP "github.com/songfei1983/open-ads/dmp/internal/dmp/adapter/http"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/config"
)

var (
	pathLine   = regexp.MustCompile(`^  (/\S*):\s*$`)
	methodLine = regexp.MustCompile(`^    (get|post|put|delete):\s*$`)
)

// documentedOperations reads dmp/docs/openapi.yaml without a YAML dependency: the
// document's stable layout is one path per two-space key and one verb per
// four-space key.
func documentedOperations(t *testing.T) map[string]map[string]bool {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "..", "..", "docs", "openapi.yaml"))
	if err != nil {
		t.Fatalf("open the OpenAPI contract: %v", err)
	}
	defer file.Close()

	operations := map[string]map[string]bool{}
	current := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if match := pathLine.FindStringSubmatch(line); match != nil {
			current = match[1]
			operations[current] = map[string]bool{}
			continue
		}
		if match := methodLine.FindStringSubmatch(line); match != nil && current != "" {
			operations[current][strings.ToUpper(match[1])] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read the OpenAPI contract: %v", err)
	}
	if len(operations) == 0 {
		t.Fatal("the OpenAPI contract documents no paths")
	}
	return operations
}

// TestOpenAPIContractMatchesTheRouter fails when the documented surface drifts
// from what the service actually routes.
func TestOpenAPIContractMatchesTheRouter(t *testing.T) {
	cfg := &config.Config{
		ServiceName:   "test-dmp",
		Port:          8082,
		InternalToken: "dmp-internal-token-0123456789abcdef",
		AdminToken:    "dmp-admin-token-0123456789abcdef",
		AdminTenantID: "00000000-0000-0000-0000-000000000001",
		AdminActor:    "sandbox-operator",
	}
	srv := adapterHTTP.NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.SetReady(true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for path, methods := range documentedOperations(t) {
		for method := range methods {
			concrete := strings.NewReplacer(
				"{snapshot_id}", "01a17d00-0000-7000-8000-000000000001",
				"{revocation_id}", "01a17d00-0000-7000-8000-000000000001",
				"{id}", "documented-id",
			).Replace(path)
			request, err := http.NewRequest(method, ts.URL+concrete, strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+cfg.InternalToken)
			response, err := ts.Client().Do(request)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			body := make([]byte, 4096)
			read, _ := response.Body.Read(body)
			response.Body.Close()
			payload := string(body[:read])

			if response.StatusCode == http.StatusMethodNotAllowed {
				t.Fatalf("%s %s is documented but the router rejects the method: %s", method, path, payload)
			}
			if strings.Contains(payload, "unknown internal API path") || strings.Contains(payload, "unknown management API path") {
				t.Fatalf("%s %s is documented but not routed: %s", method, path, payload)
			}
		}
	}
}
