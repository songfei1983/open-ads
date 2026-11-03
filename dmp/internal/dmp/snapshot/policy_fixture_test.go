package snapshot_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/snapshot"
	"github.com/songfei1983/open-ads/identity"
)

// sharedFixture mirrors fixtures/identity/subject_tokens.json.
type sharedFixture struct {
	Accept []struct {
		Value string `json:"value"`
	} `json:"accept"`
	Reject []struct {
		Value string `json:"value"`
		Kind  string `json:"kind"`
	} `json:"reject"`
}

func loadSharedFixture(t *testing.T) sharedFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "fixtures", "identity", "subject_tokens.json"))
	if err != nil {
		t.Fatalf("read shared fixture: %v", err)
	}
	var loaded sharedFixture
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatalf("decode shared fixture: %v", err)
	}
	return loaded
}

// TestMemberRowsApplyTheSharedTokenPolicy proves the import path rejects every
// raw identifier the shared fixture declares, and accepts its digests.
func TestMemberRowsApplyTheSharedTokenPolicy(t *testing.T) {
	loaded := loadSharedFixture(t)
	for _, item := range loaded.Accept {
		row := snapshot.MemberRow{SubjectToken: item.Value, SubjectKeyVersion: 1, SegmentIDs: []string{"seg-1"}}
		if err := row.Validate(); err != nil {
			t.Fatalf("import must accept %q: %v", item.Value, err)
		}
	}
	for _, item := range loaded.Reject {
		row := snapshot.MemberRow{SubjectToken: item.Value, SubjectKeyVersion: 1, SegmentIDs: []string{"seg-1"}}
		err := row.Validate()
		if !errors.Is(err, snapshot.ErrInvalidImport) {
			t.Fatalf("import must reject %q: %v", item.Value, err)
		}
		if kind, ok := identity.KindOf(err); !ok || string(kind) != item.Kind {
			t.Fatalf("%q must classify as %q, got %q (%v)", item.Value, item.Kind, kind, err)
		}
	}
}

// TestResolveAppliesTheSharedTokenPolicy covers the lookup path, which must
// refuse raw identifiers before touching the deny gate or storage.
func TestResolveAppliesTheSharedTokenPolicy(t *testing.T) {
	loaded := loadSharedFixture(t)
	for _, item := range loaded.Reject {
		input := snapshot.ResolveInput{Namespace: "publisher-account-v1", SubjectToken: item.Value}
		if err := input.Validate(); !errors.Is(err, snapshot.ErrInvalidImport) {
			t.Fatalf("resolve must reject %q: %v", item.Value, err)
		}
	}
}
