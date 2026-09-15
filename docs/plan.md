# Edge implementation plan

Status: accepted overall direction; individual contracts require review.

## Objective

Build `github.com/asteroid-computing/go-lambda-edge`, with root package `edge`,
as a new Go 1.27 module. Adapt AWS API Gateway Lambda proxy events to ordinary
`net/http` handlers and provide complementary identity, authentication,
authorization, and consumer-test packages.

Beakley was reviewed at `/Volumes/home/Developer/spmt-20260905/beakley`.
Its requirements and observed failures inform this design. This work is not a
port, source copy, or compatibility-preserving update of Beakley.

## Accepted direction

- Set the module's Go baseline to 1.27.0.
- Use `encoding/json/v2` directly for our event, response, JWKS, and claim JSON
  handling. Do not route our codec through `encoding/json`'s compatibility API.
  Check third-party parser behavior separately during dependency selection.
- Keep ordinary `http.Handler` as the application boundary.
- Provide first-class consumer request-header processing for header-selected
  action dispatchers. Preserve custom incoming headers independently of response
  combination rules. [Decision 0011](decisions/0011-request-header-processing.md)
  accepts the standard HTTP extension boundary and immutable ActionHeader
  selector, with explicit configuration and strict missing/ambiguous/invalid
  selection errors. The user approved the Go review refinements on 2026-09-15.
- Own event serialization at a raw Lambda invocation boundary implementing
  `Invoke(context.Context, []byte) ([]byte, error)`.
- Reuse AWS Lambda Go event/response structs where they meet the contract;
  reserve custom representations for demonstrated limitations.
- Use the latest stable aws-lambda-go release, recorded explicitly in go.mod
  for reproducibility. On 2026-09-14, the Go module proxy and AWS GitHub releases
  both confirmed v1.55.0, already required here; it includes Go 1.27 compatibility.
- Support already typed events without an envelope JSON round trip inside edge.
  Decision 0004 accepts HandleV1 and HandleV2 with matching AWS request/response
  types, sharing translation and identity policy with raw invocation.
- Support REST API proxy events, HTTP API payload 1.0, and HTTP API payload 2.0.
  API product and payload version are distinct concepts.
- Start with buffered responses. Streaming is a separate future capability.
- Keep payload dispatch and the response writer private initially.
- Preserve separate IAM/JWT caller representations and an anonymous zero value;
  enforce invariants rather than relying on comments about a union.
- Separate token scopes, Cognito groups, and application-resolved grants.
- Preserve context-aware authorization and distinguish denial from dependency
  failure.
- Preserve full-fidelity claims when the selected source provides them. Do not
  claim to recover arrays or original numeric values from flattened data.
- Research substantive decisions and bring recommendations to the user before
  implementing dependent behavior. See `AGENTS.md`.

The user approved [decision 0001](decisions/0001-gateway-identity.md), superseding
the review's initial gateway-default proposal: no gateway-derived identity by
default; `WithGatewayIdentity(true)` opts in for both IAM and JWT/Cognito. Local
verification is explicitly composed. Constructor details and precedence between
multiple identity producers remain separate decisions.

## Planned package boundaries

| Package | Responsibility |
| --- | --- |
| `edge` (root) | Invocation adapter, payload decoding, HTTP translation, gateway metadata, optional gateway identity extraction |
| `identity` | Validated callers, claim representation, context transport; standard library only |
| `authn` | Bearer authentication, explicitly named Cognito verification, JWKS caching |
| `authz` | Decisions, rules, combinators, middleware, application-principal resolution |
| `edgetest` | Consumer identity fixtures and gateway event helpers |

Avoid a broad root-package facade that reexports every companion type. Keep AWS
event types out of transport-independent packages. Reassess any additional
exported package against a concrete consumer need; version-reporting machinery
does not need to precede a working adapter.

## Milestones and acceptance criteria

### 0. Persist the design and working agreement

- [x] Create a working branch from the initial `main` commit.
- [x] Record accepted direction and review findings.
- [x] Establish decision records and review workflow.
- [x] Resolve decision 0001 before implementing authentication defaults.

