# 0006: Buffered HTTP responses and invocation completion

Status: accepted with the 2026-09-14 review refinements; implementation underway.

Reviewed against the implemented decoder/request layer on 2026-09-14. The
[plan review](../reviews/2026-09-14-plan-review.md) was approved by the user and its
refinements are incorporated below. Decision 0010 now settles the exact metadata
budget and V2 joinable-field set; their private shared layer is implemented.

## Question

How should the adapter capture an HTTP response, encode it for each gateway
format, and handle features or failures that a buffered invocation cannot carry?

## Evidence

- Go's ResponseWriter commits a final status and headers once, supplies an
  implicit 200 on Write, and supports automatic content-type detection. The
  ordinary server handles informational responses, trailers, and connection
  capabilities that are not represented by a buffered gateway response.
  [ResponseWriter](https://pkg.go.dev/net/http#ResponseWriter)
  [DetectContentType](https://pkg.go.dev/net/http#DetectContentType)
  [ResponseController](https://pkg.go.dev/net/http#ResponseController)
- AWS V1 responses support multivalue headers. V2 has single-string headers and
  a cookies array whose entries become separate Set-Cookie fields. Explicit
  statusCode avoids V2's inferred-response behavior.
  [REST response format](https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html)
  [HTTP API response formats](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)
- AWS requires base64 encoding for binary REST proxy responses and matching
  binary media configuration. The first Accept value affects REST binary
  handling; AWS recommends */* when clients such as browsers control that order.
  [Binary responses](https://docs.aws.amazon.com/apigateway/latest/developerguide/lambda-proxy-binary-media.html)
- Lambda buffered responses have a 6 MB maximum, regardless of API Gateway's
  larger service limit. JSON quoting, headers, cookies, and base64 consume that
  payload budget. Streaming is a separate invocation contract.
  [Lambda buffered/streaming limits](https://docs.aws.amazon.com/lambda/latest/dg/configuration-response-streaming.html)
- The inspected Go 1.27.1 server rejects writes to 204/304 with ErrBodyNotAllowed,
  omits HEAD bodies, and validates declared content length. Its cleanup removes
  multipart files reachable from the request it served. WithContext makes a
  request copy; forms parsed later on that copy are not visible to the original.
  [Go server source](https://go.dev/src/net/http/server.go)
  [WithContext](https://pkg.go.dev/net/http#Request.WithContext)
- Our decoder and request conversion are independent of response capture.
  serveHTTP already cancels the request child context and cleans up on both
  return and panic. It reports cleanup errors without file paths or input values.

## Recommendation

### Writer commitment and ordinary responses

- Implement a private buffered ResponseWriter shared by all entry points.
  First final WriteHeader commits the status and a deep header snapshot; later
  calls and header mutations cannot rewrite the committed response. Write commits
  200 when needed, and a handler that writes nothing returns 200 with an empty body.
- Accept final statuses 200 through 599. An invalid first status records an
  adapter fault; WriteHeader cannot return an error, so completion must report
  the fault. Do not log it or synthesize a different application response.
- Detect an absent Content-Type from up to the first 512 buffered body bytes,
  unless Content-Encoding is set. An explicitly present nil/empty header slice
  suppresses automatic content type, following net/http's header convention.
  Empty bodies do not acquire a sniffed type. Do not generate Date or connection
  management headers inside edge; the gateway owns the network response.
- Validate committed header names/values and require encodable UTF-8 header
  strings so raw and typed paths cannot disagree through replacement encoding.
  Canonicalize header names and retain all values; use deterministic traversal
  for differently cased keys supplied by direct Header map assignment.
- Derive Content-Length from the buffered bytes when absent. If explicitly
  present with a nil/empty slice, suppress inference. Otherwise require one valid
  nonnegative decimal length and enforce it: an
  overflowing Write returns http.ErrContentLength; a mismatch at completion is
  an invocation error. Transport faults remain faults even if the handler ignores
  a Write error. Ordinary application 4xx/5xx responses still return nil Go error.
- For HEAD, accept/count writes and retain at most the sniffing prefix, but emit
  no body. Capture the original request method before application code runs.
  Honor a supplied representation length; otherwise infer a length only
  when the handler actually wrote representation bytes. Do not require HEAD's
  declared length to match an omitted representation body.
- For 204/205/304, emit no body and return http.ErrBodyNotAllowed from a nonempty
  Write. A zero-length Write commits an implicit response and returns (0, nil).
  That
  error alone does not invalidate the committed bodyless response. Suppress
  Content-Length for these statuses and Content-Type for 304. Bodyless 205 follows
  RFC 9110 and intentionally differs from Go 1.27.1's body-allowed helper.
  [205 semantics](https://www.rfc-editor.org/rfc/rfc9110.html#section-15.3.6)

### Capabilities the buffered adapter cannot provide

- Do not implement Flusher, Hijacker, Pusher, CloseNotifier, full-duplex, or
  connection-deadline methods. ResponseController capability requests naturally
  return an error matching http.ErrNotSupported. Request.Context is the lifecycle
  signal; it does not imply a client-disconnect signal.
- Treat explicit 1xx writes, including 101 upgrades, as unsupported invocation
  faults. A buffered proxy response cannot convey their separate timing. This
  is an intentional compatibility limit, rather than pretending they were sent.
- Reject declared trailers and TrailerPrefix values. Reject upgrades. Remove
  connection-specific response headers and fields nominated by Connection;
  never claim a chunked transfer encoding for an already buffered payload.

### Gateway response encoding

- Return AWS response structs from typed methods. Invoke serializes the selected
  response with encoding/json/v2 directly. Do not JSON-round-trip typed events
  or responses. Always set an explicit final status and body representation.
- V1 uses MultiValueHeaders for every response header, including each Set-Cookie.
  Leave the redundant single-value Headers map empty.
- V2 moves Set-Cookie values into Cookies. Join repeated values with a comma and
  space only for a documented, audited set of list-valued fields. Reject repeated
  singleton or unknown fields. Preserve order and commas inside individual
  values. Leave MultiValueHeaders empty. Decision 0010 specifies the initial list;
  V2 cannot retain general header line boundaries, so applications requiring
  them must choose V1.
- Emit text directly only when bytes are valid UTF-8, Content-Encoding is absent
  or identity, and the media type is textual: text/*, application/json,
  application/xml, application/javascript, application/x-www-form-urlencoded,
  or a +json/+xml suffix. Otherwise base64 encode the bytes and set
  IsBase64Encoded. An empty emitted body is not base64 encoded. Missing/invalid
  media type is conservatively binary if it was not resolved by sniffing.
- Binary selection changes only the envelope representation, not the application
  Content-Type or byte sequence. REST deployments must configure binary media
  types; document the browser Accept caveat in the runnable Lambda example.

### Bounds and completion

- Initially cap stored response body bytes at 6,291,456 (6 MiB). AWS explicitly
  defines its documented MB unit as 1,024 KB in the
  [Lambda quotas](https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html).
  This corrects the draft's earlier 6,000,000-byte interpretation. Reject a Write
  crossing the cap without retaining a partial write, and record a sticky fault.
  Check base64 expansion before allocating the encoded body. No new public
  size-tuning option is proposed for the first implementation.
- Invoke uses json/v2.MarshalWrite with a bounded destination to enforce
  6,291,456 bytes including JSON overhead while retaining encoded output.
  A body below the cap can still fail this check. Preflight committed metadata
  using decision 0010's weighted resource charge before copying.
  A typed method cannot measure the exact envelope produced by its caller's
  serializer without violating the agreed JSON-free typed boundary; document
  that the caller/runtime owns that final limit. Apply the common body/expansion
  caps on both paths. Header collections are caller-controlled; a body cap is
  not a total-process-memory guarantee.
- Propagate handler panics to the Lambda runtime after cleanup; do not convert
  them into successful HTTP 500 responses. Direct Go callers retain normal panic
  behavior. Do not add logging to the adapter.
- One invocation scope covers conversion, identity, serving, finalization, and
  cleanup. Reject an already-canceled parent before running application code.
  Before returning success, check the invocation parent context. If it is
  canceled, return its error and no usable response. Check the parent, not the
  child context canceled during normal cleanup. This deliberately
  makes cancellation win over a just-completed HTTP response when already known.
- Cleanup failures are invocation failures even after a handler produced a
  response. Keep diagnostics free of file paths, bodies, tokens, and header values.
  Preserve a primary operation error and join sanitized cleanup failures. Parent
  cancellation replaces only an otherwise successful result. Preserve errors.Is
  for context and standard HTTP errors; review exported adapter error categories
  separately before publishing the invocation methods.

### Multipart cleanup limit discovered during implementation

The request layer removes multipart files attached to the served request. Like
Go's server, it cannot discover a form first parsed on an independently copied
request. Recommend documenting that middleware which makes such a copy owns
cleanup of forms it creates there. Do not preparse multipart bodies: that would
consume bodies before handlers choose parsing limits and would change streaming
read behavior. This limitation is surfaced for review; there is no hidden global
temporary-directory scan or eager parsing workaround in the implementation.

## Alternatives and consequences

- httptest.ResponseRecorder is a useful test reference but advertises Flush and
  does not enforce every production server behavior. Do not expose it as our
  production writer merely to avoid implementing the approved contract.
- Silently dropping informational responses or trailers would let applications
  believe unsupported output reached clients. Explicit faults are inspectable.
- Base64 for every response is simpler but adds size overhead and REST deployment
  requirements even for ordinary JSON/text responses. UTF-8 alone is insufficient
  to decide whether a declared binary representation should be encoded.
- Returning HTTP 500 for all faults obscures the Lambda invocation failure signal
  and contradicts decision 0002. Application-written 500 responses remain valid.
- Typed callers retain their no-JSON boundary, at the cost of owning final
  serialization limits. The library cannot promise exact codec parity with an
  external runtime whose formatting options the caller controls.

## Validation plan

Use a real net/http server for commitment, sniffing, HEAD, 204/304, and length
comparisons. Exercise multiple Write calls, post-commit mutation, ignored writer
errors, status/field validation, size boundaries, binary/text round trips, v1
multivalue headers, v2 cookies, unsupported capability checks, panic cleanup,
parent cancellation, and cleanup failure propagation. Verify raw registration
through lambda.NewHandler(adapter).Invoke and typed method registration through
the SDK wrapper once the remaining gateway-identity path permits public wiring.
Add explicit 205 divergence, zero-length writes, suppressed inferred length,
exact envelope boundaries, metadata bounds, and sequential/concurrent isolation.

## Resolution

Accepted by the user on 2026-09-14 with the plan-review enhancements. Implement
settled behavior now; resolve the separately identified metadata, header-list,
error API, and identity details before their dependent implementation.
