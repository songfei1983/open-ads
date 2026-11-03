package authn_test

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/authn"
)

func TestBearerAuthBindsCallerAndTenant(t *testing.T) {
	auth := authn.NewBearerAuth(
		authn.Credential{Token: "token-adapter", Caller: "tokenizer", Tenant: "tenant-a", Scopes: []authn.Scope{authn.ScopeSnapshotsWrite}},
		authn.Credential{Token: "", Caller: "unset", Tenant: "tenant-a", Scopes: []authn.Scope{authn.ScopeSnapshotsWrite}},
	)

	request := httptest.NewRequest("GET", "/internal/v1/snapshots", nil)
	request.Header.Set("Authorization", "Bearer token-adapter")
	principal, err := auth.Authenticate(request)
	if err != nil {
		t.Fatalf("known token must authenticate: %v", err)
	}
	if principal.Caller != "tokenizer" || principal.Tenant != "tenant-a" {
		t.Fatalf("principal must carry caller and tenant: %+v", principal)
	}
	if !principal.Allows(authn.ScopeSnapshotsWrite) {
		t.Fatalf("granted scope missing: %+v", principal)
	}
	if err := principal.Authorize(authn.ScopeRetentionRun); !errors.Is(err, authn.ErrForbidden) {
		t.Fatalf("missing scope must be forbidden, got %v", err)
	}

	// An empty environment variable must never become a working credential.
	empty := httptest.NewRequest("GET", "/internal/v1/snapshots", nil)
	empty.Header.Set("Authorization", "Bearer ")
	if _, err := auth.Authenticate(empty); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("empty token must not authenticate, got %v", err)
	}

	unknown := httptest.NewRequest("GET", "/internal/v1/snapshots", nil)
	unknown.Header.Set("Authorization", "Bearer token-other")
	if _, err := auth.Authenticate(unknown); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("unknown token must not authenticate, got %v", err)
	}

	if _, err := auth.Authenticate(httptest.NewRequest("GET", "/internal/v1/snapshots", nil)); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("missing header must not authenticate, got %v", err)
	}
}

func TestMTLSMapsCertificateSubjectToIdentity(t *testing.T) {
	auth := authn.MTLS{Identities: map[string]authn.Identity{
		"dmp-resolver": {Caller: "ssp", Tenant: "tenant-a", Scopes: []authn.Scope{authn.ScopeSnapshotsRead}},
	}}

	plain := httptest.NewRequest("GET", "/internal/v1/snapshots", nil)
	if _, err := auth.Authenticate(plain); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("a request without a client certificate must not authenticate, got %v", err)
	}

	request := httptest.NewRequest("GET", "/internal/v1/snapshots", nil)
	request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{
		{Subject: pkix.Name{CommonName: "dmp-resolver"}},
	}}}
	principal, err := auth.Authenticate(request)
	if err != nil {
		t.Fatalf("provisioned subject must authenticate: %v", err)
	}
	if principal.Caller != "ssp" || principal.Tenant != "tenant-a" {
		t.Fatalf("unexpected principal: %+v", principal)
	}

	unprovisioned := httptest.NewRequest("GET", "/internal/v1/snapshots", nil)
	unprovisioned.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{
		{Subject: pkix.Name{CommonName: "unknown-workload"}},
	}}}
	if _, err := auth.Authenticate(unprovisioned); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("an unprovisioned subject must not authenticate, got %v", err)
	}
}
