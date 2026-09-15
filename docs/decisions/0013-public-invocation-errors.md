# 0013: Public invocation error contracts

Status: proposed for user review on 2026-09-15. Not implemented.

## Evidence

Decisions 0002, 0004, 0006, and 0009 already settle response ownership, typed
zero responses on failure, sticky writer faults, cancellation precedence, and
the streaming handoff boundary. This proposal gives those outcomes a public Go
contract; it does not reopen them.

Current code returns sanitized private errors from decode/request conversion,
response snapshots/encoding, cleanup, and streaming. It already preserves
http.ErrNotSupported and http.ErrContentLength where meaningful. Body-forbidden
writes return http.ErrBodyNotAllowed without poisoning an otherwise valid
response. `withInvocation` joins cleanup faults after the operation fault and
uses parent cancellation only when no operation/cleanup error exists.

AWS documents that a Lambda function error or malformed proxy response produces
an API Gateway 502, while a Lambda API invocation rejection produces 500. A valid
proxy response controls the HTTP status; API Gateway does not retry Lambda
invocations. Go documents errors.Is, errors.AsType, and traversal of joined error
trees. The SDK's Handler interface has one result/error channel for a buffered
invocation, not a separate application HTTP error channel.

Sources checked through AWS public MCP and installed Go documentation:

