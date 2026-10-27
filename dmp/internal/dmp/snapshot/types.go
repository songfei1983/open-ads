// Package snapshot implements the DMP membership snapshot lifecycle: trusted
// tokenized import, integrity verification, immutable publication and the
// published-snapshot-only audience lookup.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Import and publication failures.
var (
	ErrInvalidImport    = errors.New("invalid snapshot import")
	ErrJobNotFound      = errors.New("snapshot import job not found")
	ErrNotVerified      = errors.New("snapshot is not verified")
	ErrVerification     = errors.New("snapshot verification failed")
	ErrStoreUnavailable = errors.New("snapshot store unavailable")
	ErrNoPublication    = errors.New("no published snapshot")
	ErrJobState         = errors.New("snapshot import job is not in a valid state for this operation")
	ErrSealed           = errors.New("published snapshot is sealed")
)

// Import job states.
const (
	StatusImporting = "importing"
	StatusVerified  = "verified"
	StatusFailed    = "failed"
	StatusPublished = "published"
	StatusRetired   = "retired"
)

// MaxBatchRows bounds one membership append request.
const MaxBatchRows = 1000

// MaxDuplicateRate is the data-quality ceiling applied during verification.
const MaxDuplicateRate = 0.05

// subjectTokenPattern is the only accepted token shape: 64 lowercase hex
// characters, i.e. an HMAC-SHA256 (or equivalent) digest. Raw identifiers such
// as e-mail addresses, phone numbers or device IDs can never match it.
var subjectTokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var sourceVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

// Job is the current state of a snapshot import.
type Job struct {
	SnapshotID       string    `json:"snapshot_id"`
	ProviderID       string    `json:"provider_id"`
	Namespace        string    `json:"namespace"`
	SchemaVersion    uint16    `json:"schema_version"`
	SourceVersion    string    `json:"source_version"`
	ExpectedRows     uint64    `json:"expected_rows"`
	ExpectedChecksum string    `json:"expected_checksum,omitempty"`
	SubmittedRows    uint64    `json:"submitted_rows"`
	DuplicateRows    uint64    `json:"duplicate_rows"`
	StoredRows       uint64    `json:"stored_rows"`
	PurgedRows       uint64    `json:"purged_rows"`
	Status           string    `json:"status"`
	FailureReason    string    `json:"failure_reason,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`

	stateVersion uint64
}

// DuplicateRate is the share of submitted rows rejected as duplicates.
func (j Job) DuplicateRate() float64 {
	if j.SubmittedRows == 0 {
		return 0
	}
	return float64(j.DuplicateRows) / float64(j.SubmittedRows)
}

// CreateJobInput registers a new import.
type CreateJobInput struct {
	ProviderID       string `json:"provider_id"`
	Namespace        string `json:"namespace"`
	SchemaVersion    uint16 `json:"schema_version"`
	SourceVersion    string `json:"source_version"`
	ExpectedRows     uint64 `json:"expected_rows"`
	ExpectedChecksum string `json:"expected_checksum,omitempty"`
}

// Validate checks the registration fields.
func (in *CreateJobInput) Validate() error {
	in.ProviderID = strings.TrimSpace(in.ProviderID)
	in.Namespace = strings.TrimSpace(in.Namespace)
	in.SourceVersion = strings.TrimSpace(in.SourceVersion)
	in.ExpectedChecksum = strings.ToLower(strings.TrimSpace(in.ExpectedChecksum))
	if in.ProviderID == "" || in.Namespace == "" {
		return fmt.Errorf("%w: provider_id and namespace are required", ErrInvalidImport)
	}
	if in.SchemaVersion == 0 {
		return fmt.Errorf("%w: schema_version must be at least 1", ErrInvalidImport)
	}
	if !sourceVersionPattern.MatchString(in.SourceVersion) {
		return fmt.Errorf("%w: source_version must be 1-64 characters of [A-Za-z0-9._:-]", ErrInvalidImport)
	}
	if in.ExpectedRows == 0 {
		return fmt.Errorf("%w: expected_rows must be greater than 0", ErrInvalidImport)
	}
	if in.ExpectedChecksum != "" && len(in.ExpectedChecksum) != 64 {
		return fmt.Errorf("%w: expected_checksum must be a sha256 hex digest", ErrInvalidImport)
	}
	return nil
}

// MemberRow is one subject's membership in a snapshot, as sent by the trusted
// tokenization adapter.
type MemberRow struct {
	SubjectToken      string   `json:"subject_token"`
	SubjectKeyVersion uint16   `json:"subject_key_version"`
	SegmentIDs        []string `json:"segment_ids"`
}

// Validate checks that the row only carries a pseudonymous token and known
// segments. Raw identifiers are rejected before anything is persisted.
func (r *MemberRow) Validate() error {
	r.SubjectToken = strings.ToLower(strings.TrimSpace(r.SubjectToken))
	if !subjectTokenPattern.MatchString(r.SubjectToken) {
		return fmt.Errorf("%w: subject_token must be a 64-character lowercase hex digest of a pseudonymous identifier", ErrInvalidImport)
	}
	if r.SubjectKeyVersion == 0 {
		return fmt.Errorf("%w: subject_key_version must be at least 1", ErrInvalidImport)
	}
	if len(r.SegmentIDs) == 0 {
		return fmt.Errorf("%w: segment_ids must not be empty", ErrInvalidImport)
	}
	if len(r.SegmentIDs) > 64 {
		return fmt.Errorf("%w: at most 64 segment_ids are allowed per row", ErrInvalidImport)
	}
	seen := make(map[string]struct{}, len(r.SegmentIDs))
	for i, id := range r.SegmentIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("%w: segment_ids must not contain empty values", ErrInvalidImport)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: segment_ids must be unique per row", ErrInvalidImport)
		}
		seen[id] = struct{}{}
		r.SegmentIDs[i] = id
	}
	return nil
}

// MemberKey identifies one stored membership row.
type MemberKey struct {
	SubjectToken      string
	SubjectKeyVersion uint16
	SegmentID         string
}

// AppendResult reports what one batch changed.
type AppendResult struct {
	SubmittedRows  uint64 `json:"submitted_rows"`
	StoredRows     uint64 `json:"stored_rows"`
	IdempotentRows uint64 `json:"idempotent_rows"`
	DuplicateRows  uint64 `json:"duplicate_rows"`
}

// Checksum computes the canonical snapshot digest: one "token<TAB>keyversion<TAB>segment"
// line per membership row, sorted by those three fields, hashed with SHA-256.
func Checksum(keys []MemberKey) string {
	sorted := make([]MemberKey, len(keys))
	copy(sorted, keys)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].SubjectToken != sorted[j].SubjectToken {
			return sorted[i].SubjectToken < sorted[j].SubjectToken
		}
		if sorted[i].SubjectKeyVersion != sorted[j].SubjectKeyVersion {
			return sorted[i].SubjectKeyVersion < sorted[j].SubjectKeyVersion
		}
		return sorted[i].SegmentID < sorted[j].SegmentID
	})
	digest := sha256.New()
	for _, key := range sorted {
		digest.Write([]byte(key.SubjectToken))
		digest.Write([]byte{'\t'})
		digest.Write([]byte(strconv.FormatUint(uint64(key.SubjectKeyVersion), 10)))
		digest.Write([]byte{'\t'})
		digest.Write([]byte(key.SegmentID))
		digest.Write([]byte{'\n'})
	}
	return hex.EncodeToString(digest.Sum(nil))
}
