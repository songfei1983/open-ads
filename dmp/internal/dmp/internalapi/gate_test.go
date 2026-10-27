package internalapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/gate"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/internalapi"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/revocation"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/snapshot"
)

type fakeGate struct {
	mu            sync.Mutex
	denied        map[string]bool
	admitErr      error
	finalizeErr   error
	inFlightCount uint64
	denyCount     uint64
	admitted      int
	finalized     int
}

func newFakeGate() *fakeGate {
	return &fakeGate{denied: map[string]bool{}}
}

func (g *fakeGate) Admit(_ context.Context, namespace, token, _ string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.admitted++
	if g.admitErr != nil {
		return g.admitErr
	}
	if g.denied[namespace+":"+token] {
		return gate.ErrSubjectDenied
	}
	return nil
}

func (g *fakeGate) Finalize(_ context.Context, namespace, token, _ string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.finalized++
	if g.finalizeErr != nil {
		return g.finalizeErr
	}
	if g.denied[namespace+":"+token] {
		return gate.ErrSubjectDenied
	}
	return nil
}

func (g *fakeGate) Deny(_ context.Context, namespace, token, _ string) (uint64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.denied[namespace+":"+token] = true
	return g.denyCount, nil
}

func (g *fakeGate) InFlight(context.Context, string, string) (uint64, error) {
	return g.inFlightCount, nil
}

func (g *fakeGate) Epoch(context.Context) (uint64, error) {
	return 7, nil
}

type fakeRevocations struct {
	mu       sync.Mutex
	requests []revocation.Request
	statuses map[string]revocation.Status
}

func newFakeRevocations() *fakeRevocations {
	return &fakeRevocations{statuses: map[string]revocation.Status{}}
}

func (f *fakeRevocations) Record(_ context.Context, request revocation.Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	return nil
}

func (f *fakeRevocations) AppendStatus(_ context.Context, status revocation.Status) (revocation.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	previous := f.statuses[status.RevocationID]
	status.StatusVersion = previous.StatusVersion + 1
	status.UpdatedAt = time.Unix(0, 0).UTC()
	f.statuses[status.RevocationID] = status
	return status, nil
}

func (f *fakeRevocations) LatestStatus(_ context.Context, id string) (revocation.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status, ok := f.statuses[id]
	if !ok {
		return revocation.Status{}, revocation.ErrNotFound
	}
	return status, nil
}

