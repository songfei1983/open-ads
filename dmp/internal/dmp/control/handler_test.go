package control_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/control"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/observability"
)

// memoryStore mirrors the production version semantics: append-only versions,
// optimistic locking on the caller's version, immutable reference fields and an
// audit entry for every mutation attempt.
type memoryStore struct {
	mu         sync.Mutex
	namespaces map[string]control.Namespace
	providers  map[string]control.Provider
	segments   map[string]control.Segment
	audit      []control.AuditEntry
	now        time.Time
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		namespaces: map[string]control.Namespace{},
		providers:  map[string]control.Provider{},
		segments:   map[string]control.Segment{},
		now:        time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
	}
}

func (m *memoryStore) ListNamespaces(context.Context) ([]control.Namespace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]control.Namespace, 0, len(m.namespaces))
	for _, item := range m.namespaces {
		items = append(items, item)
	}
	return items, nil
}

func (m *memoryStore) GetNamespace(_ context.Context, id string) (control.Namespace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.namespaces[id]
	if !ok {
		return control.Namespace{}, control.ErrNotFound
	}
	return item, nil
}

func (m *memoryStore) CreateNamespace(_ context.Context, input control.NamespaceInput, meta control.Mutation) (control.Namespace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.namespaces[input.ID]; ok {
		return control.Namespace{}, m.auditFail(meta, control.ActionCreate, "namespace", input.ID, 0, control.OutcomeConflict, control.ErrAlreadyExists)
	}
	item := control.Namespace{
		ID: input.ID, Version: 1, DisplayName: input.DisplayName,
		HashingAlgorithm: input.HashingAlgorithm, SubjectKeyVersion: input.SubjectKeyVersion,
		RetentionMonths: input.RetentionMonths, Status: input.Status, WrittenAt: m.now,
	}
	m.namespaces[item.ID] = item
	m.auditAccept(meta, control.ActionCreate, "namespace", item.ID, item.Version)
	return item, nil
}

func (m *memoryStore) UpdateNamespace(_ context.Context, id string, input control.NamespaceInput, meta control.Mutation) (control.Namespace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.namespaces[id]
	if !ok {
		return control.Namespace{}, m.auditFail(meta, control.ActionUpdate, "namespace", id, 0, control.OutcomeNotFound, control.ErrNotFound)
	}
	if current.Version != input.Version {
		return control.Namespace{}, m.auditFail(meta, control.ActionUpdate, "namespace", id, input.Version, control.OutcomeConflict, control.ErrVersionConflict)
	}
	current.Version++
	current.DisplayName = input.DisplayName
	current.HashingAlgorithm = input.HashingAlgorithm
	current.SubjectKeyVersion = input.SubjectKeyVersion
	current.RetentionMonths = input.RetentionMonths
	current.Status = input.Status
	current.WrittenAt = m.now
	m.namespaces[id] = current
	m.auditAccept(meta, control.ActionUpdate, "namespace", current.ID, current.Version)
	return current, nil
}

func (m *memoryStore) ListProviders(context.Context) ([]control.Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]control.Provider, 0, len(m.providers))
	for _, item := range m.providers {
		items = append(items, item)
	}
	return items, nil
}

func (m *memoryStore) GetProvider(_ context.Context, id string) (control.Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.providers[id]
	if !ok {
		return control.Provider{}, control.ErrNotFound
	}
	return item, nil
}

func (m *memoryStore) CreateProvider(_ context.Context, input control.ProviderInput, meta control.Mutation) (control.Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.providers[input.ID]; ok {
		return control.Provider{}, m.auditFail(meta, control.ActionCreate, "provider", input.ID, 0, control.OutcomeConflict, control.ErrAlreadyExists)
	}
	namespace, ok := m.namespaces[input.Namespace]
	if !ok || namespace.Status != control.StatusActive {
		return control.Provider{}, m.auditFail(meta, control.ActionCreate, "provider", input.ID, 0, control.OutcomeRejected,
			fmt.Errorf("%w: namespace must exist and be active", control.ErrInvalidDefinition))
	}
	item := control.Provider{
		ID: input.ID, Version: 1, DisplayName: input.DisplayName, Namespace: input.Namespace,
		LicenseVersion: input.LicenseVersion, Status: input.Status, WrittenAt: m.now,
	}
	m.providers[item.ID] = item
	m.auditAccept(meta, control.ActionCreate, "provider", item.ID, item.Version)
	return item, nil
}

