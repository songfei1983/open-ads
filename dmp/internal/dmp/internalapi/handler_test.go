package internalapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/internalapi"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/observability"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/snapshot"
)

const testToken = "dmp-internal-token-0123456789abcdef"

// fakeSnapshots models the import lifecycle closely enough to assert the HTTP
// contract: raw identity tokens never reach storage, and verification decides
// whether a job may be published.
type fakeSnapshots struct {
	mu   sync.Mutex
	jobs map[string]*fakeJob
	rows map[string][]snapshot.MemberKey
}

type fakeJob struct {
	job snapshot.Job
}

func newFakeSnapshots() *fakeSnapshots {
	return &fakeSnapshots{jobs: map[string]*fakeJob{}, rows: map[string][]snapshot.MemberKey{}}
}

func (f *fakeSnapshots) CreateJob(_ context.Context, input snapshot.CreateJobInput) (snapshot.Job, error) {
	if err := input.Validate(); err != nil {
		return snapshot.Job{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	job := snapshot.Job{
		SnapshotID: "01a17d00000070008000000000000001", ProviderID: input.ProviderID, Namespace: input.Namespace,
		SchemaVersion: input.SchemaVersion, SourceVersion: input.SourceVersion, ExpectedRows: input.ExpectedRows,
		ExpectedChecksum: input.ExpectedChecksum, Status: snapshot.StatusImporting,
		CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC(),
	}
	f.jobs[job.SnapshotID] = &fakeJob{job: job}
	return job, nil
}

func (f *fakeSnapshots) ListJobs(context.Context) ([]snapshot.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	jobs := make([]snapshot.Job, 0, len(f.jobs))
	for _, job := range f.jobs {
		jobs = append(jobs, job.job)
	}
	return jobs, nil
}

func (f *fakeSnapshots) GetJob(_ context.Context, id string) (snapshot.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if !ok {
		return snapshot.Job{}, snapshot.ErrJobNotFound
	}
	return job.job, nil
}

func (f *fakeSnapshots) AppendRows(_ context.Context, id string, rows []snapshot.MemberRow) (snapshot.AppendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if !ok {
		return snapshot.AppendResult{}, snapshot.ErrJobNotFound
	}
	if job.job.Status != snapshot.StatusImporting {
		return snapshot.AppendResult{}, snapshot.ErrJobState
	}
	result := snapshot.AppendResult{SubmittedRows: uint64(len(rows))}
	seen := make(map[snapshot.MemberKey]struct{})
	existing := make(map[snapshot.MemberKey]struct{}, len(f.rows[id]))
	for _, key := range f.rows[id] {
		existing[key] = struct{}{}
	}
	for i := range rows {
		if err := rows[i].Validate(); err != nil {
			return snapshot.AppendResult{}, err
		}
		for _, segmentID := range rows[i].SegmentIDs {
			key := snapshot.MemberKey{SubjectToken: rows[i].SubjectToken, SubjectKeyVersion: rows[i].SubjectKeyVersion, SegmentID: segmentID}
			if _, ok := seen[key]; ok {
				result.DuplicateRows++
				continue
			}
			seen[key] = struct{}{}
			if _, ok := existing[key]; ok {
				result.IdempotentRows++
				continue
			}
			f.rows[id] = append(f.rows[id], key)
			result.StoredRows++
		}
	}
	job.job.SubmittedRows += result.SubmittedRows
	job.job.DuplicateRows += result.DuplicateRows
	job.job.StoredRows = uint64(len(f.rows[id]))
	return result, nil
}

func (f *fakeSnapshots) Verify(_ context.Context, id string) (snapshot.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if !ok {
		return snapshot.Job{}, snapshot.ErrJobNotFound
	}
	if job.job.Status == snapshot.StatusVerified {
		return job.job, nil
	}
	if job.job.Status != snapshot.StatusImporting {
		return job.job, snapshot.ErrJobState
	}
	job.job.StoredRows = uint64(len(f.rows[id]))
	checksum := snapshot.Checksum(f.rows[id])
	if job.job.StoredRows != job.job.ExpectedRows {
		job.job.Status = snapshot.StatusFailed
		job.job.FailureReason = "row count mismatch"
		return job.job, snapshot.ErrVerification
	}
	if job.job.ExpectedChecksum != "" && job.job.ExpectedChecksum != checksum {
		job.job.Status = snapshot.StatusFailed
		job.job.FailureReason = "checksum mismatch"
		return job.job, snapshot.ErrVerification
	}
	job.job.Status = snapshot.StatusVerified
	return job.job, nil
}

func newServer(t *testing.T, token string, snapshots internalapi.SnapshotService) *httptest.Server {
	t.Helper()
	handler := internalapi.NewHandler(internalapi.HandlerOptions{Token: token, Snapshots: snapshots})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(observability.WithRequestID(r.Context(), "req-internal-1")))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func call(t *testing.T, ts *httptest.Server, token, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
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
			t.Fatalf("decode %q: %v", raw, err)
		}
	}
	return response.StatusCode, payload
}

