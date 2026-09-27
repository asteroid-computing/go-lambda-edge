# go-lambda-edge

A Go 1.27 module being built to serve AWS API Gateway requests through ordinary `net/http` handlers in AWS Lambda, with companion identity, authentication, and authorization packages.

The root package is `edge`.
The buffered adapter is implemented for REST proxy, HTTP API payload 1.0 and HTTP API payload 2.0.
It uses AWS Lambda Go v1.55.0 types and direct encoding/json/v2 processing.
Raw and already typed entry points share HTTP conversion, response capture, identity policy, cancellation and cleanup.

Start with the [complete orders dispatcher](examples/dispatcher/README.md) for verifier wiring, application grants, JSON errors and three transport entry points.
The [support matrix](docs/support.md) distinguishes implemented behavior, deferred scope and deployment qualifications;
the [testing guide](docs/consumer-testing.md) shows how to test each consumer boundary.

Create an adapter with `edge.New(handler, options...)`, handle the constructor error, then register it with `lambda.Start(adapter)`.
Pass the object, not `adapter.Invoke`, to retain the raw JSON v2 boundary.
For a fixed payload family, use `lambda.Start(adapter.HandleV1)` or `lambda.Start(adapter.HandleV2)`;
these delegate envelope JSON to the SDK and do no envelope serialization inside edge.
See the [runnable registration examples](adapter_example_test.go).

Gateway identity is disabled by default.
`edge.WithGatewayIdentity(true)` enables the reviewed native IAM and JWT/Cognito field mappings.
Invalid, conflicting or unsupported assertions fail before the handler;
custom-authorizer mapping remains unimplemented.
Authorization headers are not independently authenticated.
See the [fixture coverage and fidelity qualifications](docs/gateway-fixtures.md).

The shared invocation error contract is implemented across those private stages: public categories support `errors.Is`, and `errors.AsType[*edge.InvocationError]` exposes sanitized operation and resource-limit diagnostics.
HTTP error responses remain successful transport outcomes;
operation, cleanup, cancellation, and late stream failures retain their accepted ownership and precedence.
The shared claims API, caller/context construction, JWT normalization and resource limits are implemented in `identity`.
Every invocation rejects an inherited caller, including when gateway identity is disabled.
Constructors themselves do not authenticate credentials or authorize application actions.

The accepted authentication direction supports IAM credentials or an OAuth bearer token on the same route without built-in `AWS_IAM` authorization.
IAM clients use `iamproof.NewGenerator` to generate a signed STS GetCallerIdentity proof;
`iamproof.NewVerifier` submits that client-signed request to AWS and returns a verified IAM caller.
Both are implemented using the latest stable SDK core v1.47.0 and STS v1.51.0, checked on 2026-09-16.
See the [IAM proof guide](docs/iamproof.md) and [runnable examples](iamproof/example_test.go).
The shared `authn` selector and required-authentication middleware are implemented.
Configure Bearer and/or IAM-proof verifier functions;
the [authentication guide](docs/authn.md) shows the required IAM error mapping and the custom dispatcher/HTTP response boundaries.
`authn.NewCognitoVerifier` now provides RS256 access-token verification with direct JSON v2, explicit issuer/client/resource restrictions and bounded JWKS caching;
see the [Cognito guide](docs/cognito.md).
Wire its Verify method as the Bearer verifier.
The `authz` package now provides exact IAM/JWT predicates, ordered All/Any rules and context-aware application grant checks.
Consumers own their action registry, resource selection and HTTP responses;
see the [authorization guide](docs/authz.md) and [runnable dispatcher examples](authz/example_test.go).
No live Cognito/STS interoperability test has run.