func (m *memoryStore) UpdateProvider(_ context.Context, id string, input control.ProviderInput, meta control.Mutation) (control.Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.providers[id]
	if !ok {
		return control.Provider{}, m.auditFail(meta, control.ActionUpdate, "provider", id, 0, control.OutcomeNotFound, control.ErrNotFound)
	}
	if current.Version != input.Version {
		return control.Provider{}, m.auditFail(meta, control.ActionUpdate, "provider", id, input.Version, control.OutcomeConflict, control.ErrVersionConflict)
	}
	if current.Namespace != input.Namespace {
		return control.Provider{}, m.auditFail(meta, control.ActionUpdate, "provider", id, input.Version, control.OutcomeRejected,
			fmt.Errorf("%w: namespace cannot change after creation", control.ErrImmutableField))
	}
	current.Version++
	current.DisplayName = input.DisplayName
	current.LicenseVersion = input.LicenseVersion
	current.Status = input.Status
	current.WrittenAt = m.now
	m.providers[id] = current
	m.auditAccept(meta, control.ActionUpdate, "provider", current.ID, current.Version)
	return current, nil
}

func (m *memoryStore) ListSegments(context.Context) ([]control.Segment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]control.Segment, 0, len(m.segments))
	for _, item := range m.segments {
		items = append(items, item)
	}
	return items, nil
}

func (m *memoryStore) GetSegment(_ context.Context, id string) (control.Segment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.segments[id]
	if !ok {
		return control.Segment{}, control.ErrNotFound
	}
	return item, nil
}

func (m *memoryStore) CreateSegment(_ context.Context, input control.SegmentInput, meta control.Mutation) (control.Segment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.segments[input.ID]; ok {
		return control.Segment{}, m.auditFail(meta, control.ActionCreate, "segment", input.ID, 0, control.OutcomeConflict, control.ErrAlreadyExists)
	}
	provider, ok := m.providers[input.ProviderID]
	if !ok || provider.Status != control.StatusActive {
		return control.Segment{}, m.auditFail(meta, control.ActionCreate, "segment", input.ID, 0, control.OutcomeRejected,
			fmt.Errorf("%w: provider must exist and be active", control.ErrInvalidDefinition))
	}
	item := control.Segment{
		ID: input.ID, Version: 1, ProviderID: input.ProviderID, Name: input.Name,
		Description: input.Description, Status: input.Status, ValidFrom: *input.ValidFrom, WrittenAt: m.now,
	}
	m.segments[item.ID] = item
	m.auditAccept(meta, control.ActionCreate, "segment", item.ID, item.Version)
	return item, nil
}

func (m *memoryStore) UpdateSegment(_ context.Context, id string, input control.SegmentInput, meta control.Mutation) (control.Segment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.segments[id]
	if !ok {
		return control.Segment{}, m.auditFail(meta, control.ActionUpdate, "segment", id, 0, control.OutcomeNotFound, control.ErrNotFound)
	}
	if current.Version != input.Version {
		return control.Segment{}, m.auditFail(meta, control.ActionUpdate, "segment", id, input.Version, control.OutcomeConflict, control.ErrVersionConflict)
	}
	if current.ProviderID != input.ProviderID {
		return control.Segment{}, m.auditFail(meta, control.ActionUpdate, "segment", id, input.Version, control.OutcomeRejected,
			fmt.Errorf("%w: provider_id cannot change after creation", control.ErrImmutableField))
	}
	current.Version++
	current.Name = input.Name
	current.Description = input.Description
	current.Status = input.Status
	current.ValidFrom = *input.ValidFrom
	current.WrittenAt = m.now
	m.segments[id] = current
	m.auditAccept(meta, control.ActionUpdate, "segment", current.ID, current.Version)
	return current, nil
}

