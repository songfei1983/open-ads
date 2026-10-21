package domain_test

import (
	"testing"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/domain"
)

func TestNewUUIDv7(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)

	id, err := domain.NewUUIDv7()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := domain.ValidateUUID32(id); err != nil {
		t.Fatalf("generated id failed validation: %v", err)
	}

	ts, err := domain.TimestampFromUUIDv7(id)
	if err != nil {
		t.Fatalf("failed to extract timestamp: %v", err)
	}

	after := time.Now().UTC().Add(time.Second)
	if ts.Before(before) || ts.After(after) {
		t.Errorf("timestamp %v outside expected window [%v, %v]", ts, before, after)
	}
}

func TestValidateUUID32_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"too short", "1234567890"},
		{"too long", "0123456789abcdef0123456789abcdef0"},
		{"invalid char", "0123456789abcdef0123456789abcdeg"},
		{"uppercase", "0123456789ABCDEF0123456789ABCDEF"},
		{"hyphenated", "018f7a3b-2c9d-7e1f-a3b4-c5d6e7f89010"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := domain.ValidateUUID32(tc.in); err == nil {
				t.Errorf("expected error for input %q, got nil", tc.in)
			}
		})
	}
}
