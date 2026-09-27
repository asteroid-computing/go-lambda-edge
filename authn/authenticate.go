package authn

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// VerifyFunc authenticates a credential without its HTTP scheme.
// It must return ErrInvalidCredentials for a definite rejection;
// unknown errors are treated as unavailable.
// Success requires JWT/SourceLocallyVerifiedToken for Bearer or IAM/SourceVerifiedIAMProof for EdgeIAM.
// Constructors alone do not verify tokens.
type VerifyFunc func(context.Context, string) (identity.Caller, error)

// Config supplies explicit verifier dependencies and HTTP framing policy.
type Config struct {
	// Bearer and IAMProof are independently optional;
	// at least one is required.
	// Each function must be safe for concurrent use when sharing an Authenticator.
	Bearer   VerifyFunc
	IAMProof VerifyFunc
	// Realm is a challenge label, not a proof audience or issuer restriction.
	// Empty defaults to "edge";
	// explicit values allow 1-256 printable ASCII bytes.
	Realm string
	// MaxAuthorizationBytes bounds represented Authorization value bytes before trimming.
	// Zero defaults to 16 KiB;
	// explicit values must be in 1..1 MiB.
	// This neither changes iamproof's 8 KiB proof limit nor bounds all headers.
	MaxAuthorizationBytes int
}

// Authenticator is an immutable selector configured by New.
// Its zero value is unconfigured.
// Shared use requires concurrency-safe verifier functions.
type Authenticator struct {
	cfg             Config
	bearerChallenge string
	iamChallenge    string
}

// New validates and copies cfg without I/O or invoking a verifier.
// It returns a nil Authenticator and ErrInvalidConfiguration for invalid configuration.
func New(cfg Config) (*Authenticator, error) {
	if cfg.Bearer == nil && cfg.IAMProof == nil || cfg.MaxAuthorizationBytes < 0 || cfg.MaxAuthorizationBytes > 1<<20 {
		return nil, ErrInvalidConfiguration
	}
	if cfg.MaxAuthorizationBytes == 0 {
		cfg.MaxAuthorizationBytes = 16 * 1024
	}
	if cfg.Realm == "" {
		cfg.Realm = "edge"
	}
	if len(cfg.Realm) > 256 {
		return nil, ErrInvalidConfiguration
	}
	for i := range len(cfg.Realm) {
		if cfg.Realm[i] < 0x20 || cfg.Realm[i] > 0x7e {
			return nil, ErrInvalidConfiguration
		}
	}
	quoted := `"` + strings.ReplaceAll(strings.ReplaceAll(cfg.Realm, `\`, `\\`), `"`, `\"`) + `"`
	a := &Authenticator{cfg: cfg}
	if cfg.Bearer != nil {
		a.bearerChallenge = "Bearer realm=" + quoted
	}
	if cfg.IAMProof != nil {
		a.iamChallenge = "EdgeIAM realm=" + quoted
	}
	return a, nil
}

// Authenticate selects at most one verifier and returns a verified caller.
// It neither modifies headers nor installs context.
// Callers must not mutate headers concurrently.
// Every failure returns an anonymous zero caller.
//
// An established caller fails with identity.ErrConflict before credential work.
// Runtime failures are *Error with sanitized categories;
// caller cancellation returns ctx.Err directly.
// Nil contexts and nil/zero Authenticators return ErrInvalidConfiguration.
// No provider diagnostic or context cause is exposed.
func (a *Authenticator) Authenticate(ctx context.Context, headers http.Header) (identity.Caller, error) {
	if a == nil || a.cfg.MaxAuthorizationBytes == 0 || ctx == nil {
		return identity.Caller{}, ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return identity.Caller{}, err
	}
	if identity.FromContext(ctx).Kind() != identity.KindAnonymous {
		return identity.Caller{}, a.failure(identity.ErrConflict, http.StatusInternalServerError, noScheme)
	}
	selected, token, err := a.selectCredential(headers)
	if err != nil {
		return identity.Caller{}, err
	}
	verify := a.cfg.Bearer
	if selected == iamProof {
		verify = a.cfg.IAMProof
	}
	if err := ctx.Err(); err != nil {
		return identity.Caller{}, err
	}
	caller, err := verify(ctx, token)
	if ctx.Err() != nil {
		return identity.Caller{}, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrInvalidCredentials) {
			return identity.Caller{}, a.failure(ErrUnavailable, http.StatusServiceUnavailable, noScheme)
		}
		return identity.Caller{}, a.failure(ErrInvalidCredentials, http.StatusUnauthorized, selected)
	}
	valid := caller.Kind() == identity.KindJWT && caller.Source() == identity.SourceLocallyVerifiedToken
	if selected == iamProof {
		valid = caller.Kind() == identity.KindIAM && caller.Source() == identity.SourceVerifiedIAMProof
	}
	if !valid {
		return identity.Caller{}, a.failure(ErrVerifierContract, http.StatusInternalServerError, noScheme)
	}
	return caller, nil
}