func (m *memoryStore) ListAudit(_ context.Context, query control.AuditQuery) ([]control.AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := make([]control.AuditEntry, 0, len(m.audit))
	for i := len(m.audit) - 1; i >= 0; i-- {
		entry := m.audit[i]
		if query.ResourceType != "" && entry.ResourceType != query.ResourceType {
			continue
		}
		if query.ResourceID != "" && entry.ResourceID != query.ResourceID {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (m *memoryStore) RecordRejected(_ context.Context, meta control.Mutation, action, resourceType, resourceID, detail string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordOutcome(meta, action, resourceType, resourceID, 0, control.OutcomeRejected, detail)
	return nil
}

func (m *memoryStore) auditAccept(meta control.Mutation, action, resourceType, resourceID string, version uint64) {
	m.recordOutcome(meta, action, resourceType, resourceID, version, control.OutcomeAccepted, "")
}

// recordOutcome stores the terminal row of an attempt, paired with the intent
// that the real store writes before touching storage.
func (m *memoryStore) recordOutcome(meta control.Mutation, action, resourceType, resourceID string, version uint64, outcome, detail string) {
	id := len(m.audit) + 1
	m.audit = append(m.audit, control.AuditEntry{
		AuditID: fmt.Sprintf("audit-%d", id), IntentID: fmt.Sprintf("intent-%d", id), Phase: control.PhaseOutcome,
		Actor: meta.Actor, Action: action, ResourceType: resourceType, ResourceID: resourceID,
		ResourceVersion: version, RequestID: meta.RequestID, Outcome: outcome, Detail: detail, OccurredAt: m.now,
	})
}

// seedIntent stores an attempt whose outcome was never recorded.
func (m *memoryStore) seedIntent(actor, action, resourceType, resourceID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := fmt.Sprintf("intent-unfinished-%d", len(m.audit)+1)
	m.audit = append(m.audit, control.AuditEntry{
		AuditID: id, IntentID: id, Phase: control.PhaseIntent, Actor: actor, Action: action,
		ResourceType: resourceType, ResourceID: resourceID, RequestID: "req-lost",
		OccurredAt: m.now.Add(-time.Hour),
	})
	return id
}

// resolveIntent appends the outcome an operator recorded while reconciling.
func (m *memoryStore) resolveIntent(intentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, control.AuditEntry{
		AuditID: "audit-reconciled", IntentID: intentID, Phase: control.PhaseOutcome,
		Actor: "sandbox-operator", Action: control.ActionCreate, ResourceType: "provider",
		ResourceID: "prov-orphan", RequestID: "req-reconciled", Outcome: control.OutcomeAccepted, OccurredAt: m.now,
	})
}

func (m *memoryStore) UnfinishedIntents(_ context.Context, grace time.Duration, limit int) ([]control.AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	completed := map[string]bool{}
	for _, entry := range m.audit {
		if entry.Phase == control.PhaseOutcome {
			completed[entry.IntentID] = true
		}
	}
	cutoff := m.now.Add(-grace)
	var unfinished []control.AuditEntry
	for _, entry := range m.audit {
		if entry.Phase != control.PhaseIntent || completed[entry.IntentID] || !entry.OccurredAt.Before(cutoff) {
			continue
		}
		unfinished = append(unfinished, entry)
		if limit > 0 && len(unfinished) == limit {
			break
		}
	}
	return unfinished, nil
}

func (m *memoryStore) auditFail(meta control.Mutation, action, resourceType, resourceID string, version uint64, outcome string, cause error) error {
	m.recordOutcome(meta, action, resourceType, resourceID, version, outcome, cause.Error())
	return cause
}

func (m *memoryStore) auditEntries() []control.AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]control.AuditEntry, len(m.audit))
	copy(out, m.audit)
	return out
}

const testToken = "dmp-admin-token-0123456789abcdef"

func newTestServer(t *testing.T, store control.Store) (*httptest.Server, *memoryStore) {
	t.Helper()
	memory, _ := store.(*memoryStore)
	handler := control.NewHandler(control.HandlerOptions{Store: store, Token: testToken, Actor: "sandbox-operator"})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := observability.WithRequestID(r.Context(), "req-018f7a3b2c9d7e1fa3b4c5d6e7f89060")
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(ts.Close)
	if memory == nil {
		memory = newMemoryStore()
	}
	return ts, memory
}

