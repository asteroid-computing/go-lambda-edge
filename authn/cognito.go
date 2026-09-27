package authn

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// CognitoClient restricts one app client independently of its resource audience.
// Audience requires an exact string aud.
// AllowUnbound additionally permits an absent aud;
// with an empty Audience it permits only absent aud.
// At least one of Audience or AllowUnbound is required.
// No policy accepts an arbitrary aud.
type CognitoClient struct {
	ClientID     string
	Audience     string
	AllowUnbound bool
}

// CognitoConfig supplies explicit trust and resource policy for access tokens.
type CognitoConfig struct {
	// Issuer is an exact HTTPS pool URL, without trailing slash, escaped path, dot segments, credentials, query or fragment.
	// JWKS is fetched from this trusted endpoint plus /.well-known/jwks.json, never from token headers.
	Issuer string
	// Clients must contain at least one policy with a distinct nonempty ClientID.
	Clients []CognitoClient
	// ClockSkew permits 0..5 minutes of leeway.
	// Zero does not extend token life.
	ClockSkew time.Duration
	// CacheTTL defaults to 15 minutes;
	// explicit values allow 1 minute..24 hours.
	// Expired keys are not used during outages.
	// No token results are cached.
	CacheTTL time.Duration
	// Timeout defaults to 5 seconds;
	// explicit values must be positive and <=1 minute.
	Timeout time.Duration
	// Transport defaults to http.DefaultTransport.
	// A supplied transport remains caller-owned, must support concurrency and must honor context cancellation.
	Transport http.RoundTripper
	// MaxTokenBytes defaults to 16 KiB;
	// explicit values allow 1 byte..1 MiB.
	// The separate Authorization field bound also includes the scheme/framing.
	MaxTokenBytes int
	// ClaimsBudget defaults to identity's 256 KiB weighted allowance.
	// Explicit values allow 1 byte..6 MiB, independently of the compact token byte limit.
	ClaimsBudget int
}

// CognitoVerifier verifies a narrow RS256 Cognito access-token profile using JSON v2 and standard-library cryptography.
// It does not check revocation or grant permissions.
// It is safe for concurrent use, but must not be copied.
// Its zero value is unconfigured.
// Reuse one instance to share its bounded cache.
type CognitoVerifier struct {
	cfg     CognitoConfig
	clients map[string]CognitoClient
	http    *http.Client

	mu           sync.Mutex
	snapshot     jwksSnapshot
	flight       *jwksFlight
	unknownAfter time.Time
	retryAfter   time.Time
	backoff      time.Duration
	failed       bool
}

// NewCognitoVerifier validates and copies cfg without I/O.
// Invalid configuration returns a nil verifier and ErrInvalidConfiguration.
// It trusts the configured issuer endpoint;
// it does not establish that the operator supplied an AWS URL.
func NewCognitoVerifier(cfg CognitoConfig) (*CognitoVerifier, error) {
	u, err := url.Parse(cfg.Issuer)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return nil, ErrInvalidConfiguration
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(cfg.Issuer, "?#%") {
		return nil, ErrInvalidConfiguration
	}
	if u.Path == "" || u.Path == "/" || u.RawPath != "" || strings.HasSuffix(u.Path, "/") || path.Clean(u.Path) != u.Path {
		return nil, ErrInvalidConfiguration
	}
	if len(cfg.Clients) == 0 || cfg.ClockSkew < 0 || cfg.ClockSkew > 5*time.Minute || cfg.CacheTTL < 0 || cfg.Timeout < 0 || cfg.MaxTokenBytes < 0 || cfg.MaxTokenBytes > 1<<20 || cfg.ClaimsBudget < 0 || cfg.ClaimsBudget > 6<<20 {
		return nil, ErrInvalidConfiguration
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 15 * time.Minute
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.CacheTTL < time.Minute || cfg.CacheTTL > 24*time.Hour || cfg.Timeout > time.Minute {
		return nil, ErrInvalidConfiguration
	}
	if cfg.MaxTokenBytes == 0 {
		cfg.MaxTokenBytes = 16 * 1024
	}
	if cfg.ClaimsBudget == 0 {
		cfg.ClaimsBudget = 256 * 1024
	}
	if cfg.Transport == nil {
		cfg.Transport = http.DefaultTransport
	}
	value := reflect.ValueOf(cfg.Transport)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return nil, ErrInvalidConfiguration
		}
	}
	clients := make(map[string]CognitoClient, len(cfg.Clients))
	for _, client := range cfg.Clients {
		if client.ClientID == "" || client.Audience == "" && !client.AllowUnbound {
			return nil, ErrInvalidConfiguration
		}
		if _, duplicate := clients[client.ClientID]; duplicate {
			return nil, ErrInvalidConfiguration
		}
		clients[client.ClientID] = client
	}
	// Retain only the owned map, never the caller's mutable slice.
	cfg.Clients = nil
	return &CognitoVerifier{cfg: cfg, clients: clients, http: &http.Client{
		Transport: cfg.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}, nil
}

// Verify returns one locally verified caller without installing it in context.
// It accepts access tokens only, with integer exp/iat and optional nbf, and a string aud when present.
// Rejections match ErrInvalidCredentials;
// unavailable JWKS matches ErrUnavailable.
// Caller cancellation returns ctx.Err, never its cause.
// Every failure returns a zero caller and sanitized diagnostics.
func (v *CognitoVerifier) Verify(ctx context.Context, token string) (identity.Caller, error) {
	if v == nil || v.http == nil || ctx == nil {
		return identity.Caller{}, ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return identity.Caller{}, err
	}
	caller, err := v.verify(ctx, token)
	if ctx.Err() != nil {
		return identity.Caller{}, ctx.Err()
	}
	return caller, err
}

// Warm obtains a fresh usable JWKS snapshot, sharing any required fetch.
// It obeys refresh/backoff gates;
// it does not force refresh, validate a token or start a background refresh loop.
// Failures are ErrUnavailable, caller ctx.Err or ErrInvalidConfiguration for a nil context or unconfigured verifier.
func (v *CognitoVerifier) Warm(ctx context.Context) error {
	if v == nil || v.http == nil || ctx == nil {
		return ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := v.key(ctx, "")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
