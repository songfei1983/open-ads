package domain_test

import (
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/domain"
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