func call(t *testing.T, ts *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := ts.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}
	}
	return response.StatusCode, payload
}

func rawCall(t *testing.T, ts *httptest.Server, method, path string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	response, err := ts.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, raw
}

func createNamespace(t *testing.T, ts *httptest.Server) {
	t.Helper()
	status, payload := call(t, ts, http.MethodPost, "/admin/v1/namespaces",
		`{"id":"publisher-account-v1","display_name":"Publisher account","hashing_algorithm":"hmac-sha256","subject_key_version":1,"retention_months":3}`)
	if status != http.StatusCreated {
		t.Fatalf("create namespace: %d %v", status, payload)
	}
}

func TestAdminAPIRequiresBearerToken(t *testing.T) {
	ts, _ := newTestServer(t, newMemoryStore())

	response, err := ts.Client().Get(ts.URL + "/admin/v1/namespaces")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token should be 401, got %d", response.StatusCode)
	}

	request, err := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/namespaces", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer wrong-token-wrong-token-wrong-token")
	badResponse, err := ts.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	badResponse.Body.Close()
	if badResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token should be 401, got %d", badResponse.StatusCode)
	}
}

func TestAdminAPIDisabledWithoutConfiguration(t *testing.T) {
	handler := control.NewHandler(control.HandlerOptions{Token: "", Actor: "sandbox-operator"})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	request, err := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/namespaces", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	response, err := ts.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured control plane should be 503, got %d", response.StatusCode)
	}
}

func TestNamespaceVersioningAndConflict(t *testing.T) {
	ts, store := newTestServer(t, newMemoryStore())
	createNamespace(t, ts)

	status, payload := call(t, ts, http.MethodPut, "/admin/v1/namespaces/publisher-account-v1",
		`{"version":1,"display_name":"Publisher account v2","hashing_algorithm":"hmac-sha256","subject_key_version":2,"retention_months":2}`)
	if status != http.StatusOK {
		t.Fatalf("update namespace: %d %v", status, payload)
	}
	if payload["version"] != float64(2) || payload["subject_key_version"] != float64(2) {
		t.Fatalf("unexpected updated definition: %v", payload)
	}

	status, payload = call(t, ts, http.MethodPut, "/admin/v1/namespaces/publisher-account-v1",
		`{"version":1,"display_name":"Stale write","hashing_algorithm":"hmac-sha256","subject_key_version":3,"retention_months":2}`)
	if status != http.StatusConflict || payload["code"] != "version_conflict" {
		t.Fatalf("stale update should be 409 version_conflict, got %d %v", status, payload)
	}
	if payload["request_id"] != "req-018f7a3b2c9d7e1fa3b4c5d6e7f89060" {
		t.Fatalf("error envelope is missing the request id: %v", payload)
	}

	entries := store.auditEntries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 audit entries, got %d: %+v", len(entries), entries)
	}
	last := entries[len(entries)-1]
	if last.Outcome != control.OutcomeConflict || last.Action != control.ActionUpdate || last.Actor != "sandbox-operator" {
		t.Fatalf("conflict was not audited: %+v", last)
	}
	if last.RequestID != "req-018f7a3b2c9d7e1fa3b4c5d6e7f89060" {
		t.Fatalf("audit entry is missing the request id: %+v", last)
	}

	auditStatus, auditBody := rawCall(t, ts, http.MethodGet, "/admin/v1/audit?resource_type=namespace&resource_id=publisher-account-v1")
	if auditStatus != http.StatusOK {
		t.Fatalf("list audit: %d %s", auditStatus, auditBody)
	}
	var auditEntries []control.AuditEntry
	if err := json.Unmarshal(auditBody, &auditEntries); err != nil {
		t.Fatalf("decode audit list: %v", err)
	}
	if len(auditEntries) != 3 || auditEntries[0].Outcome != control.OutcomeConflict {
		t.Fatalf("unexpected audit list: %+v", auditEntries)
	}
}

