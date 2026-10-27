package identity_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/songfei1983/open-ads/identity"
)

type fixture struct {
	Accept []struct {
		Value string `json:"value"`
		Note  string `json:"note"`
	} `json:"accept"`
	Reject []struct {
		Value string `json:"value"`
		Kind  string `json:"kind"`
		Note  string `json:"note"`
	} `json:"reject"`
}

// loadFixture reads the shared negative fixture set so every service validates
// the same cases.
func loadFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "fixtures", "identity", "subject_tokens.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var loaded fixture
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(loaded.Accept) == 0 || len(loaded.Reject) == 0 {
		t.Fatal("fixture must cover accepting and rejecting cases")
	}
	return loaded
}

func TestPolicyAcceptsOnlyPseudonymousDigests(t *testing.T) {
	policy := identity.Default()
	for _, item := range loadFixture(t).Accept {
		if err := policy.Validate(item.Value); err != nil {
			t.Fatalf("must accept %q (%s): %v", item.Value, item.Note, err)
		}
		if kind := identity.Classify(item.Value); kind != "" {
			t.Fatalf("accepted value %q classified as %q (%s)", item.Value, kind, item.Note)
		}
	}
}

func TestPolicyRejectsEveryRawIdentifierFixture(t *testing.T) {
	policy := identity.Default()
	for _, item := range loadFixture(t).Reject {
		err := policy.Validate(item.Value)
		if !errors.Is(err, identity.ErrInvalidToken) {
			t.Fatalf("must reject %q (%s): got %v", item.Value, item.Note, err)
		}
		if kind := string(identity.Classify(item.Value)); kind != item.Kind {
			t.Fatalf("%q classified as %q, fixture expects %q (%s)", item.Value, kind, item.Kind, item.Note)
		}
	}
}

func TestPolicyMarksRawIdentifiersSeparately(t *testing.T) {
	policy := identity.Default()
	for _, value := range []string{"alice@example.com", "+8613800138000", "13800138000",
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301", "device_id_0123456789abcdef"} {
		if err := policy.Validate(value); !errors.Is(err, identity.ErrRawIdentity) {
			t.Fatalf("%q must be reported as a raw identity, got %v", value, err)
		}
	}
	// A malformed digest is invalid but not a raw identity, so the two cases can
	// be alerted on separately.
	if err := policy.Validate("abc"); errors.Is(err, identity.ErrRawIdentity) {
		t.Fatal("a short non-hex value is not a raw identity")
	}
}

func TestNormalizeCanonicalisesTokens(t *testing.T) {
	if got := identity.Normalize("  ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789 "); got != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
		t.Fatalf("unexpected canonical form: %q", got)
	}
}
