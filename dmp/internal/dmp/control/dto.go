// Package control defines the DMP management API DTOs, validation rules and
// the append-only versioned store that backs them.
package control

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// ErrInvalidDefinition reports a request that fails field or reference validation.
var ErrInvalidDefinition = errors.New("invalid definition")

// ErrNotFound reports a definition that has never been written.
var ErrNotFound = errors.New("definition not found")

// ErrVersionConflict reports that the caller's version is no longer current.
var ErrVersionConflict = errors.New("definition version does not match")

// ErrAlreadyExists reports a create for an identifier that is already defined.
var ErrAlreadyExists = errors.New("definition already exists")

// ErrImmutableField reports changes to fields that versioning cannot rewrite.
var ErrImmutableField = errors.New("definition field is immutable")

// Definition statuses. Only active definitions can be referenced by new versions.
const (
	StatusActive  = "active"
	StatusRetired = "retired"
)

// HashingAlgorithmHMACSHA256 is the only supported subject tokenization profile.
const HashingAlgorithmHMACSHA256 = "hmac-sha256"

// MaxRetentionMonths is the approved ceiling for identity mapping retention.
const MaxRetentionMonths = 3

var (
	idPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	versionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
)

// Namespace is one immutable version of an identity namespace definition.
type Namespace struct {
	ID                string    `json:"id"`
	Version           uint64    `json:"version"`
	DisplayName       string    `json:"display_name"`
	HashingAlgorithm  string    `json:"hashing_algorithm"`
	SubjectKeyVersion uint16    `json:"subject_key_version"`
	RetentionMonths   uint8     `json:"retention_months"`
	Status            string    `json:"status"`
	WrittenAt         time.Time `json:"written_at"`

	revisionID uuid.UUID
}

// NamespaceInput is the create/update payload for a namespace definition.
type NamespaceInput struct {
	ID                string `json:"id,omitempty"`
	Version           uint64 `json:"version,omitempty"`
	DisplayName       string `json:"display_name"`
	HashingAlgorithm  string `json:"hashing_algorithm"`
	SubjectKeyVersion uint16 `json:"subject_key_version"`
	RetentionMonths   uint8  `json:"retention_months"`
	Status            string `json:"status,omitempty"`
}

// Provider is one immutable version of a data provider definition.
type Provider struct {
	ID             string    `json:"id"`
	Version        uint64    `json:"version"`
	DisplayName    string    `json:"display_name"`
	Namespace      string    `json:"namespace"`
	LicenseVersion string    `json:"license_version"`
	Status         string    `json:"status"`
	WrittenAt      time.Time `json:"written_at"`

	revisionID uuid.UUID
}

// ProviderInput is the create/update payload for a provider definition.
type ProviderInput struct {
	ID             string `json:"id,omitempty"`
	Version        uint64 `json:"version,omitempty"`
	DisplayName    string `json:"display_name"`
	Namespace      string `json:"namespace"`
	LicenseVersion string `json:"license_version"`
	Status         string `json:"status,omitempty"`
}

// Segment is one immutable version of a segment definition.
type Segment struct {
	ID          string    `json:"id"`
	Version     uint64    `json:"version"`
	ProviderID  string    `json:"provider_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	ValidFrom   time.Time `json:"valid_from"`
	WrittenAt   time.Time `json:"written_at"`

	revisionID uuid.UUID
}

// SegmentInput is the create/update payload for a segment definition.
type SegmentInput struct {
	ID          string     `json:"id,omitempty"`
	Version     uint64     `json:"version,omitempty"`
	ProviderID  string     `json:"provider_id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Status      string     `json:"status,omitempty"`
	ValidFrom   *time.Time `json:"valid_from,omitempty"`
}

// Validate checks namespace fields; it does not read stored references.
func (in *NamespaceInput) Validate() error {
	in.ID = strings.TrimSpace(in.ID)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.HashingAlgorithm = strings.TrimSpace(in.HashingAlgorithm)
	if in.Status == "" {
		in.Status = StatusActive
	}
	if err := validateID("id", in.ID); err != nil {
		return err
	}
	if err := validateText("display_name", in.DisplayName, 128); err != nil {
		return err
	}
	if in.HashingAlgorithm != HashingAlgorithmHMACSHA256 {
		return fmt.Errorf("%w: hashing_algorithm must be %s", ErrInvalidDefinition, HashingAlgorithmHMACSHA256)
	}
	if in.SubjectKeyVersion == 0 {
		return fmt.Errorf("%w: subject_key_version must be at least 1", ErrInvalidDefinition)
	}
	if in.RetentionMonths == 0 || in.RetentionMonths > MaxRetentionMonths {
		return fmt.Errorf("%w: retention_months must be between 1 and %d", ErrInvalidDefinition, MaxRetentionMonths)
	}
	return validateStatus(in.Status)
}

// Validate checks provider fields; the referenced namespace is verified by the store.
func (in *ProviderInput) Validate() error {
	in.ID = strings.TrimSpace(in.ID)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.Namespace = strings.TrimSpace(in.Namespace)
	in.LicenseVersion = strings.TrimSpace(in.LicenseVersion)
	if in.Status == "" {
		in.Status = StatusActive
	}
	if err := validateID("id", in.ID); err != nil {
		return err
	}
	if err := validateText("display_name", in.DisplayName, 128); err != nil {
		return err
	}
	if err := validateID("namespace", in.Namespace); err != nil {
		return err
	}
	if !versionIDPattern.MatchString(in.LicenseVersion) {
		return fmt.Errorf("%w: license_version must be 1-64 characters of [A-Za-z0-9._:-]", ErrInvalidDefinition)
	}
	return validateStatus(in.Status)
}

// Validate checks segment fields; the referenced provider is verified by the store.
func (in *SegmentInput) Validate() error {
	in.ID = strings.TrimSpace(in.ID)
	in.ProviderID = strings.TrimSpace(in.ProviderID)
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Status == "" {
		in.Status = StatusActive
	}
	if err := validateID("id", in.ID); err != nil {
		return err
	}
	if err := validateID("provider_id", in.ProviderID); err != nil {
		return err
	}
	if err := validateText("name", in.Name, 128); err != nil {
		return err
	}
	if err := validateOptionalText("description", in.Description, 512); err != nil {
		return err
	}
	if in.ValidFrom == nil {
		now := time.Now().UTC()
		in.ValidFrom = &now
	} else if in.ValidFrom.IsZero() {
		return fmt.Errorf("%w: valid_from must not be zero", ErrInvalidDefinition)
	}
	return validateStatus(in.Status)
}

func validateID(field, value string) error {
	if !idPattern.MatchString(value) {
		return fmt.Errorf("%w: %s must be 1-64 lowercase characters of [a-z0-9._-] starting with a letter or digit", ErrInvalidDefinition, field)
	}
	return nil
}

func validateStatus(status string) error {
	switch status {
	case StatusActive, StatusRetired:
		return nil
	default:
		return fmt.Errorf("%w: status must be %s or %s", ErrInvalidDefinition, StatusActive, StatusRetired)
	}
}

func validateText(field, value string, max int) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidDefinition, field)
	}
	return validateOptionalText(field, value, max)
}

func validateOptionalText(field, value string, max int) error {
	if len(value) > max {
		return fmt.Errorf("%w: %s must be at most %d characters", ErrInvalidDefinition, field, max)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s must not contain control characters", ErrInvalidDefinition, field)
		}
	}
	return nil
}