func TestDefinitionValidation(t *testing.T) {
	ts, _ := newTestServer(t, newMemoryStore())
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		code   string
	}{
		{"uppercase id", http.MethodPost, "/admin/v1/namespaces",
			`{"id":"Publisher-1","display_name":"x","hashing_algorithm":"hmac-sha256","subject_key_version":1,"retention_months":3}`,
			http.StatusUnprocessableEntity, "invalid_definition"},
		{"retention above policy", http.MethodPost, "/admin/v1/namespaces",
			`{"id":"ns-1","display_name":"x","hashing_algorithm":"hmac-sha256","subject_key_version":1,"retention_months":4}`,
			http.StatusUnprocessableEntity, "invalid_definition"},
		{"hashing algorithm", http.MethodPost, "/admin/v1/namespaces",
			`{"id":"ns-1","display_name":"x","hashing_algorithm":"sha1","subject_key_version":1,"retention_months":3}`,
			http.StatusUnprocessableEntity, "invalid_definition"},
		{"unknown field", http.MethodPost, "/admin/v1/namespaces",
			`{"id":"ns-1","display_name":"x","hashing_algorithm":"hmac-sha256","subject_key_version":1,"retention_months":3,"tenant_id":"00000000000000000000000000000009"}`,
			http.StatusBadRequest, "invalid_request"},
		{"missing version on update", http.MethodPut, "/admin/v1/namespaces/ns-1",
			`{"display_name":"x","hashing_algorithm":"hmac-sha256","subject_key_version":1,"retention_months":3}`,
			http.StatusUnprocessableEntity, "invalid_definition"},
		{"missing namespace reference", http.MethodPost, "/admin/v1/providers",
			`{"id":"prov-1","display_name":"x","namespace":"missing-ns","license_version":"lic-1"}`,
			http.StatusUnprocessableEntity, "invalid_definition"},
		{"missing provider reference", http.MethodPost, "/admin/v1/segments",
			`{"id":"seg-1","provider_id":"missing-provider","name":"Gamers"}`,
			http.StatusUnprocessableEntity, "invalid_definition"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, payload := call(t, ts, tc.method, tc.path, tc.body)
			if status != tc.status || payload["code"] != tc.code {
				t.Fatalf("expected %d %s, got %d %v", tc.status, tc.code, status, payload)
			}
		})
	}
}

func TestProviderAndSegmentLifecycle(t *testing.T) {
	ts, store := newTestServer(t, newMemoryStore())
	createNamespace(t, ts)

	status, provider := call(t, ts, http.MethodPost, "/admin/v1/providers",
		`{"id":"dmp-provider-01","display_name":"Data provider","namespace":"publisher-account-v1","license_version":"lic-2026.10"}`)
	if status != http.StatusCreated {
		t.Fatalf("create provider: %d %v", status, provider)
	}
	if provider["version"] != float64(1) || provider["status"] != "active" {
		t.Fatalf("unexpected provider: %v", provider)
	}

	status, payload := call(t, ts, http.MethodPost, "/admin/v1/providers",
		`{"id":"dmp-provider-01","display_name":"Duplicate","namespace":"publisher-account-v1","license_version":"lic-2026.10"}`)
	if status != http.StatusConflict || payload["code"] != "resource_conflict" {
		t.Fatalf("duplicate provider should be 409 resource_conflict, got %d %v", status, payload)
	}

	status, payload = call(t, ts, http.MethodPut, "/admin/v1/providers/dmp-provider-01",
		`{"version":1,"display_name":"Data provider","namespace":"other-namespace","license_version":"lic-2026.11"}`)
	if status != http.StatusUnprocessableEntity || payload["code"] != "immutable_field" {
		t.Fatalf("namespace change should be rejected, got %d %v", status, payload)
	}

	status, segment := call(t, ts, http.MethodPost, "/admin/v1/segments",
		`{"id":"seg-tech-enthusiasts","provider_id":"dmp-provider-01","name":"Tech enthusiasts","description":"Interest segment"}`)
	if status != http.StatusCreated {
		t.Fatalf("create segment: %d %v", status, segment)
	}
	if segment["valid_from"] == nil || segment["version"] != float64(1) {
		t.Fatalf("segment is missing defaulted fields: %v", segment)
	}

	status, payload = call(t, ts, http.MethodPut, "/admin/v1/segments/seg-tech-enthusiasts",
		`{"version":1,"provider_id":"dmp-provider-01","name":"Tech enthusiasts","status":"retired"}`)
	if status != http.StatusOK || payload["status"] != "retired" || payload["version"] != float64(2) {
		t.Fatalf("retire segment: %d %v", status, payload)
	}

	status, payload = call(t, ts, http.MethodPut, "/admin/v1/providers/dmp-provider-01",
		`{"version":1,"display_name":"Data provider","namespace":"publisher-account-v1","license_version":"lic-2026.11"}`)
	if status != http.StatusOK || payload["version"] != float64(2) {
		t.Fatalf("update provider: %d %v", status, payload)
	}

	status, payload = call(t, ts, http.MethodPost, "/admin/v1/segments",
		`{"id":"seg-new","provider_id":"dmp-provider-01","name":"New segment"}`)
	if status != http.StatusCreated {
		t.Fatalf("active provider must accept new segments: %d %v", status, payload)
	}

	entries := store.auditEntries()
	if len(entries) < 6 {
		t.Fatalf("expected every attempt to be audited, got %d entries", len(entries))
	}
}

