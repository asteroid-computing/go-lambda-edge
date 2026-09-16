# HTTP authentication

`authn` is an implemented, SDK-independent HTTP authentication selector. It
chooses exactly one configured Bearer or EdgeIAM verifier, checks the returned
caller and, when used as middleware, installs it in the request context. It works
with ordinary Go HTTP servers and the root edge adapter's raw/typed entry points.

`authn.NewCognitoVerifier` implements local RS256 Cognito access-token verification
with direct JSON v2 and bounded JWKS caching. Supply its Verify method, another
fully verifying Bearer function, IAM proofs alone, or both mechanisms. See the
[Cognito guide](cognito.md) for issuer/client/audience restrictions and cache
ownership. The authz package remains future work; applications must authorize
actions. Default gateway identity remains disabled.

## Configure both mechanisms

```go
proofVerifier, err := iamproof.NewVerifier("eu-west-2", "orders.production")
if err != nil {
    return err
}
verifyIAM := func(ctx context.Context, token string) (identity.Caller, error) {
    caller, err := proofVerifier.Verify(ctx, token)
    if errors.Is(err, iamproof.ErrInvalidProof) {
        return identity.Caller{}, authn.ErrInvalidCredentials
    }
    return caller, err
}
authenticator, err := authn.New(authn.Config{
    Bearer:   verifyBearer, // For Cognito: cognitoVerifier.Verify.
    IAMProof: verifyIAM,
    Realm:    "orders",
})
if err != nil {
    return err
}
protected, err := authenticator.Handler(dispatcher)
if err != nil {
    return err
}
adapter, err := edge.New(protected)
if err != nil {
    return err
}
lambda.Start(adapter)
```

The IAM error adapter above is required: passing `proofVerifier.Verify` directly
leaves its package-specific rejection unclassified and results in 503 instead
of 401. This explicit mapping lets JWT-only applications use authn without
importing the AWS SDK. No dependency versions changed for this package.

`verifyBearer` has signature
`func(context.Context, string) (identity.Caller, error)`. It receives the exact
credential without the scheme. It must validate the intended issuer, signature,
time and client/audience policy before constructing a JWT caller with
SourceLocallyVerifiedToken. Parsing a JWT or calling identity.NewJWT is not
verification. Return authn.ErrInvalidCredentials only for a definite credential
rejection; authn.ErrUnavailable or unclassified errors become 503. No raw provider
diagnostics are retained in the selector's returned error.

The IAM function must return KindIAM/SourceVerifiedIAMProof. Bearer must return
KindJWT/SourceLocallyVerifiedToken. Anonymous, gateway/custom, or wrong-kind
success is a verifier contract failure, not client rejection. All functions must
be concurrency-safe when their Authenticator is shared.

Omitting either function disables that scheme. Both omitted is a configuration
error. New copies configuration and performs no I/O. Realm defaults to `edge`
and is only a challenge label; it does not configure proof audience or JWT trust.
Explicit realms allow up to 256 printable ASCII bytes and are escaped safely.

## Request selection

Only Authorization establishes credentials. Cookies, query parameters, bodies
and alternate headers are not examined as authentication sources. The parser
checks all case-equivalent Authorization map keys, rejects repeated values even
if identical, and rejects comma-combined credentials. It does not try a second
verifier after any failure.

Schemes are case-insensitive. Outer spaces/tabs are trimmed; the separator must
be one or more spaces. Token bytes and case are preserved. Configured schemes
require token68-shaped header credentials. Bearer contents are not decoded to
select a verifier. The IAM verifier applies its stricter proof format afterward.

MaxAuthorizationBytes defaults to 16 KiB, counting represented field-value bytes
before trimming. An explicit value must be 1 byte through 1 MiB; zero requests the
default. This is separate from the IAM proof's fixed 8 KiB credential limit and
from gateway/server limits on the whole request. Earlier proxies can discard
duplicate information; no middleware can reconstruct it afterward.

An already established caller produces identity.ErrConflict before verifier
work. Do not place this local authentication middleware behind an enabled gateway
producer for the same route. There is no implicit skip, merge or replacement.

## Errors and HTTP customization

Authenticate(ctx, headers) returns one caller without installing it or writing a
response. Failures return an anonymous zero caller. Inspect categories with
errors.Is and HTTP details with errors.AsType[*authn.Error]. Challenges returns
an owned copy. Caller cancellation/deadline returns ctx.Err directly, without
exposing context.Cause.

| Category | Default HTTP response |
| --- | --- |
| ErrMissingCredentials / ErrUnsupportedScheme | 401; configured challenges |
| ErrMalformedCredentials | 400; configured challenges, Bearer invalid_request when unambiguously selected |
| ErrHeaderTooLarge | 431; no challenge |
| ErrInvalidCredentials | 401; selected scheme, Bearer invalid_token where applicable |
| ErrUnavailable or unknown verifier error | 503; no challenge |
| ErrVerifierContract / identity.ErrConflict | 500; no challenge |
| Caller cancellation/deadline | Best-effort 503; no challenge |

An Error chain contains only its sanitized category. A nonnil verifier error
discards any returned caller. Unavailable takes precedence over invalid
credentials when an error matches both. An internal timeout with a live request
context is unavailable, not caller cancellation.

Handler returns fixed generic text and Cache-Control: no-store on failure,
replaces WWW-Authenticate and preserves unrelated response headers such as CORS.
It does not log, add Retry-After or include detailed error descriptions. Request
cancellation prevents the next handler from running; delivery of the 503 is not
guaranteed. Edge still rejects a usable response for a canceled invocation.

For custom JSON responses, use Authenticate and the Error accessors in ordinary
HTTP middleware. Serialize application envelopes directly with encoding/json/v2.
On success, install the returned caller with identity.WithCaller before dispatch.
See the [runnable examples](../authn/example_test.go) for error mapping, a custom
JSON failure response and a shared action dispatcher. Their synthetic Bearer
fixture is explicitly not a production token verifier.

## Action dispatch and deployment

Select an action once using edge.ActionHeader or your own header processor.
Authorize that caller for that exact selection, then execute it. Both IAM and
JWT callers can reach the same dispatcher. Successful IAM authentication does
not permit every AWS identity to execute your actions; deny by default.

Complete authentication and authorization before writing or flushing a response.
The caller is a snapshot and is not automatically refreshed during a stream.
Application-specific ongoing authorization remains explicit.

Protect only intended routes. Public routes omit Handler. There is no automatic
OPTIONS bypass: configure CORS/preflight at API Gateway or in outer middleware.
Deploy over HTTPS; r.TLS need not be populated behind API Gateway, and forwarded
scheme headers do not themselves establish trust.

Local tests cover native in-memory HTTP, raw REST/HTTP API payloads and typed
V1/V2 methods, including challenge combination and actual IAM helper/verifier
composition against synthetic STS responses, plus actual RS256 verification
against local JWKS fixtures. No live AWS service or deployment
was used. [Decision 0019](decisions/0019-http-authentication-selector.md) records
the accepted contract and its official documentation sources.
