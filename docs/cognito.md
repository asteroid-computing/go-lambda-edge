# Cognito access-token verification

`authn.NewCognitoVerifier` provides local RS256 verification using Go 1.27's `encoding/json/v2`/`jsontext` and standard-library RSA.
It returns an immutable JWT caller with `SourceLocallyVerifiedToken`.
It performs no AWS account API calls and adds no SDK or JWT-library dependency.

## Configure explicit issuer, clients and resource binding

```go
verifier, err := authn.NewCognitoVerifier(authn.CognitoConfig{
    Issuer: "https://issuer-cognito-idp.eu-west-2.amazonaws.com/eu-west-2_example",
    Clients: []authn.CognitoClient{
        {ClientID: "user-app-client", Audience: "https://orders.example"},
        {ClientID: "machine-app-client", AllowUnbound: true},
    },
})
if err != nil {
    return err
}
authenticator, err := authn.New(authn.Config{Bearer: verifier.Verify})
if err != nil {
    return err
}
protected, err := authenticator.Handler(dispatcher)
if err != nil {
    return err
}
```

Use `protected` with an ordinary HTTP server or `edge.New`.
Include IAMProof with the explicit error adapter in [the HTTP authentication guide](authn.md) to accept IAM and Cognito credentials on the same route.
Authentication must precede action authorization and any response/stream commitment.
Gateway identity stays disabled for these routes;
there is no merge or fallback between producers.

Issuer is the exact configured Cognito pool issuer URL.
Both original `cognito-idp` and updated `issuer-cognito-idp` formats work.
Supply an HTTPS URL with a pool path and no trailing slash, escaped path, dot segments, credentials, query or fragment.
The library appends `/.well-known/jwks.json` to this trusted configuration.
It never uses a token's issuer or JOSE URLs to discover a server.
The constructor does not prove that your configured URL belongs to AWS.

Every token must be an access token from an explicitly listed client.
ID tokens are rejected.
`client_id` identifies the app client;
`aud` identifies the bound resource.
They are never interchangeable:

| Policy | Absent aud | Matching string aud | Other aud |
| --- | --- | --- | --- |
| Audience set | Reject | Accept | Reject |
| Audience set, AllowUnbound true | Accept | Accept | Reject |
| Audience empty, AllowUnbound true | Accept | Reject | Reject |

Audience empty with AllowUnbound false is invalid configuration.
Null, empty or array audiences fail verification.
Missing sub is allowed when client_id is valid;
sub is not assumed to be a UUID or evidence of a particular grant flow.

Cognito resource binding is unavailable for client-credentials M2M grants and SDK authentication models.
AllowUnbound makes that exception explicit per client.
Prefer dedicated app clients and resource-specific scopes for APIs accepting unbound tokens: other APIs trusting that same issuer/client could accept them.
Scopes and Cognito groups remain separate identity facts;
your dispatcher must authorize the exact selected action.
A valid token alone grants nothing.

## Lifecycle and key cache

Construct once and reuse the verifier across concurrent requests/invocations.
Construction performs no I/O.
`Warm(ctx)` optionally fetches keys before serving;
otherwise Verify fetches lazily.
Warm is not a forced refresh or a token check.
It returns immediately when a usable snapshot is fresh.

The cache stores only keys, never tokens or verification results.
A successful fetch atomically replaces the complete snapshot, including removal of omitted keys.
Known fresh keys verify locally even while another refresh is failing.
Expired keys are never used during an outage.

- CacheTTL defaults to 15 minutes, configurable from 1 minute through 24 hours.
  It starts at successful fetch completion.
  No autonomous refresh loop runs.
- An unknown kid can trigger a fetch, with a 30-second cooldown from the last success or unknown-key attempt.
  Random kid values do not grow a negative cache.
- Failed fetches back off for 5, 10, 20, 40, then 60 seconds, capped at 60.
  This includes cold-cache failures.
  Where both gates apply, the later wins.
- Concurrent callers share at most one fetch.
  Each call fetches/joins at most once.
  A known-key signature failure never triggers an additional fetch.