REST response streaming is implemented through `edge.NewStreaming(handler)` and `lambda.Start(adapter.Handle)` or `lambda.Start(adapter.HandleV1)`.
It provides bounded sniffing, Flush/FlushError, raw body delivery, backpressure, cancellation, cleanup and sanitized terminal errors.
Streaming never infers Content-Length.
See the [streaming guide](docs/streaming.md) and [SSE, NDJSON, binary and gzip examples](streaming_example_test.go).
Local SDK tests pass, but deployment support remains unverified: SDK v1.55.0's Runtime API mode header and connection handling differ from current AWS guidance.
The [deployment qualification](docs/decisions/0022-streaming-writer-review.md#s3-sdk-issue-retain-the-existing-deployment-release-gate) remains open;
no live API Gateway streaming test has run.

## Header-selected actions

`edge.NewActionHeader("Action")` creates an immutable selector whose `Parse(r.Header)` method returns an action string or an error.
Configure the header name explicitly.
Selection preserves case, trims outer spaces/tabs, and requires a nonempty HTTP token.
Repeated values and comma-bearing values are rejected, including Gateway payload 2.0's comma-combined duplicates.
Use `errors.Is` with `edge.ErrActionMissing`, `edge.ErrActionAmbiguous`, or `edge.ErrActionInvalid` to distinguish request-input failures.

Use ordinary `http.Handler` middleware to process this and other custom headers.
The consumer owns its action registry, metadata, authorization and HTTP error responses.
Select once, authorize that selection, then execute it;
do not select again from mutable headers.
An action header does not authenticate a caller.
The selector works with both ordinary Go HTTP servers and the Lambda adapter.

See the [runnable middleware example](action_example_test.go) and [accepted header contract](docs/decisions/0011-request-header-processing.md).

## Response header budget

Response headers have a 256 KiB weighted resource budget, configurable with `edge.WithResponseHeaderBudget(bytes)` from 1 byte through 6 MiB.
Each original value costs `len(name) + len(value) + 32`;
nil/empty slices still cost a name plus 32, and generated headers consume remaining budget.
This is separate from the complete buffered-envelope limit and the 16,000-byte streaming metadata prefix (including its delimiter).
It is neither an AWS quota nor a total heap bound.
Both writers apply the allowance at commitment.
Raw buffered invocation also checks the complete serialized response against 6 MiB;
typed callers own their final envelope serialization and size.
Streaming checks the complete metadata prefix before handing off the reader;
its body does not use the buffered limit.

## Owned claims

`identity.NewJWT` and `identity.NewIAM` construct immutable, mutually exclusive caller views.
Zero Caller is anonymous.
Constructors validate facts and source attribution;
they do not authenticate credentials or grant permissions.
JWT issuer, subject, client ID, audience, scopes and Cognito groups stay distinct.
Ambiguous gateway collections remain unavailable, while conflicting interpretable scope sets fail.
IAM callers retain exact ARN paths and session names.

`identity.WithCaller` installs one caller in a derived context and rejects replacement, even by an apparently identical caller.
`identity.FromContext` returns anonymous when absent.
See the [caller examples](identity/caller_example_test.go).

`identity.ParseClaims`, `identity.NewClaims`, and `identity.NewTextClaims` accept raw JSON, decoded Go values, and gateway string maps respectively.
Claims are immutable snapshots with checked accessors.
Exact numeric text remains available where the input preserves it;
a decoded float64 is never advertised as an exact original JSON number, and gateway text is never reparsed as an embedded array.

Processing uses `encoding/json/jsontext` directly.
The only production `encoding/json` import recognizes its `Number` data type, supporting typed SDK handlers configured with `lambda.WithUseNumber(true)`;
no v1 codec is called.
See the [runnable examples](identity/claims_example_test.go).

The default claim allowance is 256 KiB, configurable with `identity.WithClaimsBudget(bytes)` up to 6 MiB.
Each value costs 64 bytes plus object-name, string and exact-number text bytes.
Raw JSON also has a wire-length limit of the same size.
Nesting is limited to 64 containers.
These are resource policies, not heap caps;
[measurements](docs/benchmarks.md) include allocation and retained-memory costs.
`edge.WithIdentityClaimsBudget(bytes)` applies the matching allowance to gateway claims and dedicated scopes.

Handlers receive an owned request body and canceled context after invocation.
Multipart files parsed on the served request are removed on success, error or panic.
Middleware that parses multipart on a separate request copy owns cleanup of files attached only to that copy.
Buffered and pre-handoff streaming panics propagate after cleanup;
post-handoff streaming panics become terminal errors.

- [Implementation plan](docs/plan.md)
- [Design decisions](docs/decisions/README.md)
- [Repository working agreement](AGENTS.md)
- [Release workflow and GitHub App setup](docs/releases.md)
- [Initial codec benchmarks](docs/benchmarks.md)