### 1. Specify the transport contracts

Accepted: [decision 0002](decisions/0002-adapter-boundary.md) settles construction,
option application, and transport invocation-failure handling. Its independent
constructor/options foundation is implemented while the event/HTTP contracts are
reviewed. Constructor tests pass with race detection, vet and formatting checks
pass, and package builds pass for Linux arm64 and amd64.

Accepted: [decision 0003](decisions/0003-event-decoding.md) settles AWS type reuse,
version dispatch, structural validation, and the decoder fixture matrix. The
private decoder and typed validators are implemented and tested against AWS
Lambda Go v1.55.0. [Decision 0004](decisions/0004-typed-events.md) accepts typed
methods that skip envelope JSON work inside edge. These entry points await the
response contract and gateway identity production before public wiring.

Accepted: [decision 0005](decisions/0005-http-request-translation.md) settles
request URLs, header/query merging, cookies, body handling, invocation metadata,
and request ownership. Shared request conversion is implemented with local HTTP
comparisons, race tests, fuzzing, vet, and Lambda-target builds passing.

Accepted: [decision 0006](decisions/0006-buffered-responses.md) specifies
buffered response commitment/encoding, unsupported features, payload bounds,
invocation completion, and the documented multipart cleanup limit.

The accepted [2026-09-14 plan review](reviews/2026-09-14-plan-review.md) requires
response-contract refinements and completing a runnable adapter, including the
minimum gateway identity foundation, before broader authentication work. These
enhancements are incorporated into decision 0006. Concrete metadata/header
policies, exported error categories, and identity APIs remain separate reviews.

The user accepted the qualification to decision 0007: do not freeze arbitrary
64 KiB/1,024-entry metadata limits or the initial ten-field list without stronger
resource and compatibility justification. [Decision 0010](decisions/0010-response-header-design.md)
now proposes shared snapshots, a 25-field V2 combination audit, weighted resource
accounting, and a conservative exact streaming-prefix limit. These details await
user review; no header-policy implementation has been added. Streaming is specified in
[decision 0008](decisions/0008-streaming-architecture.md): explicit REST response
streaming alongside the buffered adapter, sharing HTTP/identity foundations.
The high-level architecture is accepted. [Decision 0009](decisions/0009-streaming-boundaries.md)
accepts concrete APIs and an ownership/state graph. The private bridge and shared
invocation ownership primitives are implemented. A local SDK Runtime API
probe demonstrates incremental delivery, raw/typed capture, closure, and error
trailers; it also exposes a bytes-plus-error reader issue and a missing header
relative to AWS's documented streaming contract. No production streaming API is
implemented, and deployed API Gateway behavior is not verified.

The private bridge publishes an owned prefix through an explicit handoff before
body writes can block, uses an unbuffered pipe, and transfers cleanup to one
producer. Close and parent cancellation release blocked I/O; terminal reads and
Close join cleanup and error reporting. Early panics are rethrown at entry, late
panics become sanitized terminal errors, and reader results defer errors that
accompany bytes. Synthetic concurrency tests cover these boundaries and reporter
failures. Full race tests (including the existing SDK probe), vet, formatting,
and Linux arm64/amd64 builds pass on Go 1.27.1. HTTP streaming commitment,
sniffing, framing validation, and public entry points remain to be implemented.

Implemented under decision 0006: bounded direct JSON v2 envelope encoding,
text/base64 body selection, and a shared invocation scope that preserves primary
errors, cleans up on failures/panics, and checks parent cancellation. The HTTP
writer and gateway header conversion await the concrete policy in
[decision 0007](decisions/0007-response-metadata.md). Public wiring still awaits
the minimum identity contracts. [Initial codec benchmarks](benchmarks.md) record
allocation baselines; they do not establish a peak-memory bound.

Research and present a contract covering:

- Raw invocation API, constructor/options, configuration validation, and errors.
- Discrimination between all three supported event cases; reject unsupported
  versions and malformed shapes without fallback to a different format.
- Strict JSON v2 handling of duplicate names and invalid UTF-8, with tolerance
  for additional AWS fields.