func TestRetiredNamespaceBlocksNewProviders(t *testing.T) {
	ts, _ := newTestServer(t, newMemoryStore())
	createNamespace(t, ts)

	status, payload := call(t, ts, http.MethodPut, "/admin/v1/namespaces/publisher-account-v1",
		`{"version":1,"display_name":"Publisher account","hashing_algorithm":"hmac-sha256","subject_key_version":1,"retention_months":3,"status":"retired"}`)
	if status != http.StatusOK {
		t.Fatalf("retire namespace: %d %v", status, payload)
	}

	status, payload = call(t, ts, http.MethodPost, "/admin/v1/providers",
		`{"id":"prov-2","display_name":"Provider","namespace":"publisher-account-v1","license_version":"lic-1"}`)
	if status != http.StatusUnprocessableEntity || payload["code"] != "invalid_definition" {
		t.Fatalf("retired namespace must reject new providers, got %d %v", status, payload)
	}
}

func TestListReturnsEmptyArrays(t *testing.T) {
	ts, _ := newTestServer(t, newMemoryStore())
	for _, path := range []string{"/admin/v1/namespaces", "/admin/v1/providers", "/admin/v1/segments", "/admin/v1/audit"} {
		request, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+testToken)
		response, err := ts.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", path, response.StatusCode, raw)
		}
		if strings.TrimSpace(string(raw)) != "[]" {
			t.Fatalf("%s should return an empty JSON array, got %s", path, raw)
		}
	}
}

var _ control.Store = (*memoryStore)(nil)

func TestRejectedAttemptAuditsTheAttemptedResource(t *testing.T) {
	ts, store := newTestServer(t, newMemoryStore())

	status, payload := call(t, ts, http.MethodPost, "/admin/v1/namespaces",
		`{"id":"ns-retention","display_name":"x","hashing_algorithm":"hmac-sha256","subject_key_version":1,"retention_months":4}`)
	if status != http.StatusUnprocessableEntity || payload["code"] != "invalid_definition" {
		t.Fatalf("expected validation failure, got %d %v", status, payload)
	}
	if message, _ := payload["message"].(string); strings.Contains(message, "\n") {
		t.Fatalf("validation message must stay on one line: %q", message)
	}

	var rejected *control.AuditEntry
	for i := range store.auditEntries() {
		entry := store.auditEntries()[i]
		if entry.Outcome == control.OutcomeRejected {
			rejected = &entry
		}
	}
	if rejected == nil {
		t.Fatal("validation rejection was not audited")
	}
	if rejected.ResourceID != "ns-retention" || rejected.ResourceType != "namespace" || rejected.Action != control.ActionCreate {
		t.Fatalf("rejected attempt lost its resource binding: %+v", rejected)
	}
}

