# go-lambda-edge

A Go 1.27 module being built to serve AWS API Gateway requests through ordinary
`net/http` handlers in AWS Lambda, with companion identity, authentication, and
authorization packages.

The root package is `edge`. This project is being designed and built from scratch.
The constructor, options, private event decoder, and request conversion are implemented. The decoder
uses AWS Lambda Go v1.55.0 event structs and encoding/json/v2 directly, retaining
authorizer JSON separately. Request conversion covers URLs, headers, body bytes,
and request lifetime. A shared invocation scope now covers cleanup and cancellation;
private response encoding provides text/base64 selection and bounded direct JSON
v2 output. Private response-header snapshots and V1/V2 projections now enforce
the accepted syntax, suppression, cookie and field-combination rules. The private
buffered HTTP writer now implements commitment, sniffing, bodyless/HEAD responses,
length enforcement and typed AWS response projection. Public invocation methods
and identity extraction are not implemented yet; the adapter cannot yet be
registered as a Lambda handler.

The shared invocation error contract is implemented across those private stages:
public categories support `errors.Is`, and `errors.AsType[*edge.InvocationError]`
exposes sanitized operation and resource-limit diagnostics. HTTP error responses
remain successful transport outcomes; operation, cleanup, cancellation, and late
stream failures retain their accepted ownership and precedence. The shared
claims API, caller/context construction, JWT normalization and resource limits
are implemented in `identity`. The private invocation boundary rejects inherited
callers. Native gateway extraction remains pending its fixture/recognition review.

The accepted authentication direction supports IAM credentials or an OAuth
bearer token on the same route without built-in `AWS_IAM` authorization. IAM
clients will use an exported Go helper to generate a signed STS GetCallerIdentity
proof; the server verifier submits that client-signed request to AWS and uses the
verified identity for application authorization. The helper, verifier and their
client/server examples are planned. The detailed protocol/API is accepted in
[decision 0017](docs/decisions/0017-iam-proof-protocol.md), but not implemented.

The accepted streaming design now has a private bridge with incremental delivery,
backpressure, cancellation, cleanup, and terminal-error handling. Its lifecycle
is covered by race-enabled synthetic concurrency tests. Bounded JSON v2 metadata
prefix encoding is implemented. HTTP streaming commitment and public streaming
entry points remain under construction; deployed API Gateway
streaming has not been verified.

## Header-selected actions

`edge.NewActionHeader("Action")` creates an immutable selector whose
`Parse(r.Header)` method returns an action string or an error. Configure the
header name explicitly. Selection preserves case, trims outer spaces/tabs, and
requires a nonempty HTTP token. Repeated values and comma-bearing values are
rejected, including Gateway payload 2.0's comma-combined duplicates. Use
`errors.Is` with `edge.ErrActionMissing`, `edge.ErrActionAmbiguous`, or
`edge.ErrActionInvalid` to distinguish request-input failures.

Use ordinary `http.Handler` middleware to process this and other custom headers.
The consumer owns its action registry, metadata, authorization and HTTP error
responses. Select once, authorize that selection, then execute it; do not select
again from mutable headers. An action header does not authenticate a caller.
The selector is usable now with ordinary Go HTTP servers, independently of the
unfinished Lambda invocation methods.

See the [runnable middleware example](action_example_test.go) and
[accepted header contract](docs/decisions/0011-request-header-processing.md).

## Response header budget

Response headers have a 256 KiB weighted resource budget, configurable with
`edge.WithResponseHeaderBudget(bytes)` from 1 byte through 6 MiB. Each original
value costs `len(name) + len(value) + 32`; nil/empty slices still cost a name plus
32, and generated headers consume remaining budget. This is separate from the
complete buffered-envelope limit and the 16,000-byte streaming metadata prefix
(including its delimiter). It is neither an AWS quota nor a total heap bound.
The option validates configuration now, and the private buffered writer applies
it at commitment. Public invocation and the streaming HTTP writer remain pending.

## Owned claims

`identity.NewJWT` and `identity.NewIAM` construct immutable, mutually exclusive
caller views. Zero Caller is anonymous. Constructors validate facts and source
attribution; they do not authenticate credentials or grant permissions.
JWT issuer, subject, client ID, audience, scopes and Cognito groups stay distinct.
Ambiguous gateway collections remain unavailable, while conflicting interpretable
scope sets fail. IAM callers retain exact ARN paths and session names.

`identity.WithCaller` installs one caller in a derived context and rejects
replacement, even by an apparently identical caller. `identity.FromContext`
returns anonymous when absent. See the [caller examples](identity/caller_example_test.go).

`identity.ParseClaims`, `identity.NewClaims`, and `identity.NewTextClaims` accept
raw JSON, decoded Go values, and gateway string maps respectively. Claims are
immutable snapshots with checked accessors. Exact numeric text remains available
where the input preserves it; a decoded float64 is never advertised as an exact
original JSON number, and gateway text is never reparsed as an embedded array.

Processing uses `encoding/json/jsontext` directly. The only production
`encoding/json` import recognizes its `Number` data type, supporting typed SDK
handlers configured with `lambda.WithUseNumber(true)`; no v1 codec is called.
See the [runnable examples](identity/claims_example_test.go).

The default claim allowance is 256 KiB, configurable with
`identity.WithClaimsBudget(bytes)` up to 6 MiB. Each value costs 64 bytes plus
object-name, string and exact-number text bytes. Raw JSON also has a wire-length
limit of the same size. Nesting is limited to 64 containers. These are resource
policies, not heap caps; [measurements](docs/benchmarks.md) include allocation and
retained-memory costs. `edge.WithIdentityClaimsBudget(bytes)` records the matching
gateway configuration, whose producer wiring remains pending.

- [Implementation plan](docs/plan.md)
- [Design decisions](docs/decisions/README.md)
- [Repository working agreement](AGENTS.md)
- [Initial codec benchmarks](docs/benchmarks.md)