A newly rotated key can be rejected during a successful-fetch cooldown.
A same-kid replacement may wait until TTL expiry.
These limits are per verifier, not fleet-wide AWS rate controls.
Shorter TTL reduces the window in which a removed key remains locally trusted and increases fetch frequency.

Cancellation stops an individual caller's wait without canceling other waiters.
The shared fetch uses its own bounded context and no request context values.
It may finish and populate the cache after all waiters leave;
Lambda can freeze that work, so it is not a durable background job.
There is no Close method.

Timeout defaults to 5 seconds and accepts a positive duration up to 1 minute.
It covers headers, body and key validation.
Redirects, cookie storage and application retries are disabled;
only HTTP 200 is accepted.
Cache lifetime is explicit key-trust policy, not an HTTP Cache-Control/ETag cache.

Transport defaults to http.DefaultTransport with normal TLS verification.
A custom RoundTripper is a trusted, caller-owned dependency: it must support concurrency and honor context cancellation, including body reads.
The library cannot kill arbitrary blocked Go code.
It bounds waiter duration and retains a still-running fetch to prevent accumulating replacement goroutines;
late success after its deadline cannot populate the cache.
It does not mutate or close the supplied transport.

## Token profile, limits and failures

Only compact RS256 tokens are supported.
JSON is strict about duplicate members, UTF-8, trailing data and a maximum depth of 64 containers.
Base64url must be canonical and unpadded.
Optional typ must be JWT;
critical headers, unencoded payloads, token-supplied keys/URLs/certificate chains, compression and nested content declarations are rejected.
kid is an opaque identifier, never a URL.

exp and iat are required plain nonnegative integer JSON seconds;
nbf is optional.
Values must fit through year 9999.
Fractions, exponents and string dates are rejected.
Require exp > iat and nbf < exp when present, and check expiration, future iat and nbf both before and after expensive work.
ClockSkew defaults to zero and permits explicit leeway up to five minutes.
These profile restrictions are narrower than general JWT, not additional claims about AWS service limits.

MaxTokenBytes defaults to 16 KiB and accepts 1 byte..1 MiB.
ClaimsBudget defaults to the identity package's weighted 256 KiB allowance, configurable up to 6 MiB.
The separate authn MaxAuthorizationBytes includes the scheme and spaces: raise both where appropriate.
Gateway/server total header limits still apply.

Fixed bounds are a 4 KiB decoded protected header, a 256-byte kid, a 64 KiB JWKS response after transport decoding, at most 32 keys, and 2048..4096-bit RSA keys.
Duplicate nonempty kids are rejected even across unsupported key types.
Malformed eligible signing keys invalidate the entire fetched set;
previously cached keys remain usable only until their original expiry.
No indefinite union of removed keys or partial publication of malformed sets occurs.

| Verify outcome | Category | Existing middleware response |
| --- | --- | --- |
| Invalid syntax, profile, claims, signature or definitively absent key | ErrInvalidCredentials | 401, Bearer invalid_token |
| Missing key during successful-fetch cooldown | ErrInvalidCredentials | 401; rotation can be briefly delayed |
| Network/JWKS failure, expired cache outage, or failed required refresh/backoff | ErrUnavailable | 503, no challenge |
| Caller canceled or deadline exceeded | ctx.Err() | Best-effort 503; no application handler |

Each failure returns a zero caller.
Diagnostics contain no raw token, claims, kid, response body, transport cause or cancellation cause.
Direct Verify does not install context or write a response.
Constructors and nil/zero-verifier misuse return ErrInvalidConfiguration.
Warm reports key unavailability or cancellation, not invalid credentials.

Offline signature/claim verification cannot establish current Cognito revocation, sign-out or user-enabled state.
No GetUser/introspection call is implied.
The resulting caller is a snapshot, including during a long stream;
ongoing policy checks belong to the application.

Local validation includes an independent RFC RS256 vector, signed synthetic tokens, JWKS rotation/outage/concurrency tests, and actual Bearer/IAM composition over native HTTP and raw/typed API Gateway entry points.
No live Cognito or AWS deployment was used.
[Decision 0020](decisions/0020-cognito-verification.md) records the approved tradeoffs and official sources.
The [runnable configuration example](../authn/cognito_example_test.go) shows resource-bound users and explicitly unbound M2M clients without network I/O.