func TestInternalAPIRequiresTrustedToken(t *testing.T) {
	ts := newServer(t, testToken, newFakeSnapshots())

	status, payload := call(t, ts, "", http.MethodGet, "/internal/v1/snapshots", "")
	if status != http.StatusUnauthorized || payload["code"] != "unauthenticated" {
		t.Fatalf("missing token should be 401, got %d %v", status, payload)
	}

	disabled := newServer(t, "", newFakeSnapshots())
	status, payload = call(t, disabled, testToken, http.MethodGet, "/internal/v1/snapshots", "")
	if status != http.StatusServiceUnavailable || payload["code"] != "internal_api_unconfigured" {
		t.Fatalf("unconfigured API should be 503, got %d %v", status, payload)
	}
}

func TestSnapshotImportRejectsRawIdentifiers(t *testing.T) {
	ts := newServer(t, testToken, newFakeSnapshots())
	status, payload := call(t, ts, testToken, http.MethodPost, "/internal/v1/snapshots",
		`{"provider_id":"dmp-provider-01","namespace":"publisher-account-v1","schema_version":1,"source_version":"feed-2026-10-27","expected_rows":1}`)
	if status != http.StatusCreated {
		t.Fatalf("create job: %d %v", status, payload)
	}
	snapshotID, _ := payload["snapshot_id"].(string)

	for _, raw := range []string{"alice@example.com", "+819012345678", "device-1234", strings.Repeat("a", 63), strings.Repeat("a", 65)} {
		status, payload = call(t, ts, testToken, http.MethodPost, "/internal/v1/snapshots/"+snapshotID+"/rows",
			`{"rows":[{"subject_token":"`+raw+`","subject_key_version":1,"segment_ids":["seg-tech-enthusiasts"]}]}`)
		if status != http.StatusBadRequest || payload["code"] != "invalid_request" {
			t.Fatalf("raw identity %q must be rejected, got %d %v", raw, status, payload)
		}
	}
}

