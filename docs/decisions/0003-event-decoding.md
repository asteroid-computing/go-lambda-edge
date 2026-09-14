# 0003: AWS event types and explicit event decoding

Status: accepted by the user; private decoder and typed validators implemented.

## Question

Which event shapes does the decoder accept, and should our JSON representation
use the AWS SDK event structs or private types owned by this module?

## Evidence

- AWS's REST proxy example has `httpMethod`, `path`, and `requestContext`, with
  an API ID in the context and no top-level version discriminator. Its examples
  also show null optional fields.
  [REST proxy input](https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html)
  [AWS HTTP request example](https://aws.amazon.com/blogs/developer/handling-arbitrary-http-requests-in-amazon-api-gateway/)
- HTTP APIs support payload versions `1.0` and `2.0`. V1 carries method/path at
  the top level; V2 uses `requestContext.http.method` and `rawPath`. The versions
  differ in headers, queries, cookies, and custom-domain mapping visibility.
  [HTTP API payload formats](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)
- The inspected AWS Go SDK v1.54.0 uses non-pointer scalar event fields and
  `map[string]string` for V2 authorizer JWT claims. Those types are convenient
  consumers of the wire format, but alone cannot distinguish an omitted field
  from a zero value or preserve a non-string claim value should one arrive.
  [SDK event types](https://pkg.go.dev/github.com/aws/aws-lambda-go@v1.54.0/events#APIGatewayV2HTTPRequest)
  [SDK JWT claims](https://pkg.go.dev/github.com/aws/aws-lambda-go@v1.54.0/events#APIGatewayV2HTTPRequestContextAuthorizerJWTDescription)
- JSON v2 requires a single JSON value, rejects invalid UTF-8 and duplicate
  object names by default, and matches struct member names case-sensitively.
  JSON null generally decodes as a Go zero value; unmarshaling into `any` decodes
  numbers as float64. Presence/type validation therefore needs explicit handling.
  [JSON v2 documentation](https://pkg.go.dev/encoding/json/v2)
  [Unmarshal](https://pkg.go.dev/encoding/json/v2#Unmarshal)
- Lambda Function URLs use the same request/response schema as API Gateway V2.
  Shape validation cannot authenticate the invoking service.
  [Function URL payloads](https://docs.aws.amazon.com/lambda/latest/dg/urls-invocation.html)
- At the time of this decision, implementation contained only the constructor/options foundation.
  No existing decoder or wire schema constrains this choice. The accepted plan
  already requires direct JSON v2 and rejection rather than V2-to-V1 fallback.

## Decision

### Wire representation

Reuse AWS Lambda Go request/response structs wherever they meet the reviewed
contract, decoded and encoded directly with `encoding/json/v2`. The user accepted
this direction after questioning the original private-schema proposal. SDK type
reuse does not require using the SDK's JSON decoder. Keep a small explicit
version discriminator and targeted presence/type validation where needed;
do not duplicate complete AWS schemas just to select the event format.

Carry authorizer data internally as `jsontext.Value` until
the selected identity producer interprets it. Preserve exactly the JSON
representation AWS supplies;
this does not assert that AWS supplies full-fidelity JWT claims or authorize
guessing an array from a flattened string. Avoid float64 conversion of arbitrary
claim numbers at the transport boundary.

Authorizer extraction and SDK decoding must be designed together so an SDK claim
field cannot prematurely reject data intended to remain opaque. The exact
exception mechanism is an implementation detail; authorizer fidelity alone does not justify
private copies of the entire envelope.

The AWS Lambda Go library remains the runtime integration and supplies event
types. Its reflected function path decodes into the handler's declared parameter
type; an `any` parameter receives generic JSON values, not automatically selected
AWS event structs. Our raw boundary selects the version before decoding the
concrete event. Dependency-version selection remains separate.
[SDK handler implementation](https://github.com/aws/aws-lambda-go/blob/v1.54.0/lambda/handler.go)
[AWS Go handler documentation](https://docs.aws.amazon.com/lambda/latest/dg/golang-handler.html)

The user also requested a route for already typed events that avoids JSON work
inside edge. See [decision 0004](0004-typed-events.md) for the accepted signatures
and the limits of validation after another component has decoded the payload.

### Version dispatch and minimum structure

| Discriminator | Canonical method/path | Interpretation |
| --- | --- | --- |
| `version` absent | `httpMethod`, `path` | REST proxy envelope |
| `version` exactly `"1.0"` | `httpMethod`, `path` | HTTP API payload 1.0 envelope |
| `version` exactly `"2.0"` | `requestContext.http.method`, `rawPath` | Payload 2.0 envelope |
| Any other present value, including null, empty string, or a number | None | Reject |

For every accepted envelope require a non-null object `requestContext`, a
nonempty string `requestContext.apiId`, and the canonical method/path fields as
nonempty strings. V2 also requires the `requestContext.http` object. These are
accepted library validity checks based on the documented envelopes, not a claim
that AWS publishes a formal required-field schema for all variants.

Do not fall back to a different format after a decode/validation error. Do not
invent `GET` or `/` when a canonical method/path is missing. V2's reported HTTP
context path does not replace a missing `rawPath`. Method syntax and exact path
escaping are part of the subsequent HTTP translation contract.

The API ID helps reject unrelated event shapes such as ordinary ALB envelopes;
it is not proof of origin. We do not promise Function URL support merely because
a V2-shaped event passes validation, nor reject one based on guessed hostnames or
API ID spelling. Gateway identity remains the explicit deployment trust decision
accepted in decision 0001.

### Already typed events

The selected method determines the payload family without automatic dispatch.
Semantic checks still require nonempty API ID, method, and canonical
path. HandleV2 accepts Version equal to "2.0" or empty: an omitted Go string
does not make a caller-created V2 struct ambiguous, because its type and method
already select V2. Reject any other nonempty Version as contradictory input.
HandleV1 has no version field to inspect. Neither method can validate original
JSON shape, member presence, or syntax. Do not serialize structs to imitate
raw-wire validation.

This is an accepted ergonomic exception for explicitly typed input, not a change
to raw Invoke: a present empty version in JSON remains invalid there. Matching
raw and typed valid events share translation; their available validation evidence
differs as accepted in decision 0004.

### Strictness and forward compatibility

- Accept one top-level JSON object. Reject null, arrays, scalars, invalid UTF-8,
  duplicate object names, and trailing non-whitespace content.
- Use exact AWS member names and require correct types for consumed fields.
  Do not enable legacy case-insensitive matching or coercions.
- Permit unknown members for forward compatibility. Unknown data still must be
  valid JSON; this does not relax syntax checking inside authorizer data.
- Handle documented absent/null optional fields intentionally. In particular,
  absent/null body means empty bytes; absent/null header and query maps mean no
  entries. A missing `isBase64Encoded` means false; a present value must be a JSON
  boolean. Further fields will be specified when their consumers are designed.
- Preserve opaque authorizer data during transport decoding. Do not establish
  identity, interpret claim semantics, or validate a custom-authorizer schema
  when no identity producer was selected.
- Return failures through the invocation-error contract accepted in decision
  0002. Do not include the input payload or credential values in errors.

### Fixture matrix

| Case | Expected result |
| --- | --- |
| Documented REST envelope, no version | Select REST/V1 translation |
| HTTP API `1.0` envelope | Select HTTP API/V1 translation |
| HTTP API `2.0` envelope | Select V2 translation |
| Future version, null/empty/non-string version | Reject without fallback |
| Malformed V2 plus otherwise usable V1 fields | Reject V2; never reinterpret as V1 |
| Missing/null/wrong-type context, API ID, method, or canonical path | Reject |
| Unrelated SQS/S3/ALB events; empty object | Reject |
| Extra unknown fields | Accept if the selected envelope remains valid |
| Duplicate names, including in nested opaque data; invalid UTF-8 | Reject |
| Multiple JSON values or non-object root | Reject |
| Null optional body/maps | Accept as absent/empty |
| Opaque authorizer string, array, object, or numeric claims | Preserve wire values pending identity interpretation |
| Method/body carrying application-invalid data | Leave application validation to the appropriate later layer |
| Typed V2 with empty or "2.0" Version and valid required values | Select V2 directly |
| Typed V2 with other nonempty Version | Reject contradictory input |
| Typed V1/V2 missing API ID, canonical method, or path | Reject before HTTP handling |

Write fixtures from documented contracts and focused synthetic cases, not by
copying Beakley's tests. Include an SDK-generated representative event as an
additional compatibility check rather than treating SDK serialization as the
sole oracle. Run fuzzing against the decoder after implementation.

## Alternatives and tradeoffs

- Private copies of all event types: more schema maintenance than needed for
  automatic dispatch. Prefer SDK types with narrowly justified exceptions.
- Use `map[string]any` throughout: flexible but scatters type assertions and
  exposes numeric precision loss. Keep flexibility only where the schema is
  genuinely opaque.
- Guess a version from whichever decode succeeds: accepts malformed events in
  the wrong format and contradicts the approved rejection policy.
- Reject all unknown fields: catches misspellings, but makes additive AWS fields
  a breaking change for deployed applications.

SDK structs alone do not provide the accepted presence checks or opaque-claim
contract. Limit supplementary representations to those needs and maintain
independent wire fixtures so SDK serialization is not the sole specification.

## Resolution

The user accepted AWS type reuse, the typed entry points, and subsequently the
remaining decoding contract in full: dispatch, structural checks, strict JSON,
opaque authorizer data, and the typed V2 empty-version exception. This supersedes
the original recommendation for entirely private wire types.

The private decoder and typed validators are implemented. Pin AWS Lambda Go
v1.55.0, verified against the
Go module proxy on 2026-09-14. Its API Gateway event definitions are unchanged
from the inspected v1.54.0, and its reflected handler still uses encoding/json.
Use SDK embedding with a shadowed request context/authorizer field to retain
opaque data without copying the complete schema. SDK dependency selection and
this private decoding mechanism do not alter the accepted public API.

Validation: documented-shape synthetic fixtures, malformed/unsupported envelopes,
opaque authorizer ownership and precision, error redaction, and SDK serialization
compatibility pass. Race tests, vet, and Linux arm64/amd64 builds pass. A 15-second
fuzz run completed 1,553,008 executions without a failure. Public Invoke and typed
HTTP entry points still await response encoding and gateway identity production; these
checks do not claim end-to-end Lambda adaptation is implemented.