- [API Gateway/Lambda errors](https://docs.aws.amazon.com/lambda/latest/dg/services-apigateway-errors.html)
- [SDK Handler at v1.55.0](https://pkg.go.dev/github.com/aws/aws-lambda-go@v1.55.0/lambda#Handler)
- [Go error inspection](https://pkg.go.dev/errors)
- [errors.Join](https://pkg.go.dev/errors#Join)
- [errors.AsType](https://pkg.go.dev/errors#AsType)
- [HTTP unsupported features](https://pkg.go.dev/net/http#ErrNotSupported)
- [Streaming boundary already approved](0009-streaming-boundaries.md)

## E1. HTTP outcomes stay separate from invocation failure

**Recommend retaining the accepted boundary:**

| Outcome | Result |
| --- | --- |
| Malformed Lambda event, request conversion failure, invalid opted-in gateway assertion | Go invocation error; no HTTP handler |
| Local bearer rejected, permission denied, application validation failure | Middleware/handler writes HTTP response; nil invocation error if transport completes |
| Auth dependency unavailable | Middleware chooses its HTTP response, normally 503 under the later authn/authz contract; not automatically an adapter failure |
| Invalid response construction, unsupported committed behavior, resource limit, cleanup failure | Go invocation error; discard buffered result |
| Application explicitly writes 500 | Valid HTTP response with nil invocation error if transport completes |

**Reasoning:** an invalid token presented to an explicitly configured verifier
is an application authentication outcome. An inconsistent gateway assertion is
a failure to establish the integration's promised identity. Turning both into
the same Go error would make routine authentication failures appear as gateway
502s. The transport must not manufacture 401/403/503 from arbitrary errors.

Authn/authz HTTP status, bearer challenges, and public-route policies still need
their own review. These errors do not settle that middleware's complete API.

## E2. Stable categories plus one diagnostic type

**Question:** export each private error, only strings, or structured categories?

**Recommend:** a small set of sentinels checked with errors.Is, plus an opaque
*edge.InvocationError carrying bounded diagnostic fields. Do not require parsing
Error() text and do not expose decoder or SDK implementation errors.

| Proposed sentinel | Meaning |
| --- | --- |
| ErrInvalidInvocation | Invalid direct call, such as nil context or unusable Adapter |
| ErrInvalidEvent | Malformed supported event or failed request conversion |
| ErrUnsupportedEvent | Unsupported event family, version, or streaming product boundary |
| ErrIdentity | Failure in opted-in identity production or invocation identity isolation |
| ErrResponse | Invalid or unrepresentable HTTP response, including encoding failure |
| ErrLimitExceeded | Adapter-enforced response/header/stream-metadata resource limit |
| ErrCleanup | Request-resource cleanup failed |
| ErrStream | Stream lifecycle failure or recovered late producer/reporter panic |

Use identity.ErrInvalidCaller and identity.ErrConflict as safe subordinate causes
of ErrIdentity. An unsupported custom producer can additionally match
errors.ErrUnsupported. Do not export one sentinel for every missing event field
or invalid header; those are diagnostics within a category.

Proposed diagnostic shape:

```go
type InvocationError struct { /* private sanitized fields and cause */ }
func (e *InvocationError) Error() string
func (e *InvocationError) Unwrap() error
func (e *InvocationError) Operation() string
func (e *InvocationError) Limit() (name string, maximum int64, ok bool)
```

Operation uses documented bounded labels: validate, decode, request, identity,
response, encode, cleanup, stream. Limit is present only for a limit failure and
reports its configured maximum and a bounded name such as response_headers,
buffered_body, buffered_envelope, or stream_metadata. Do not add arbitrary field
paths, credential values, request snapshots, or caller identifiers.

The private cause can join a category and explicitly allowed subordinate errors;
Unwrap exposes only that safe tree. Avoid custom Is/As machinery where ordinary
wrapping suffices. Use errors.AsType[*edge.InvocationError] on Go 1.27 for details.
Do not add a generic Result[T], a second numeric error-code registry, or error
methods that prescribe HTTP status or automatic retries.

Constructor/configuration errors remain ordinary startup errors, outside
InvocationError. Existing ActionHeader sentinels stay separate because header
selection is an application concern. Failure to call a constructor is an
ErrInvalidInvocation when someone directly invokes an unusable adapter.

**Tradeoff:** eight broad categories commit less implementation detail than the
current private error list while distinguishing the operational decisions a
caller can reasonably make. Stable limit metadata avoids three more budget
sentinels and distinguishes an adjustable header budget from a payload ceiling.

## E3. Preserve useful standard errors; sanitize the entire chain

**Recommend:** preserve errors.Is for context.Canceled, context.DeadlineExceeded,
http.ErrNotSupported, and http.ErrContentLength where they are the actual cause.
Explicitly unsupported HTTP features also remain in ErrResponse when they are
invocation-fatal. A handler probing an absent ResponseController capability and
handling ErrNotSupported does not, by itself, poison the invocation.

Keep http.ErrBodyNotAllowed on disallowed Write calls as accepted; it is not an
invocation failure if the response otherwise completes. Keep io.EOF as successful
stream termination. Private pipe teardown errors must not replace the real
cancellation or producer failure.

Do not wrap raw JSON/base64/parser errors, custom mapper errors, OS cleanup paths,
or recovered panic payloads into public invocation errors. Formatting an unsafe
cause with %v still leaks its text; losing Unwrap alone is not sanitization.
Construct safe diagnostics at the originating boundary, not by parsing existing
private error strings at the public entry point.

Do not expose arbitrary context.Cause(parent) automatically: it can contain
application secrets or private error types. Preserve the standard parent.Err()
classification instead. Callers still control their original context.

The sanitized-error promise does not apply to an application panic deliberately
re-propagated to the SDK under the already accepted early-panic contract. Edge
does not promise to sanitize SDK/application logging or recover arbitrary
application panics into ordinary invocation errors.

## E4. Primary failure, cleanup, and cancellation

**Recommend preserving current precedence:**

1. Return the operation's classified error first.
2. Join any separately classified cleanup failure after it.
3. Use parent.Err() if no operation/cleanup failure exists.
4. Normal internal child cancellation after successful work is not an error.

Keep joined faults individually inspectable. errors.AsType sees the primary
InvocationError first; errors.Is can detect secondary ErrCleanup. Error text and
join formatting are diagnostic, not a stability promise. If there is a primary
failure plus concurrent parent cancellation, do not replace that failure with
the cancellation; callers can inspect their context separately.

Any nonnil buffered invocation error produces nil raw bytes or a zero typed
response. No caller can use a partially encoded result as success. Cleanup
failure alone still fails the invocation, as already implemented and accepted.

**Alternative:** logging cleanup faults and returning success hides resource
leaks in a reused Lambda process. Always joining cancellation would blur whether
it was the cause or merely concurrent shutdown.

## E5. The same vocabulary across raw, typed, and streaming calls

**Recommend:** classify where shared logic detects failure. Raw decoding and
final envelope encoding add their own operations; typed calls retain shared
semantic classifications without a JSON round trip. Never promise a typed call
detects duplicate keys, lost numbers, or the final size of an envelope encoded
later by the SDK. SDK-originated failures outside edge cannot be InvocationError.

Before a streaming handoff, return nil reader and the classified error. After
handoff, expose the classified terminal error through the reader and the already
approved reporter; status/body cannot be replaced. Preserve the distinction
between an ordinary returned error and a late stream failure in documentation
and metrics. Do not claim AWS treats them as the same invocation-error metric.

Recovered late producer panic matches ErrStream. A reporter panic adds another
sanitized ErrStream diagnostic without replacing the original error, exposing
the panic value, or recursively calling the reporter. Early producer panics
still re-panic on the entry goroutine after cleanup. A future streaming writer
must use this same classification at its response and limit fault sites.

No new buffering, SDK patch, or custom runtime is needed for this error contract.
The documented Runtime API header discrepancy remains a separate streaming
release gate from decision 0009.

## Implementation and acceptance

- Introduce public categories/type and classify existing private failure sites.
  Keep writer-local standard errors compatible with their accepted contracts.
- Use safe internal constructors for diagnostics; do not accept arbitrary
  untrusted cause strings as diagnostic fields.
- Test errors.Is/AsType after outer wrapping and joined cleanup faults, sanitized
  error trees, correct result discard, cancellation precedence, and limits.
- Test malformed event versus malformed application body, 401/403/500 responses,
  invalid gateway assertions, default-off authorizer handling, and SDK registration.
- Include raw/typed parity only where the input retains the required information.
- Extend existing stream tests for classified early/late/reporter errors when
  wiring streaming; no need to finish streaming before buffered invocation.

## Resolution

Awaiting user review. This is a documentation proposal; no runtime behavior has
changed.