- V1 decoded paths versus V2 raw paths, escaped segments, query representation,
  custom-domain mappings, and information that AWS does not preserve.
- Headers and multivalue precedence per key, cookies, host, protocol,
  `RequestURI`, source address, body decoding/length/cleanup, and context lifetime.
- Status commitment, header snapshots, implicit responses, content-type
  detection, HEAD/bodyless statuses, informational responses, trailers, and
  accurately advertised optional `ResponseWriter` capabilities.
- Binary response selection and REST API deployment requirements; response-size
  limits, including base64 and JSON-envelope overhead.
- Separation of HTTP application outcomes from invocation/translation faults.

Acceptance: a reviewed contract and fixture matrix exist before the corresponding
behavior is implemented. Resolve smaller decisions incrementally rather than
presenting an entire frozen public API at once.

### 2. Implement and prove the HTTP adapter

- [x] Create the Go 1.27 module and constructor/options foundation after review.
- [x] Implement the private decoder and typed semantic validators with direct
      JSON v2, SDK types, opaque authorizer data, fixtures, and fuzzing.
- [x] Implement shared HTTP request conversion and request-lifetime cleanup.
- [x] Implement the accepted action-header selector with ordinary HTTP middleware
      examples and native HTTP/raw/typed Gateway request contract tests.
- [ ] Implement the buffered HTTP writer and gateway response conversion.
- [x] Implement bounded envelope/body encoding and shared invocation cleanup.
- [x] Extract shared invocation ownership and implement the private streaming
      bridge, with handoff, backpressure, cancellation, cleanup, and error tests.
- [ ] Implement the minimum reviewed identity/context and native gateway producers
      needed to honor WithGatewayIdentity(true) before publishing invocation.
- [ ] Implement raw invocation after the remaining transport contracts are reviewed.
- [ ] Implement the accepted typed entry points after shared HTTP translation
      review, documenting upstream codec ownership.
- Implement the adapter from scratch using JSON v2 directly.
- Test raw invocation through the actual pinned AWS Lambda Go SDK boundary.
- Add runnable raw/typed registration examples, sequential/concurrent invocation
  isolation checks, and ServeMux, Redirect, ServeContent/Range, compression, and
  cookie integration cases.
- Exercise documented event fixtures and adversarial/malformed input.
- Compare relevant request/response behavior with real `net/http` serving,
  documenting unavoidable transport differences.
- Fuzz decoding and path/query conversion; add targeted allocation benchmarks.
- Run appropriate tests, race detection, vet, and Lambda-target build checks.
- Put those checks in Go 1.27 CI and establish raw/typed, binary, and near-limit
  allocation baselines before setting performance thresholds.

CI configuration is present for Go 1.27.0/latest 1.27 patch tests, race detection,
vet, and Lambda-target builds. It has not run on GitHub yet. Local Go 1.27.1 race
tests, vet, formatting checks, and both target builds pass for the initial
encoding/lifetime implementation.

Acceptance: the same application handler behaves as specified across all three
event cases; no auth or codec behavior depends on accidental SDK dispatch.

### 3. Identity and gateway producers

- Review caller constructors, immutable/owned data, validation, anonymous state,
  and context-presence semantics.
- Preserve issuer plus subject for JWT application identity; do not treat a
  bare subject as globally unique or assume every M2M caller has a user subject.
- Keep audience, app client, token use, and provenance distinct.
- Implement the approved opt-in gateway policy for native JWT/Cognito and IAM.
- Review custom-authorizer mapping for both payload families, including context,
  error returns, provenance, and conflicts between identity sources.
- Expose only the gateway metadata handlers actually need, separately from
  authenticated identity. Do not synthesize cryptographic guarantees from it.

Acceptance: invalid/ambiguous identities cannot authorize; gateway and token
claims cannot be silently mixed; unavailable claim fidelity remains explicit.

### 4. Cognito verification and JWKS resilience

- Review a verifier requiring explicit issuer/client restrictions; accepting all
  pool clients must be a deliberate policy.
