package internalapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/internalapi"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/observability"
)

// memoryIdempotency records responses the way the ClickHouse store does.
type memoryIdempotency struct {
	records map[string]internalapi.IdempotencyRecord
	saves   int
}

func newMemoryIdempotency() *memoryIdempotency {
	return &memoryIdempotency{records: map[string]internalapi.IdempotencyRecord{}}
}

func (m *memoryIdempotency) Lookup(_ context.Context, key, route string) (internalapi.IdempotencyRecord, bool, error) {
	record, ok := m.records[key+"|"+route]
	return record, ok, nil
}

func (m *memoryIdempotency) Save(_ context.Context, record internalapi.IdempotencyRecord) error {
	m.saves++
	m.records[record.Key+"|"+record.Route] = record
	return nil
}

func newIdempotentServer(t *testing.T, store internalapi.IdempotencyStoreAPI) (*httptest.Server, *fakeSnapshots) {
	t.Helper()
	snapshots := newFakeSnapshots()
	handler := internalapi.NewHandler(internalapi.HandlerOptions{
		Token: testToken, TenantID: testTenantID, Snapshots: snapshots, Idempotency: store,
	})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(observability.WithRequestID(r.Context(), "req-idempotency")))
	}))
	t.Cleanup(ts.Close)
	return ts, snapshots
}

func postWithKey(t *testing.T, ts *httptest.Server, path, body, key string) (*http.Response, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := ts.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, string(payload)
}

func TestIdempotencyKeyReplaysTheOriginalSnapshot(t *testing.T) {
	store := newMemoryIdempotency()
	ts, snapshots := newIdempotentServer(t, store)
	body := `{"provider_id":"dmp-provider-01","namespace":"publisher-account-v1","schema_version":1,"source_version":"feed-idem","expected_rows":1}`

	first, firstBody := postWithKey(t, ts, "/internal/v1/snapshots", body, "key-snapshot-1")
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first import: %d %s", first.StatusCode, firstBody)
	}
	if first.Header.Get("Idempotency-Replayed") != "" {
		t.Fatal("the first answer must not be marked as a replay")
	}

	second, secondBody := postWithKey(t, ts, "/internal/v1/snapshots", body, "key-snapshot-1")
	if second.StatusCode != http.StatusCreated {
		t.Fatalf("replay status: %d %s", second.StatusCode, secondBody)
	}
	if second.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("the replay must be marked with Idempotency-Replayed")
	}
	if firstBody != secondBody {
		t.Fatalf("replay must return the original body:\nfirst  %s\nsecond %s", firstBody, secondBody)
	}
	if created := len(snapshots.jobs); created != 1 {
		t.Fatalf("the retry must not create a second snapshot, got %d", created)
	}

	// A different payload under the same key is refused rather than silently
	// replayed.
	conflict, conflictBody := postWithKey(t, ts, "/internal/v1/snapshots",
		`{"provider_id":"dmp-provider-01","namespace":"publisher-account-v1","schema_version":1,"source_version":"feed-other","expected_rows":1}`,
		"key-snapshot-1")
	if conflict.StatusCode != http.StatusConflict || !strings.Contains(conflictBody, "idempotency_key_reuse") {
		t.Fatalf("reused key with a new body must be 409, got %d %s", conflict.StatusCode, conflictBody)
	}
	if len(snapshots.jobs) != 1 {
		t.Fatalf("a refused reuse must not create a snapshot, got %d", len(snapshots.jobs))
	}
}

func TestIdempotencyKeyCoversRevocation(t *testing.T) {
	store := newMemoryIdempotency()
	snapshots := newFakeSnapshots()
	handler := internalapi.NewHandler(internalapi.HandlerOptions{
		Token: testToken, TenantID: testTenantID, Snapshots: snapshots,
		Gate: newFakeGate(), Revocations: newFakeRevocations(), Idempotency: store,
	})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(observability.WithRequestID(r.Context(), "req-idempotency")))
	}))
	t.Cleanup(ts.Close)

	token := strings.Repeat("ab", 32)
	body := `{"namespace":"publisher-account-v1","subject_tokens":[{"token":"` + token + `","key_version":1}],"reason_code":"user_request"}`
	first, firstBody := postWithKey(t, ts, "/internal/v1/identity:revoke", body, "key-revoke-1")
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("revoke: %d %s", first.StatusCode, firstBody)
	}
	second, secondBody := postWithKey(t, ts, "/internal/v1/identity:revoke", body, "key-revoke-1")
	if second.StatusCode != http.StatusAccepted || second.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("revoke replay: %d %s", second.StatusCode, secondBody)
	}
	if firstBody != secondBody {
		t.Fatalf("revoke replay must return the original body:\nfirst  %s\nsecond %s", firstBody, secondBody)
	}
	// Only the original answer is stored; the replay must not overwrite it.
	if store.saves != 1 || len(store.records) != 1 {
		t.Fatalf("expected exactly one stored revocation answer, got %d saves and %d records", store.saves, len(store.records))
	}
}

func TestIdempotencyKeyRefusedWhenUnconfigured(t *testing.T) {
	ts, snapshots := newIdempotentServer(t, nil)
	response, body := postWithKey(t, ts, "/internal/v1/snapshots",
		`{"provider_id":"dmp-provider-01","namespace":"publisher-account-v1","schema_version":1,"source_version":"feed-x","expected_rows":1}`,
		"key-unconfigured")
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "idempotency") {
		t.Fatalf("an unconfigured store must refuse the header, got %d %s", response.StatusCode, body)
	}
	if len(snapshots.jobs) != 0 {
		t.Fatal("a refused request must not create a snapshot")
	}
	// Without the header the endpoint keeps working unchanged.
	response, body = postWithKey(t, ts, "/internal/v1/snapshots",
		`{"provider_id":"dmp-provider-01","namespace":"publisher-account-v1","schema_version":1,"source_version":"feed-y","expected_rows":1}`, "")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("requests without the header must still work: %d %s", response.StatusCode, body)
	}
}