func (f *fakeRevocations) ListStatuses(context.Context, int) ([]revocation.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	statuses := make([]revocation.Status, 0, len(f.statuses))
	for _, status := range f.statuses {
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func newGatedServer(t *testing.T) (*fakeSnapshots, *fakeGate, *fakeRevocations, *httptest.Server, func(string) (int, map[string]any)) {
	t.Helper()
	snapshots := newFakeSnapshots()
	denyGate := newFakeGate()
	revocations := newFakeRevocations()
	handler := internalapi.NewHandler(internalapi.HandlerOptions{
		Token: testToken, Snapshots: snapshots, Gate: denyGate, Revocations: revocations,
	})
	server := newServerFromHandler(t, handler)
	resolve := func(token string) (int, map[string]any) {
		return call(t, server, testToken, http.MethodPost, "/internal/v1/audiences:resolve",
			`{"namespace":"publisher-account-v1","provider_id":"dmp-provider-01","subject_token":"`+token+`"}`)
	}
	return snapshots, denyGate, revocations, server, resolve
}

func TestResolveFailsClosedWhenGateIsUnavailable(t *testing.T) {
	snapshots, denyGate, _, _, resolve := newGatedServer(t)
	token := strings.Repeat("11", 32)

	denyGate.admitErr = gate.ErrUnavailable
	status, payload := resolve(token)
	if status != http.StatusServiceUnavailable || payload["code"] != "policy_state_unavailable" {
		t.Fatalf("gate outage must fail closed with 503, got %d %v", status, payload)
	}
	if snapshots.resolveCalls != 0 {
		t.Fatalf("a refused admission must not query the snapshot store")
	}

	denyGate.admitErr = nil
	denyGate.denied["publisher-account-v1:"+token] = true
	status, payload = resolve(token)
	if status != http.StatusOK {
		t.Fatalf("revoked subject must resolve without an audience, got %d %v", status, payload)
	}
	if segments, _ := payload["segments"].([]any); len(segments) != 0 {
		t.Fatalf("revoked subject must not receive segments: %v", payload)
	}
	if snapshots.resolveCalls != 0 {
		t.Fatalf("revoked subject must not reach storage")
	}
}

func TestResolveDiscardsSegmentsWhenFinalizeSeesRevocation(t *testing.T) {
	snapshots, denyGate, _, _, resolve := newGatedServer(t)
	token := strings.Repeat("22", 32)
	if _, err := snapshots.CreateJob(context.Background(), snapshot.CreateJobInput{
		ProviderID: "dmp-provider-01", Namespace: "publisher-account-v1", SchemaVersion: 1,
		SourceVersion: "feed-gate", ExpectedRows: 1,
	}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	for id, job := range snapshots.jobs {
		job.job.Status = snapshot.StatusPublished
		snapshots.publications["dmp-provider-01|publisher-account-v1"] = snapshot.Publication{
			ProviderID: "dmp-provider-01", Namespace: "publisher-account-v1", SnapshotID: id,
		}
		snapshots.published[id] = snapshots.publications["dmp-provider-01|publisher-account-v1"]
		snapshots.rows[id] = []snapshot.MemberKey{{SubjectToken: token, SubjectKeyVersion: 1, SegmentID: "seg-tech-enthusiasts"}}
	}
	snapshots.resolveCalls = 0

	// The subject is revoked between admission and finalize.
	denyGate.finalizeErr = gate.ErrSubjectDenied
	status, payload := resolve(token)
	if status != http.StatusOK {
		t.Fatalf("finalize rejection should still answer 200, got %d %v", status, payload)
	}
	if segments, _ := payload["segments"].([]any); len(segments) != 0 {
		t.Fatalf("segments resolved before finalize must be discarded: %v", payload)
	}

	// An epoch mismatch at finalize means the answer is untrustworthy.
	denyGate.finalizeErr = gate.ErrEpochMismatch
	status, payload = resolve(token)
	if status != http.StatusServiceUnavailable || payload["code"] != "policy_state_unavailable" {
		t.Fatalf("epoch mismatch must fail closed, got %d %v", status, payload)
	}
}

func TestRevokeBlocksAdmissionAndReportsStatus(t *testing.T) {
	_, denyGate, revocations, server, resolve := newGatedServer(t)
	token := strings.Repeat("33", 32)

	status, payload := call(t, server, testToken, http.MethodPost, "/internal/v1/identity:revoke",
		`{"namespace":"publisher-account-v1","subject_tokens":[{"token":"`+token+`","key_version":1}],"reason_code":"user_request"}`)
	if status != http.StatusAccepted {
		t.Fatalf("revoke: %d %v", status, payload)
	}
	if payload["status"] != "completed" || payload["online_status"] != revocation.OnlineDenied {
		t.Fatalf("expected completed revocation, got %v", payload)
	}
	revocationID, _ := payload["revocation_id"].(string)
	if revocationID == "" {
		t.Fatalf("revocation id missing: %v", payload)
	}
	if !denyGate.denied["publisher-account-v1:"+token] {
		t.Fatal("deny marker was not set")
	}
	if len(revocations.requests) != 1 || revocations.requests[0].Tokens[0].Token != token {
		t.Fatalf("tombstone was not recorded: %+v", revocations.requests)
	}

	// New lookups for the revoked subject are blocked.
	status, payload = resolve(token)
	if status != http.StatusOK {
		t.Fatalf("revoked resolve: %d %v", status, payload)
	}
	if segments, _ := payload["segments"].([]any); len(segments) != 0 {
		t.Fatalf("revoked subject received segments: %v", payload)
	}

	status, payload = call(t, server, testToken, http.MethodGet, "/internal/v1/identity-revocations/"+revocationID, "")
	if status != http.StatusOK || payload["revocation_id"] != revocationID {
		t.Fatalf("status lookup: %d %v", status, payload)
	}
	if payload["physical_delete_status"] != revocation.DeletePending {
		t.Fatalf("physical deletion must stay pending: %v", payload)
	}
}

func TestRevokeRejectsRawIdentityAndUnknownStatus(t *testing.T) {
	_, _, _, server, _ := newGatedServer(t)

	status, payload := call(t, server, testToken, http.MethodPost, "/internal/v1/identity:revoke",
		`{"namespace":"publisher-account-v1","subject_tokens":[{"token":"alice@example.com","key_version":1}]}`)
	if status != http.StatusBadRequest || payload["code"] != "invalid_request" {
		t.Fatalf("raw identity revoke must be rejected, got %d %v", status, payload)
	}

	status, payload = call(t, server, testToken, http.MethodGet, "/internal/v1/identity-revocations/01a17d00-0000-7000-8000-000000000099", "")
	if status != http.StatusNotFound || payload["code"] != "not_found" {
		t.Fatalf("unknown revocation must be 404, got %d %v", status, payload)
	}
}

func TestRevokeRequiresGateAndFailsClosedOnGateErrors(t *testing.T) {
	snapshots := newFakeSnapshots()
	// No gate/revocation stores configured.
	handler := internalapi.NewHandler(internalapi.HandlerOptions{Token: testToken, Snapshots: snapshots})
	server := newServerFromHandler(t, handler)
	status, payload := call(t, server, testToken, http.MethodPost, "/internal/v1/identity:revoke",
		`{"namespace":"publisher-account-v1","subject_tokens":[{"token":"`+strings.Repeat("44", 32)+`","key_version":1}]}`)
	if status != http.StatusServiceUnavailable || payload["code"] != "internal_api_unconfigured" {
		t.Fatalf("unconfigured gate must be 503, got %d %v", status, payload)
	}
}
