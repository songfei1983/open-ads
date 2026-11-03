package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
)

const testToken = "supersecrettoken1234567890abcdef"

type fakeStore struct {
	ManagementStore
	published        string
	publishErr       error
	publishVersion   uint64
	publishType      string
	registryEntries  []CreativeRegistration
	registryEndpoint string
	outbox           []OutboxEvent
	retried          string
}

func (f *fakeStore) ListPublishers(context.Context) ([]Publisher, error) {
	return []Publisher{{ID: strings.Repeat("a", 32), Name: "Acme", Status: "active", Version: 1}}, nil
}

func (f *fakeStore) Publish(_ context.Context, aggregateType, id string, version uint64) (string, error) {
	f.publishType, f.publishVersion = aggregateType, version
	return f.published, f.publishErr
}

func (f *fakeStore) ListOutbox(context.Context, string, int) ([]OutboxEvent, error) {
	return f.outbox, nil
}

func (f *fakeStore) RetryOutbox(_ context.Context, id string) error {
	f.retried = id
	return nil
}

func (f *fakeStore) ListCreativeWhitelist(_ context.Context, id string) (CreativeWhitelistResponse, error) {
	return CreativeWhitelistResponse{DSPEndpointID: id, Whitelist: []CreativeWhitelistEntry{}}, nil
}

func (f *fakeStore) UpsertCreativeRegistry(_ context.Context, endpointID string, entries []CreativeRegistration) (int, error) {
	f.registryEndpoint, f.registryEntries = endpointID, entries
	return len(entries), nil
}

type fakeRuntime struct{ instances []RuntimeInstance }

func (f fakeRuntime) Instances(context.Context) ([]RuntimeInstance, error) { return f.instances, nil }

func adminRequest(t *testing.T, handler http.Handler, method, target, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", strings.Repeat("f", 32))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestAdminHandlerAuthenticatesAndLists(t *testing.T) {
	store := &fakeStore{}
	handler := NewAdminHandler(store, testToken, fakeRuntime{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if response := adminRequest(t, handler, http.MethodGet, "/admin/v1/publishers", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
	response := adminRequest(t, handler, http.MethodGet, "/admin/v1/publishers", "", testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var items []Publisher
	if err := json.NewDecoder(response.Body).Decode(&items); err != nil || len(items) != 1 {
		t.Fatalf("unexpected list: %v %v", items, err)
	}
}

func TestAdminHandlerPublishAcceptsActionSuffix(t *testing.T) {
	store := &fakeStore{published: snapshot.FormatVersion(7)}
	handler := NewAdminHandler(store, testToken, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	id := strings.Repeat("b", 32)
	response := adminRequest(t, handler, http.MethodPost, "/admin/v1/placements/"+id+":publish", `{"version":3,"reason":"demo"}`, testToken)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", response.Code, response.Body.String())
	}
	if store.publishType != AggregatePlacement || store.publishVersion != 3 {
		t.Fatalf("unexpected publish call: %s %d", store.publishType, store.publishVersion)
	}
	var body map[string]string
	_ = json.NewDecoder(response.Body).Decode(&body)
	if body["publish_status"] != "pending" || body["config_version"] != snapshot.FormatVersion(7) {
		t.Fatalf("unexpected publish response: %v", body)
	}
}

func TestAdminHandlerMapsVersionConflict(t *testing.T) {
	store := &fakeStore{publishErr: ErrVersionConflict}
	handler := NewAdminHandler(store, testToken, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := adminRequest(t, handler, http.MethodPost, "/admin/v1/publishers/"+strings.Repeat("c", 32)+"/publish", `{"version":1}`, testToken)
	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "version_conflict") {
		t.Fatalf("unexpected error body: %s", response.Body.String())
	}
}

func TestAdminHandlerRetryAndRuntime(t *testing.T) {
	store := &fakeStore{outbox: []OutboxEvent{{EventID: strings.Repeat("d", 32), AggregateType: "placement"}}}
	runtime := fakeRuntime{instances: []RuntimeInstance{{InstanceID: strings.Repeat("e", 32), Ready: true, ManifestVersion: snapshot.FormatVersion(2)}}}
	handler := NewAdminHandler(store, testToken, runtime, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if response := adminRequest(t, handler, http.MethodGet, "/admin/v1/config-outbox?status=failed", "", testToken); response.Code != http.StatusOK {
		t.Fatalf("expected outbox list 200, got %d", response.Code)
	}
	eventID := strings.Repeat("d", 32)
	if response := adminRequest(t, handler, http.MethodPost, "/admin/v1/config-outbox/"+eventID+":retry", "", testToken); response.Code != http.StatusAccepted {
		t.Fatalf("expected retry 202, got %d", response.Code)
	}
	if store.retried != eventID {
		t.Fatalf("retry did not reach the store: %s", store.retried)
	}
	response := adminRequest(t, handler, http.MethodGet, "/admin/v1/runtime/instances", "", testToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), snapshot.FormatVersion(2)) {
		t.Fatalf("unexpected runtime response: %d %s", response.Code, response.Body.String())
	}
}

func TestInternalRegistryUpsertValidatesEntries(t *testing.T) {
	store := &fakeStore{}
	handler := NewInternalHandler(store, testToken, slog.New(slog.NewTextHandler(io.Discard, nil)))
	valid := CreativeRegistration{
		CreativeID: strings.Repeat("1", 32), AssetRef: "as:" + strings.Repeat("2", 32) + ":" + strings.Repeat("3", 64),
		AssetSHA256: strings.Repeat("3", 64), Width: 728, Height: 90, MIMEType: "image/png",
		LandingURL: "https://publisher.test/landing", Status: "active", Version: 1,
	}
	invalid := valid
	invalid.CreativeID = strings.Repeat("4", 32)
	invalid.MIMEType = "text/html"
	body, _ := json.Marshal(CreativeRegistryUpsert{Creatives: []CreativeRegistration{valid, invalid}})
	response := adminRequest(t, handler, http.MethodPost,
		"/internal/v1/dsp-endpoints/"+strings.Repeat("5", 32)+"/creative-registry:upsert", string(body), testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var result CreativeRegistryResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.AcceptedCount != 1 || len(result.Rejected) != 1 {
		t.Fatalf("unexpected registry result: %+v", result)
	}
	if len(store.registryEntries) != 1 || store.registryEntries[0].CreativeID != valid.CreativeID {
		t.Fatalf("invalid entries reached the store: %+v", store.registryEntries)
	}
	if store.registryEndpoint != strings.Repeat("5", 32) {
		t.Fatalf("unexpected endpoint id: %s", store.registryEndpoint)
	}
}
