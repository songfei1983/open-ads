package control_test

import (
	"errors"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/control"
)

func TestNamespaceInputValidateDefaults(t *testing.T) {
	input := control.NamespaceInput{
		ID: "publisher-account-v1", DisplayName: " Publisher account ",
		HashingAlgorithm: " hmac-sha256 ", SubjectKeyVersion: 1, RetentionMonths: 3,
	}
	if err := input.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if input.Status != control.StatusActive || input.DisplayName != "Publisher account" {
		t.Fatalf("defaults were not normalised: %+v", input)
	}
}

func TestNamespaceInputValidateRejects(t *testing.T) {
	base := func() control.NamespaceInput {
		return control.NamespaceInput{
			ID: "ns-1", DisplayName: "Namespace", HashingAlgorithm: control.HashingAlgorithmHMACSHA256,
			SubjectKeyVersion: 1, RetentionMonths: 3,
		}
	}
	cases := map[string]func(*control.NamespaceInput){
		"empty id":            func(in *control.NamespaceInput) { in.ID = "" },
		"dash first":          func(in *control.NamespaceInput) { in.ID = "-ns" },
		"too long id":         func(in *control.NamespaceInput) { in.ID = string(make([]byte, 65)) },
		"empty display name":  func(in *control.NamespaceInput) { in.DisplayName = "  " },
		"control char":        func(in *control.NamespaceInput) { in.DisplayName = "bad\x00name" },
		"wrong algorithm":     func(in *control.NamespaceInput) { in.HashingAlgorithm = "sha256" },
		"zero key version":    func(in *control.NamespaceInput) { in.SubjectKeyVersion = 0 },
		"zero retention":      func(in *control.NamespaceInput) { in.RetentionMonths = 0 },
		"retention above max": func(in *control.NamespaceInput) { in.RetentionMonths = control.MaxRetentionMonths + 1 },
		"unknown status":      func(in *control.NamespaceInput) { in.Status = "draft" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			input := base()
			mutate(&input)
			if err := input.Validate(); !errors.Is(err, control.ErrInvalidDefinition) {
				t.Fatalf("expected ErrInvalidDefinition, got %v", err)
			}
		})
	}
}

func TestSegmentInputValidateDefaultsValidFrom(t *testing.T) {
	input := control.SegmentInput{ID: "seg-1", ProviderID: "provider-1", Name: "Gamers"}
	if err := input.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if input.ValidFrom == nil || input.ValidFrom.IsZero() {
		t.Fatalf("valid_from was not defaulted: %+v", input)
	}

	zero := time.Time{}
	input = control.SegmentInput{ID: "seg-1", ProviderID: "provider-1", Name: "Gamers", ValidFrom: &zero}
	if err := input.Validate(); !errors.Is(err, control.ErrInvalidDefinition) {
		t.Fatalf("zero valid_from must be rejected, got %v", err)
	}
}

func TestProviderInputValidateRejectsBadLicense(t *testing.T) {
	input := control.ProviderInput{
		ID: "provider-1", DisplayName: "Provider", Namespace: "ns-1", LicenseVersion: "license with spaces",
	}
	if err := input.Validate(); !errors.Is(err, control.ErrInvalidDefinition) {
		t.Fatalf("expected ErrInvalidDefinition, got %v", err)
	}
}
