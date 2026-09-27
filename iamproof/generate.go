package iamproof

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// Token owns a versioned proof.
// Its zero value is unusable.
// Default string and debug formatting are redacted;
// Value explicitly exposes the credential.
type Token struct {
	value   string
	expires time.Time
}

// Value returns the versioned credential, without the EdgeIAM HTTP scheme.
func (t Token) Value() string { return t.value }

// ExpiresAt returns the earlier of proof expiry and known credential expiry.
// This is an upper bound, not a guarantee of AWS acceptance until that time.
func (t Token) ExpiresAt() time.Time { return t.expires }

// String returns a redacted diagnostic description.
func (t Token) String() string { return "iamproof.Token(redacted)" }

// GoString returns the same redacted description as String.
func (t Token) GoString() string { return t.String() }

// Generator presigns proofs using its credentials provider.
// It is safe for concurrent use if the provider is.
// A zero Generator is not configured.
type Generator struct {
	cfg         configuration
	credentials aws.CredentialsProvider
}

// NewGenerator requires an explicit standard STS region, a case-sensitive audience of 1-256 visible ASCII bytes without spaces, and a nonnil provider.
// The provider owns credential caching, refresh and any retrieval I/O.
func NewGenerator(region, audience string, credentials aws.CredentialsProvider) (*Generator, error) {
	if isNil(credentials) {
		return nil, ErrInvalidConfiguration
	}
	cfg, err := configure(region, audience)
	if err != nil {
		return nil, err
	}
	return &Generator{cfg: cfg, credentials: credentials}, nil
}

// Generate retrieves fresh credentials and presigns locally, without calling STS.
// Failure returns a zero Token and a sanitized package or caller-context error.
// Oversized proofs fail with ErrProofTooLarge;
// credentials are not cut.
func (g *Generator) Generate(ctx context.Context) (Token, error) {
	if g == nil || g.credentials == nil || ctx == nil {
		return Token{}, ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return Token{}, err
	}
	creds, err := g.credentials.Retrieve(ctx)
	if ctx.Err() != nil {
		return Token{}, ctx.Err()
	}
	if err != nil {
		return Token{}, ErrCredentialsUnavailable
	}
	now := time.Now()
	if !validAccessKey(creds.AccessKeyID) || creds.SecretAccessKey == "" || !utf8.ValidString(creds.SessionToken) || creds.CanExpire && !now.Before(creds.Expires) {
		return Token{}, ErrCredentialsUnavailable
	}
	// A session longer than the entire decoded budget cannot fit.
	// Check before query escaping or JSON encoding can amplify a provider's oversized token.
	if len(creds.SessionToken) > base64.RawURLEncoding.DecodedLen(maxProofBytes-3) {
		return Token{}, ErrProofTooLarge
	}
	issued := now.UTC().Truncate(time.Second)
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableHeaderHoisting = true })
	signed, _, err := signer.PresignHTTP(ctx, creds, g.cfg.request(ctx), emptyHash, "sts", g.cfg.region, issued)
	if ctx.Err() != nil {
		return Token{}, ctx.Err()
	}
	if err != nil {
		return Token{}, ErrCredentialsUnavailable
	}
	u, err := url.Parse(signed)
	if err != nil {
		return Token{}, ErrCredentialsUnavailable
	}
	q := u.Query()
	if q.Get("X-Amz-SignedHeaders") != "host;x-edge-iam-audience" {
		return Token{}, ErrCredentialsUnavailable
	}
	wire, err := json.Marshal(envelope{
		Audience:     g.cfg.audience,
		Credential:   q.Get("X-Amz-Credential"),
		Date:         q.Get("X-Amz-Date"),
		Signature:    q.Get("X-Amz-Signature"),
		SessionToken: q.Get("X-Amz-Security-Token"),
	})
	if err != nil {
		return Token{}, ErrCredentialsUnavailable
	}
	if 3+base64.RawURLEncoding.EncodedLen(len(wire)) > maxProofBytes {
		return Token{}, ErrProofTooLarge
	}
	expires := issued.Add(proofLifetime)
	if creds.CanExpire && creds.Expires.Before(expires) {
		expires = creds.Expires
	}
	return Token{value: "v1." + base64.RawURLEncoding.EncodeToString(wire), expires: expires}, nil
}
