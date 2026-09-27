package iamproof

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// Verifier authenticates each proof through its configured regional STS endpoint.
// It is safe for concurrent use if its transport is.
// Its zero value is not configured.
// It never obtains or uses server-side AWS credentials.
type Verifier struct {
	cfg     configuration
	client  *http.Client
	timeout time.Duration
}

// VerifierOption configures a verifier.
// Options apply in order;
// the last value for a setting wins.
// Nil options and invalid values fail construction.
type VerifierOption func(*verifierConfig) error

type verifierConfig struct {
	transport http.RoundTripper
	timeout   time.Duration
}

// WithTransport sets trusted deployment infrastructure for STS requests.
// The transport must be nonnil, concurrency-safe and honor request cancellation.
// Internal transport retries and proxy/TLS policy belong to the transport owner.
func WithTransport(transport http.RoundTripper) VerifierOption {
	return func(cfg *verifierConfig) error {
		if isNil(transport) {
			return ErrInvalidConfiguration
		}
		cfg.transport = transport
		return nil
	}
}

// WithTimeout sets a positive verification network deadline, defaulting to five seconds.
// The caller's earlier context deadline still wins.
func WithTimeout(timeout time.Duration) VerifierOption {
	return func(cfg *verifierConfig) error {
		if timeout <= 0 {
			return ErrInvalidConfiguration
		}
		cfg.timeout = timeout
		return nil
	}
}

// NewVerifier requires the same region and audience as the client generator.
// Its owned HTTP client disables redirects and cookies.
// The default transport is http.DefaultTransport as configured when this constructor is called.
func NewVerifier(region, audience string, opts ...VerifierOption) (*Verifier, error) {
	cfg, err := configure(region, audience)
	if err != nil {
		return nil, err
	}
	network := verifierConfig{transport: http.DefaultTransport, timeout: 5 * time.Second}
	for _, opt := range opts {
		if opt == nil {
			return nil, ErrInvalidConfiguration
		}
		if err := opt(&network); err != nil {
			return nil, err
		}
	}
	if isNil(network.transport) {
		return nil, ErrInvalidConfiguration
	}
	return &Verifier{
		cfg:     cfg,
		timeout: network.timeout,
		client: &http.Client{Transport: network.transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

// Verify accepts [Token.Value], without an HTTP authentication scheme.
// It returns an IAM caller with SourceVerifiedIAMProof only after STS confirms the proof, response invariants hold and the proof remains fresh.
// It does not install the caller in a context.
// Every failure returns an anonymous zero caller.
//
// Malformed or rejected credentials match [ErrInvalidProof].
// Dependency failures, internal timeouts and unexpected responses match [ErrUnavailable].
// Caller cancellation/deadline errors are preserved, without unsafe dependency causes.
func (v *Verifier) Verify(ctx context.Context, token string) (identity.Caller, error) {
	if v == nil || v.client == nil || ctx == nil {
		return identity.Caller{}, ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return identity.Caller{}, err
	}
	p, issued, err := decodeProof(token, v.cfg)
	if err != nil || !fresh(issued, time.Now()) {
		return identity.Caller{}, ErrInvalidProof
	}
	network, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	resp, err := v.client.Do(p.request(network, v.cfg))
	if err != nil {
		return identity.Caller{}, verificationFailure(ctx)
	}
	defer resp.Body.Close()
	wire, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if ctx.Err() != nil {
		return identity.Caller{}, ctx.Err()
	}
	if err != nil || network.Err() != nil || len(wire) > maxResponseBytes {
		return identity.Caller{}, ErrUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			fields, err := parseResponse(wire, false)
			if err == nil && invalidCredentialCode(fields["Code"]) {
				return identity.Caller{}, ErrInvalidProof
			}
		}
		return identity.Caller{}, ErrUnavailable
	}
	fields, err := parseResponse(wire, true)
	if err != nil {
		return identity.Caller{}, ErrUnavailable
	}
	caller, err := identity.NewIAM(fields["Arn"], identity.SourceVerifiedIAMProof, identity.WithIAMAccountID(fields["Account"]), identity.WithIAMPrincipalID(fields["UserId"]))
	if err != nil {
		return identity.Caller{}, ErrUnavailable
	}
	iam, ok := caller.IAM()
	if !ok || iam.Partition() != v.cfg.partition {
		return identity.Caller{}, ErrUnavailable
	}
	if ctx.Err() != nil {
		return identity.Caller{}, ctx.Err()
	}
	if network.Err() != nil {
		return identity.Caller{}, ErrUnavailable
	}
	if !fresh(issued, time.Now()) {
		return identity.Caller{}, ErrInvalidProof
	}
	return caller, nil
}

func verificationFailure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrUnavailable
}

func invalidCredentialCode(code string) bool {
	switch code {
	case "ExpiredToken", "ExpiredTokenException", "IncompleteSignature", "MissingAuthenticationToken", "RequestExpired",
		"UnrecognizedClientException", "InvalidClientTokenId", "SignatureDoesNotMatch":
		return true
	}
	return false
}