- Keep access-token `client_id`, ID-token audience, and access-token resource
  audience validation separate. Preserve access-token-only defaults unless
  deliberately widened.
- Review algorithm/key selection, expiry/not-before/issued-at behavior, leeway,
  malformed claims, and the chosen JWT dependency's JSON behavior.
- Separate cache freshness, unknown-key refresh, and failure backoff.
- Bound network duration and JWKS response size. Define stale-key behavior,
  rotation, concurrency, waiter cancellation, and HTTP client ownership.
- Use deterministic cache/rotation/outage tests; consider Go 1.27 test networking
  and `testing/synctest` where useful.

Acceptance: malformed credentials fail closed, failures do not create fetch
storms, and dependency outages remain distinguishable from invalid credentials.

### 5. Authorization and consumer ergonomics

- Implement reviewed rules/combinators, scope/group/grant separation, principal
  resolution, and explicit authentication precedence.
- Review missing/invalid credential responses (ordinarily 401), authenticated
  denial (403), and dependency unavailability (503), including bearer challenges
  and behavior on public routes.
- Define ARN matching precisely, including separators, partitions, account
  boundaries, and STS role sessions. Do not approximate IAM policy evaluation.
- Validate nil/empty authorizers and invalid identities consistently.
- Provide consumer fixtures, runnable examples, and limited structured logging
  that excludes raw credentials and unnecessary profile claims.

Acceptance: examples work both in Lambda and ordinary HTTP applications, while
authorization decisions remain inspectable and deny by default.

## Findings carried forward from the Beakley review

These are requirements to test independently, not instructions to preserve old
implementation structure.

1. Unverified header JWTs were reconciled using only `sub` and `client_id`.
   A probe admitted altered groups with an invalid signature when modeling a
   gateway/adapter identity-source mismatch. This is conditional on that mismatch,
   not a demonstrated bypass of a correctly aligned native JWT authorizer.
2. Repeated final `WriteHeader` calls replaced the committed status, and header
   changes after commitment appeared in output. Both reproduced.
3. V1 path `/a?b#c` was interpreted as path, query, and fragment rather than as
   the supplied path. Reproduced.
4. V1 helpers discarded single-value maps wholesale when multivalue maps were
   nonempty. Host lookup and request metadata also need defined semantics.
5. A caller with both IAM and JWT arms could satisfy both rule conditions.
   Reproduced; validation was not consistently enforced.
6. Cold JWKS failures bypassed the refetch floor: three immediate calls produced
   three network requests. Reproduced. Outage and cancellation behavior need
   explicit contracts beyond the existing successful-cache tests.
7. An attached anonymous identity prevented subsequent bearer verification.
   Existing tests codified that behavior; the new composition policy must be
   deliberate rather than inherited.
8. `path.Match` permits wildcards across colons, contrary to the old ARN-matching
   comment. Synthetic IAM-role examples are insufficient for role-session cases.
9. Client ID and audience were conflated, and verifier construction allowed any
   client in the issuer's pool unless explicitly restricted.

Validation performed during review: the old suite passed with `-race` in a
temporary copy after changing only its Go directive to 1.27. The original 1.26
directive triggered Go 1.27's standard-library version checks for JSON v2. The
SDK serialization-boundary probe passed on Go 1.27. No live AWS integration was
deployed or exercised, and the Beakley repository was not edited.

## Official references

- [Go 1.27 release notes](https://go.dev/doc/go1.27)
- [JSON v2](https://pkg.go.dev/encoding/json/v2)
- [HTTP ResponseWriter](https://pkg.go.dev/net/http#ResponseWriter)
- [AWS Lambda Go Handler](https://pkg.go.dev/github.com/aws/aws-lambda-go/lambda#Handler)
- [API Gateway HTTP API payload formats](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)
- [Gateway JWT validation](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-jwt-authorizer.html)
- [Gateway IAM authorization](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-access-control-iam.html)
- [Cognito resource binding](https://docs.aws.amazon.com/cognito/latest/developerguide/cognito-user-pools-define-resource-servers.html)
- [REST API binary media](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-payload-encodings.html)
