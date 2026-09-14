# 0003: Private wire types and explicit event decoding

Status: proposed; awaiting user review.

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
- Our implementation currently contains only the constructor/options foundation.
  No existing decoder or wire schema constrains this choice. The accepted plan
  already requires direct JSON v2 and rejection rather than V2-to-V1 fallback.

## Recommendation

### Wire representation

Use private, purpose-built request/response types, decoded and encoded directly
with `encoding/json/v2`. Model only fields needed by the reviewed transport and
identity contracts. Keep presence information where validation needs it.

Carry authorizer data internally as `jsontext.Value` until the selected identity
producer interprets it. Preserve exactly the JSON representation AWS supplies;
this does not assert that AWS supplies full-fidelity JWT claims or authorize
guessing an array from a flattened string. Avoid float64 conversion of arbitrary
claim numbers at the transport boundary.

The AWS Go SDK remains the runtime integration in consuming applications and a
compatibility-test target. Its event structs need not control our internal
decoder or become exported API. Dependency-version selection remains separate.

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
proposed library validity checks based on the documented envelopes, not a claim
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

Write fixtures from documented contracts and focused synthetic cases, not by
copying Beakley's tests. Include an SDK-generated representative event as an
additional compatibility check rather than treating SDK serialization as the
sole oracle. Run fuzzing against the decoder after implementation.

## Alternatives and tradeoffs

- Reuse SDK event types directly: less schema code to maintain, but additional
  presence validation and opaque-data extraction are still needed. A hybrid
  decoder would then carry two representations of important parts of the input.
- Use `map[string]any` throughout: flexible but scatters type assertions and
  exposes numeric precision loss. Keep flexibility only where the schema is
  genuinely opaque.
- Guess a version from whichever decode succeeds: accepts malformed events in
  the wrong format and contradicts the approved rejection policy.
- Reject all unknown fields: catches misspellings, but makes additive AWS fields
  a breaking change for deployed applications.

Private types create schema-maintenance work. Limit it to fields we consume,
link their source documentation, and maintain independent wire fixtures so the
types do not become a self-confirming specification.

## Resolution

Pending user approval or redirection. No decoder is implemented under this
proposal yet.
