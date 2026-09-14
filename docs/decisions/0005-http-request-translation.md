# 0005: Translating gateway requests into net/http

Status: proposed; awaiting user review.

## Question

What should the HTTP handler observe across raw and typed API Gateway inputs,
including information that API Gateway has already decoded, combined, or lost?

## Evidence

- AWS REST events contain decoded parameter maps, and AWS does not preserve
  request-parameter ordering. Its documented header merge rule deduplicates a
  key/value present in both single and multivalue maps, rather than discarding
  all single-value entries whenever any multivalue entries exist.
  [REST proxy inputs](https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html)
- HTTP API V2 supplies rawPath and rawQueryString. Its flattened query/header
  maps combine duplicate values with commas. RawPath omits the custom-domain
  API mapping prefix. V2 also has a dedicated cookies array.
  [HTTP API payload differences](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)
- Go's URL.Path is decoded; URL.RawPath can preserve the encoded spelling.
  Incoming HTTP requests normally have no URL scheme or authority. The host is
  in Request.Host and absent from Request.Header. Body is always non-nil, and
  the server owns its cleanup. RemoteAddr has no required format, although the
  ordinary HTTP server uses IP:port. TLS describes the connection received by
  the server, not an assertion about an upstream connection.
  [Go URL](https://pkg.go.dev/net/url#URL)
  [Go Request](https://pkg.go.dev/net/http#Request)
- Go's base64 decoder can return partial data with an error. That partial data
  must not reach the HTTP handler under decision 0002.
  [DecodeString](https://pkg.go.dev/encoding/base64#Encoding.DecodeString)
- The new decoder preserves SDK transport fields and authorizer JSON separately.
  It does not yet construct HTTP requests. Beakley's decoded-path reparsing and
  wholesale single-map loss are independently reproduced requirements to avoid.

## Recommendation

### Paths and queries

- V1: assign the supplied decoded path to URL.Path directly. Generate an escaped
  request target from that value; do not parse it as a URL or unescape it again.
  Literal question marks, hashes, and percent signs remain path data.
- V2: decode rawPath once into URL.Path and retain its encoded form in RawPath
  when valid. Reject invalid path escapes and raw path delimiters that would
  change the request target. Do not substitute requestContext.http.path.
- Require an absolute path beginning with a slash. Do not clean dot segments,
  collapse repeated slashes, strip a stage, or invent a custom-domain prefix.
  ServeMux retains responsibility for its own redirect/routing behavior.
- V1 query names are case-sensitive. Preserve multivalue occurrences and their
  order, then append a single-map value only when that exact value is absent
  from the multivalue list for the same key. Include keys found only in the
  single map. This applies AWS's header merge principle consistently to queries;
  handling contradictory synthetic query maps is an edge policy, not an AWS
  guarantee. Encode through url.Values.Encode; original encoding/key order is
  unavailable and cannot be reconstructed.
- V2: use rawQueryString unchanged, including repeated parameters and encoding.
  Never reconstruct it from the comma-flattened map or split values on commas.
  Leave query parsing and its errors to the handler's ordinary net/url APIs.
- Populate RequestURI from the escaped path and query. V1 necessarily receives
  a reconstructed target. A lost empty trailing question mark or API mapping
  prefix cannot be recovered. Never populate Fragment, User, or Opaque.
- Leave URL.Scheme and URL.Host empty, as for an ordinary origin-form server
  request. Do not derive them from forwarding headers.

### Headers, cookies, and host

- Canonicalize header names with net/http's rules and validate HTTP field names
  and values. Reject invalid field names and prohibited control bytes. Preserve
  ordinary value text and commas without generic splitting or trimming.
- V1: merge by case-insensitive name and exact value as described for queries.
  Preserve repeated values already in the multivalue list. Across differently
  cased map keys, use sorted source keys for deterministic traversal because
  original cross-key order is unavailable. Do not discard unrelated single-map
  values. This also specifies deterministic handling of caller-created structs.
- V2: retain each flattened header value as supplied. If cookies is nonempty,
  it supplies the Cookie header, joined with "; "; otherwise retain an existing
  Cookie header. Dedicated cookies take precedence to avoid duplicate cookies
  from contradictory typed/synthetic inputs. Do not comma-split cookie values.
- Move the selected Host header into Request.Host and remove it from Header.
  Reject multiple selected host values or malformed authority syntax. If there
  is no Host header, use requestContext.domainName; if both are missing, leave
  Host empty. Do not trust X-Forwarded-Host to change the target host.
- Leave gateway pathParameters and routeKey separate from Go ServeMux state.
  Do not synthesize Request.Pattern or populate PathValue from an unrelated
  gateway route. A future metadata accessor remains a separate API decision.

### Method, body, and invocation metadata

- Preserve method casing; validate it as an HTTP token. No empty-method-to-GET
  fallback. Unsupported application methods remain the handler's concern.
- Decode a base64 body with the standard padded encoding when flagged. Return
  a conversion error on malformed base64 and discard all partial decoded data.
  Otherwise use the supplied string bytes without parsing application JSON,
  decompressing content, or applying a second text encoding.
- Set ContentLength to the actual decoded byte length. Reconcile a supplied
  Content-Length header to that length, and remove Transfer-Encoding and Trailer
  declarations because the Lambda envelope has already framed the complete body
  and supplies no streaming trailers. Leave TransferEncoding and Trailer empty.
  These are transport normalization choices for review, not claims of original
  client framing fidelity.
- Supply http.NoBody for an empty body and a fresh readable body otherwise.
  Own request headers, query value slices, and body state; do not mutate or alias
  mutable request data supplied by typed callers. Application middleware can
  change the request without changing the source event.
- Use the invocation context as the parent of a request context, and cancel the
  child after ServeHTTP returns. Close the adapter-created body and clean up
  multipart temporary files created through standard request parsing. The
  gateway does not provide ordinary client-disconnect notification through this
  context. Do not invent such a signal.
- Preserve the reported HTTP protocol and parsed major/minor values. Default
  an absent value to HTTP/1.1 as an explicitly documented library fallback;
  reject a present malformed protocol. Do not infer protocol from headers.
- Set RemoteAddr to requestContext's source IP as supplied (empty when absent).
  Do not add an invented client port or replace it with X-Forwarded-For. This
  differs from the usual IP:port form but fits Go's documented field contract.
- Leave TLS nil: Lambda has no client TLS connection to expose. Do not create a
  fabricated tls.ConnectionState from gateway metadata. Any later mTLS identity
  support needs an explicit gateway assertion contract.

### Shared behavior and remaining decisions

Raw and typed entry points use the same conversion functions after their
respective validated input boundaries. Conversion failures use decision 0002's
error policy without credential-bearing details. Gateway identity remains the
separate opt-in producer from decision 0001.

Response commitment, binary output selection, response limits, panic handling,
and optional ResponseWriter capabilities remain a separate response contract.
Gateway metadata accessors, identity interpretation, and cancellation's effect on
the final invocation result remain separate reviews. Passing cancellation to the
handler does not by itself settle whether to discard a completed response.

## Alternatives and consequences

- Parsing a V1 path as a complete URL changes literal path delimiters and can
  route to a different resource. Assigning URL.Path avoids that bug.
- Using V2's flattened query map loses distinctions between repeats and literal
  commas. RawQueryString preserves the representation AWS still provides.
- Multivalue replacement per whole map loses unrelated fields; replacement per
  key also discards a differing single value. The documented key/value merge
  rule preserves all supplied values without duplicating the single-map echo.
- Synthesizing https/TLS metadata may help existing redirect middleware, but
  creates an inaccurate connection object. Document the server-request shape
  and review explicit gateway metadata separately.
- Requiring every context metadata field would reject otherwise usable typed
  requests. Defaults above are explicit and limited to fields unnecessary for
  unambiguous route selection.

## Validation plan

Compare relevant results with Go's HTTP request parsing and a real local HTTP
server, plus independent AWS fixtures. Cover V1 literal delimiters, V2 encoded
slashes and double escaping, Unicode, repeated slashes, malformed path escapes,
query repeats/commas, all header merge cases, cookie precedence, Host promotion,
invalid header bytes, body bytes/base64 errors, protocol handling, source address,
context cancellation, body/multipart cleanup, and caller-owned data isolation.
Exercise the same HTTP assertions through raw and typed entry points when the
response contract makes those public entry points implementable.

## Resolution

Pending review. No HTTP request translation is implemented under this proposal.
