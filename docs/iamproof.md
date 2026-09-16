# IAM proof generation and verification

The `iamproof` package is implemented. It authenticates a client using its AWS
credentials without requiring an API Gateway `AWS_IAM` authorizer. The client
generates an audience-bound, presigned GetCallerIdentity proof; the server
reconstructs that signed request and asks regional STS to authenticate it.
The verifier never signs using the Lambda role or other server credentials.

This is a library-defined proof protocol, not an ordinary SigV4 request to your
API, an EKS token, or an OAuth bearer token. The shared HTTP credential selector
and OAuth verification remain separate work. Merely registering the edge adapter
does not authenticate an Authorization header.

## Client

Use the credentials provider from your existing AWS SDK configuration:

```go
generator, err := iamproof.NewGenerator("eu-west-2", "orders.production", cfg.Credentials)
if err != nil {
    return err
}
token, err := generator.Generate(ctx)
if err != nil {
    return err
}
req.Header.Set("Authorization", "EdgeIAM "+token.Value())
req.Header.Set("Action", "ListOrders")
```

`cfg` is the application's AWS SDK configuration, and `req` is its outgoing
HTTPS request. The provider owns caching, refresh, AssumeRole and credential
retrieval I/O. Generate retrieves credentials each time and signs locally; it
does not contact STS. Temporary credentials and their session tokens are
supported. Provider AccountID is not used as identity evidence.

Use a distinct, case-sensitive audience for each application/environment. The
audience is 1-256 visible ASCII bytes without spaces. Clients may reuse a token
before ExpiresAt, which is the earlier of the proof expiry and known credential
expiry. AWS may reject credentials earlier. The runnable
[client example](../iamproof/example_test.go) uses synthetic credentials and
makes no network requests.

## Server

Construct one verifier with the same explicit region and audience. Its
`Verify(ctx, tokenValue)` method takes the credential without `EdgeIAM ` and
returns `identity.Caller`. It never installs the caller in a context. Successful
callers have SourceVerifiedIAMProof, preserve the full ARN/session and STS UserId,
and have an account and partition checked against the response and endpoint.
Root, user, assumed-role session and federated-user identities are represented;
representation grants no application permission.

The authentication layer must select exactly one credential, call exactly one
verifier, install the returned caller using identity.WithCaller, then authorize
the selected application action. Shared middleware and its HTTP challenges will
be reviewed separately. Never fall back to another verifier after a rejection.

| Outcome | Inspect with errors.Is | Intended HTTP treatment |
| --- | --- | --- |
| Malformed, wrongly bound, expired or AWS-rejected proof | iamproof.ErrInvalidProof | Invalid credentials, 401 |
| Network failure, internal deadline, throttling, unexpected status/XML/identity | iamproof.ErrUnavailable | Dependency unavailable, 503 |
| Caller cancellation/deadline | context.Canceled / context.DeadlineExceeded | Follow request cancellation lifecycle |
| Invalid construction or unconfigured object | iamproof.ErrInvalidConfiguration | Configuration error |
| Client retrieval, expired/incomplete credentials or signing failure | iamproof.ErrCredentialsUnavailable | No proof generated |
| Client proof exceeds 8 KiB | iamproof.ErrProofTooLarge | No proof generated; never truncate |

The package itself does not write these HTTP responses. Authorization denial
after successful authentication is separate and ordinarily produces 403.
All failures return an anonymous caller or zero token, and errors omit raw
provider/transport/XML diagnostics. Token string/debug formatting is redacted;
Value explicitly exposes the credential. Do not log that value, Authorization
headers, session tokens, signed URLs or transport dumps containing them.

## Network, freshness and limits

- Every verification contacts STS, with no application retry or result cache.
  Size admission control against the shared STS quota, normally 600 requests per
  second per account and Region for credential-based requests.
- The owned HTTP client refuses redirects and has no cookie jar. The default
  deadline is five seconds and covers receiving the body. WithTimeout accepts a
  positive override; an earlier caller deadline still wins.
- WithTransport supplies trusted deployment infrastructure. Its TLS/proxy
  settings, concurrency safety, cancellation handling and internal retries are
  the application's responsibility. The default is http.DefaultTransport as
  configured when NewVerifier is called. Options apply in order, last value wins.
- Encoded credentials, including `v1.`, are limited to 8,192 bytes. This is not an
  AWS session-token maximum, nor a guarantee the entire API request fits gateway
  header limits. Incoming oversize proofs are invalid credentials.
- STS success/error bodies are limited to 65,536 bytes, parsed as XML and closed.
  Unknown metadata is tolerated; duplicate/missing identity fields, unexpected
  namespaces, contradictory accounts/partitions and second documents fail.
- Proofs expire at signing time plus 60 seconds, with no expired-proof grace.
  Up to 30 seconds of future clock skew is allowed. Freshness is checked before
  and after STS, independently of AWS enforcement of X-Amz-Expires.

A proof can be replayed to the same audience while fresh; the possible local
acceptance interval under skew is up to 90 seconds. It does not bind the API
method, body or action header and is not a single-use challenge. HTTPS is
required to the application and STS. Neither freshness nor audience validation
alone authenticates a proof: STS must accept its signature.

## Endpoint and dependency policy

Production dependencies were checked against the Go module proxy on 2026-09-16:
AWS SDK core v1.47.0 and STS v1.51.0 were the latest stable releases. The root
adapter and identity package do not import the SDK; only iamproof does.

Both constructors require an explicit standard regional endpoint. They expose
no global, FIPS, dual-stack or arbitrary endpoint switch. Endpoint resolution
uses the SDK V2 resolver. Because that public result omits the partition ID,
the package obtains it from the SDK's public legacy resolver and requires both
resolvers to agree on endpoint and signing region. Disagreement fails
construction. No private SDK APIs or copied partition table are used.

Local fixtures cover all eight partitions in that SDK release, including
commercial, GovCloud, China, the four ISO partitions and European Sovereign
Cloud. This checks metadata and signing reconstruction, not regional account
eligibility or live service availability.

## Protocol and verification evidence

[Decision 0017](decisions/0017-iam-proof-protocol.md) specifies the compact JSON v2
envelope, canonical unpadded base64url, fixed STS request and accepted error policy.
The library uses direct encoding/json/v2 for generation and parsing, and the
official SDK v4 signer for cryptography. It rejects unknown/duplicate JSON
members, nulls, invalid UTF-8, trailing data, noncanonical base64, wrong audience
or scope, and malformed dates/signatures before network work.

Tests use synthetic credentials, fake transports, a local TLS server and virtual
time. They verify reconstruction against the official signer, tampering,
freshness boundaries, response validation, bounded reads, cleanup, cancellation,
error sanitization and concurrent use. There is no live STS or deployed Gateway
interoperability result yet; those checks require separate authorization.

Official sources:

- [GetCallerIdentity response contract](https://docs.aws.amazon.com/STS/latest/APIReference/API_GetCallerIdentity.html)
- [Regional STS endpoints](https://docs.aws.amazon.com/sdkref/latest/guide/feature-sts-regionalized-endpoints.html)
- [STS credential fields](https://docs.aws.amazon.com/STS/latest/APIReference/API_Credentials.html)
- [STS common errors](https://docs.aws.amazon.com/STS/latest/APIReference/CommonErrors.html)
- [GetCallerIdentity SignatureDoesNotMatch](https://docs.aws.amazon.com/eks/latest/userguide/security-iam-troubleshoot.html)
- [ExpiredToken with temporary credentials](https://repost.aws/knowledge-center/iam-credentials-token)
- [STS quotas](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_iam-quotas.html)
