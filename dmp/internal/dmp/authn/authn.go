// Package authn identifies data-plane callers and binds each one to exactly one
// tenant. The sandbox profile authenticates with bearer tokens; D-01 replaces
// them with mTLS workload identity, so both paths share the same Principal.
package authn

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var (
	// ErrUnauthenticated means no usable credential was presented.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrForbidden means the caller is known but lacks the required scope or
	// tenant.
	ErrForbidden = errors.New("forbidden")
)

// Scope is one capability granted to a caller.
type Scope string

const (
	// ScopeSnapshotsRead reads snapshots, publications, revocations and policy.
	ScopeSnapshotsRead Scope = "snapshots:read"
	// ScopeSnapshotsWrite imports, verifies, publishes and deletes snapshots.
	ScopeSnapshotsWrite Scope = "snapshots:write"
	// ScopeIdentityRevoke records revocation requests.
	ScopeIdentityRevoke Scope = "identity:revoke"
	// ScopeRetentionRun triggers retention sweeps.
	ScopeRetentionRun Scope = "retention:run"
)

// Principal is an authenticated caller. Tenant is the only tenant it may touch.
type Principal struct {
	Caller string
	Tenant string
	Scopes []Scope
}

// Allows reports whether the principal holds scope.
func (p Principal) Allows(scope Scope) bool {
	for _, granted := range p.Scopes {
		if granted == scope {
			return true
		}
	}
	return false
}

// Authorize returns ErrForbidden unless the principal holds scope.
func (p Principal) Authorize(scope Scope) error {
	if p.Allows(scope) {
		return nil
	}
	return fmt.Errorf("%w: caller %q requires scope %s", ErrForbidden, p.Caller, scope)
}

// Authenticator resolves a request to a Principal.
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, error)
}

type contextKey struct{}

// WithPrincipal stores the authenticated principal on the request context.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, principal)
}

// PrincipalFromContext returns the principal installed by WithPrincipal.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(contextKey{}).(Principal)
	return principal, ok
}

// Credential is one bearer token and exactly what it may do.
type Credential struct {
	Token  string
	Caller string
	Tenant string
	Scopes []Scope
}

type hashedCredential struct {
	digest    [sha256.Size]byte
	principal Principal
}

// BearerAuth authenticates a fixed credential set in constant time.
type BearerAuth struct {
	credentials []hashedCredential
}

// NewBearerAuth returns a bearer authenticator. Credentials without a token are
// ignored so an unset environment variable can never authenticate.
func NewBearerAuth(credentials ...Credential) *BearerAuth {
	auth := &BearerAuth{}
	for _, credential := range credentials {
		if credential.Token == "" {
			continue
		}
		auth.credentials = append(auth.credentials, hashedCredential{
			digest: sha256.Sum256([]byte(credential.Token)),
			principal: Principal{
				Caller: credential.Caller,
				Tenant: credential.Tenant,
				Scopes: credential.Scopes,
			},
		})
	}
	return auth
}

// Authenticate matches the bearer token against every credential without
// short-circuiting, so timing does not reveal which token was closest.
func (a *BearerAuth) Authenticate(r *http.Request) (Principal, error) {
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		return Principal{}, fmt.Errorf("%w: bearer token required", ErrUnauthenticated)
	}
	presented := sha256.Sum256([]byte(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))))
	var (
		matched  Principal
		verified bool
	)
	for _, credential := range a.credentials {
		if subtle.ConstantTimeCompare(presented[:], credential.digest[:]) == 1 {
			matched = credential.principal
			verified = true
		}
	}
	if !verified {
		return Principal{}, fmt.Errorf("%w: unknown bearer token", ErrUnauthenticated)
	}
	return matched, nil
}

// Identity is a workload identity granted to one client certificate subject.
type Identity struct {
	Caller string
	Tenant string
	Scopes []Scope
}

// MTLS authenticates the peer certificate of an already verified TLS
// connection. D-01 wires it in place of BearerAuth; it lives here so callers can
// switch authenticators without touching route authorization.
type MTLS struct {
	// Identities maps a certificate common name to its identity.
	Identities map[string]Identity
}

// Authenticate reads the verified peer chain from the request TLS state.
func (m MTLS) Authenticate(r *http.Request) (Principal, error) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
		return Principal{}, fmt.Errorf("%w: client certificate required", ErrUnauthenticated)
	}
	cert := r.TLS.VerifiedChains[0][0]
	commonName, err := commonNameOf(cert)
	if err != nil {
		return Principal{}, err
	}
	identity, ok := m.Identities[commonName]
	if !ok {
		return Principal{}, fmt.Errorf("%w: certificate subject %q is not provisioned", ErrUnauthenticated, commonName)
	}
	return Principal{Caller: identity.Caller, Tenant: identity.Tenant, Scopes: identity.Scopes}, nil
}

func commonNameOf(cert *x509.Certificate) (string, error) {
	if cert.Subject.CommonName == "" {
		return "", fmt.Errorf("%w: client certificate has no common name", ErrUnauthenticated)
	}
	return cert.Subject.CommonName, nil
}