func TestAdminRoutingUsesTheErrorEnvelope(t *testing.T) {
	ts, _ := newTestServer(t, newMemoryStore())

	status, payload := call(t, ts, http.MethodGet, "/admin/v1/unknown", "")
	if status != http.StatusNotFound || payload["code"] != "not_found" {
		t.Fatalf("unknown path should be 404 not_found, got %d %v", status, payload)
	}

	status, payload = call(t, ts, http.MethodDelete, "/admin/v1/namespaces/ns-1", "")
	if status != http.StatusMethodNotAllowed || payload["code"] != "method_not_allowed" {
		t.Fatalf("unsupported method should be 405 method_not_allowed, got %d %v", status, payload)
	}

	status, payload = call(t, ts, http.MethodGet, "/admin/v1", "")
	if status != http.StatusNotFound || payload["code"] != "not_found" {
		t.Fatalf("collection root should be 404, got %d %v", status, payload)
	}
}

// unavailableStore fails every call the way the ClickHouse store does when the
// database is unreachable.
type unavailableStore struct {
	control.Store
}

func (unavailableStore) ListNamespaces(context.Context) ([]control.Namespace, error) {
	return nil, fmt.Errorf("%w: connection refused", control.ErrStoreUnavailable)
}

func TestStoreOutageIsReportedAsUnavailable(t *testing.T) {
	ts, _ := newTestServer(t, unavailableStore{})
	status, payload := call(t, ts, http.MethodGet, "/admin/v1/namespaces", "")
	if status != http.StatusServiceUnavailable || payload["code"] != "control_plane_unavailable" {
		t.Fatalf("store outage should be 503 control_plane_unavailable, got %d %v", status, payload)
	}
}

func TestUnfinishedIntentsAreReported(t *testing.T) {
	store := newMemoryStore()
	ts, _ := newTestServer(t, store)

	status, body := rawCall(t, ts, http.MethodGet, "/admin/v1/audit/unfinished")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("no unfinished intents expected: %d %s", status, body)
	}

	intentID := store.seedIntent("sandbox-operator", control.ActionCreate, "provider", "prov-orphan")
	status, body = rawCall(t, ts, http.MethodGet, "/admin/v1/audit/unfinished")
	if status != http.StatusOK {
		t.Fatalf("list unfinished intents: %d %s", status, body)
	}
	var entries []control.AuditEntry
	if err := json.Unmarshal([]byte(body), &entries); err != nil {
		t.Fatalf("decode unfinished intents: %v (%s)", err, body)
	}
	if len(entries) != 1 || entries[0].IntentID != intentID || entries[0].Phase != control.PhaseIntent {
		t.Fatalf("the outstanding attempt must be reported: %+v", entries)
	}

	// Recording the outcome clears it from the report.
	store.resolveIntent(intentID)
	status, body = rawCall(t, ts, http.MethodGet, "/admin/v1/audit/unfinished")
	if status != http.StatusOK {
		t.Fatalf("list unfinished intents: %d %s", status, body)
	}
	if err := json.Unmarshal([]byte(body), &entries); err != nil {
		t.Fatalf("decode unfinished intents: %v (%s)", err, body)
	}
	for _, entry := range entries {
		if entry.IntentID == intentID {
			t.Fatalf("a reconciled attempt must no longer be reported: %+v", entries)
		}
	}
}

// unrecordableAudit refuses to persist audit rows, simulating an audit outage.
type unrecordableAudit struct {
	control.Store
}

func (u unrecordableAudit) RecordRejected(context.Context, control.Mutation, string, string, string, string) error {
	return control.ErrStoreUnavailable
}

func TestRejectionFailsClosedWhenItCannotBeAudited(t *testing.T) {
	store := unrecordableAudit{newMemoryStore()}
	handler := control.NewHandler(control.HandlerOptions{Store: store, Token: testToken, Actor: "sandbox-operator"})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(observability.WithRequestID(r.Context(), "req-audit-outage")))
	}))
	t.Cleanup(ts.Close)

	status, payload := call(t, ts, http.MethodPost, "/admin/v1/namespaces",
		`{"id":"ns-retention","display_name":"x","hashing_algorithm":"hmac-sha256","subject_key_version":1,"retention_months":4}`)
	if status != http.StatusServiceUnavailable || payload["code"] != "control_plane_unavailable" {
		t.Fatalf("an unauditable rejection must fail closed, got %d %v", status, payload)
	}
}