func TestSnapshotImportVerifyFlow(t *testing.T) {
	ts := newServer(t, testToken, newFakeSnapshots())
	token := strings.Repeat("ab", 32)
	status, payload := call(t, ts, testToken, http.MethodPost, "/internal/v1/snapshots",
		`{"provider_id":"dmp-provider-01","namespace":"publisher-account-v1","schema_version":1,"source_version":"feed-1","expected_rows":2}`)
	if status != http.StatusCreated {
		t.Fatalf("create job: %d %v", status, payload)
	}
	snapshotID, _ := payload["snapshot_id"].(string)

	status, payload = call(t, ts, testToken, http.MethodPost, "/internal/v1/snapshots/"+snapshotID+"/rows",
		`{"rows":[{"subject_token":"`+token+`","subject_key_version":1,"segment_ids":["seg-tech-enthusiasts","seg-sports-fans"]}]}`)
	if status != http.StatusOK || payload["stored_rows"] != float64(2) {
		t.Fatalf("append rows: %d %v", status, payload)
	}

	// Re-delivering the same batch is a no-op: the import stays idempotent.
	status, payload = call(t, ts, testToken, http.MethodPost, "/internal/v1/snapshots/"+snapshotID+"/rows",
		`{"rows":[{"subject_token":"`+token+`","subject_key_version":1,"segment_ids":["seg-tech-enthusiasts","seg-sports-fans"]}]}`)
	if status != http.StatusOK || payload["stored_rows"] != float64(0) {
		t.Fatalf("re-delivered batch should store nothing, got %d %v", status, payload)
	}

	status, payload = call(t, ts, testToken, http.MethodPost, "/internal/v1/snapshots/"+snapshotID+"/verify", "")
	if status != http.StatusOK || payload["status"] != snapshot.StatusVerified {
		t.Fatalf("verify: %d %v", status, payload)
	}

	status, payload = call(t, ts, testToken, http.MethodPost, "/internal/v1/snapshots/"+snapshotID+"/verify", "")
	if status != http.StatusOK || payload["status"] != snapshot.StatusVerified {
		t.Fatalf("re-verify must be idempotent: %d %v", status, payload)
	}
}

func TestSnapshotVerificationFailureKeepsPreviousPublication(t *testing.T) {
	ts := newServer(t, testToken, newFakeSnapshots())
	token := strings.Repeat("cd", 32)
	status, payload := call(t, ts, testToken, http.MethodPost, "/internal/v1/snapshots",
		`{"provider_id":"dmp-provider-01","namespace":"publisher-account-v1","schema_version":1,"source_version":"feed-2","expected_rows":5}`)
	if status != http.StatusCreated {
		t.Fatalf("create job: %d %v", status, payload)
	}
	snapshotID, _ := payload["snapshot_id"].(string)
	call(t, ts, testToken, http.MethodPost, "/internal/v1/snapshots/"+snapshotID+"/rows",
		`{"rows":[{"subject_token":"`+token+`","subject_key_version":1,"segment_ids":["seg-tech-enthusiasts"]}]}`)

	status, payload = call(t, ts, testToken, http.MethodPost, "/internal/v1/snapshots/"+snapshotID+"/verify", "")
	if status != http.StatusUnprocessableEntity || payload["code"] != "verification_failed" {
		t.Fatalf("row-count mismatch must fail verification, got %d %v", status, payload)
	}
	job, _ := payload["job"].(map[string]any)
	if job == nil || job["status"] != snapshot.StatusFailed {
		t.Fatalf("failed verification must be recorded on the job: %v", payload)
	}

	status, payload = call(t, ts, testToken, http.MethodPost, "/internal/v1/snapshots/"+snapshotID+"/rows",
		`{"rows":[{"subject_token":"`+token+`","subject_key_version":1,"segment_ids":["seg-sports-fans"]}]}`)
	if status != http.StatusConflict || payload["code"] != "invalid_state" {
		t.Fatalf("failed job must stop accepting rows, got %d %v", status, payload)
	}
}

func TestInternalAPIRoutingUsesErrorEnvelope(t *testing.T) {
	ts := newServer(t, testToken, newFakeSnapshots())
	status, payload := call(t, ts, testToken, http.MethodGet, "/internal/v1/nope", "")
	if status != http.StatusNotFound || payload["code"] != "not_found" {
		t.Fatalf("unknown path: %d %v", status, payload)
	}
	status, payload = call(t, ts, testToken, http.MethodDelete, "/internal/v1/snapshots/x", "")
	if status != http.StatusMethodNotAllowed || payload["code"] != "method_not_allowed" {
		t.Fatalf("unsupported method: %d %v", status, payload)
	}
}
