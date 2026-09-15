# 0010: Shared response headers and transport-specific encoding

Status: accepted by the user on 2026-09-15, including the measured 256 KiB
default and explicit override up to 6 MiB. Shared snapshots, projections and
streaming-prefix encoding are implemented and connected to the private buffered
HTTP writer. Streaming HTTP writer wiring follows.

Terminology: V1/V2 in this record mean API Gateway payload formats 1.0/2.0,
not versions of this Go module. The field-combination table concerns outgoing
responses only. It does not restrict custom incoming request headers.
The user's action-dispatch requirement is recorded separately in
[decision 0011](0011-request-header-processing.md).

## Recommendation

Keep the application boundary as http.Header. Capture one private, owned,
canonical multivalue snapshot at HTTP commitment. Validate/filter it using shared
rules, then encode it for the selected gateway response format. Only V2 needs
comma combination; REST streaming shares V1's multivalue representation.

Replace decision 0007's initial ten-field audit with the broader table below.
Replace its unaccepted 64 KiB/1,024-entry pair with a single weighted resource
budget with a measured default and explicit override. Keep actual envelope and
stream-prefix checks separate. These are accepted refinements, not changes to
the already implemented bridge.

## Evidence and limits of the evidence

AWS public MCP was used to read the complete current gateway response-format,
streaming-integration, REST-quota, and HTTP-API-quota pages. The sources establish:

| Boundary | What the documentation establishes |
| --- | --- |
| REST / HTTP API payload 1.0 | MultiValueHeaders can carry single and repeated values |
| HTTP API payload 2.0 | Headers contains one string per name; Cookies carries separate Set-Cookie values |
| REST streaming | Metadata supports MultiValueHeaders and Cookies; all extra headers can use MultiValueHeaders |
| Streaming framing | JSON followed by eight NUL bytes; delimiter must appear within the first 16 KB |
| REST quotas | Current table lists 20,480 bytes for combined header names, values, whitespace and terminators; private APIs have an 8,000-byte entry |
| HTTP API quotas | The 10,240-byte entry explicitly covers the request line and headers; it does not establish an equivalent response limit |

Sources: [HTTP API formats](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html),
[streaming format](https://docs.aws.amazon.com/apigateway/latest/developerguide/response-transfer-mode-lambda.html),
[REST quotas](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-execution-service-limits-table.html),
[HTTP API quotas](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-quotas.html).

These are not interchangeable byte counts. Edge does not know every deployment
setting or the final headers added by Gateway/CDN layers. Passing edge's checks
cannot guarantee gateway acceptance. Do not turn the HTTP API request quota into
a response limit, infer private/public endpoint configuration from payload V1,
or promise an exact final HTTP wire size from a Lambda JSON envelope.

HTTP allows comma combination when the field definition supports it, with
Set-Cookie requiring special handling. The IANA registry provides useful type
and specification references, but registration alone is not permission to join.
Structured Fields Lists and Dictionaries support combination; Items do not
become lists merely because their names are registered.
[RFC 9110 sections 5.2–5.5](https://www.rfc-editor.org/rfc/rfc9110.html#section-5.2),
[IANA field registry](https://www.iana.org/assignments/http-fields/),
[RFC 9651 section 4.2](https://www.rfc-editor.org/rfc/rfc9651.html#section-4.2).

Go 1.27's new DefaultMaxHeaderValueCount is 500, but it is a request-parser limit.
It is useful evidence that value count matters, not authority for applying 500
to edge responses. The installed Go 1.27.1 ResponseWriter documentation confirms
header commitment and nil-value suppression. Our request helpers already have
token/control-character checks, but their copying is not resource-bounded and
must not be reused wholesale for response snapshots.
[Go ResponseWriter](https://pkg.go.dev/net/http#ResponseWriter),
[Go request value-count limit](https://pkg.go.dev/net/http#DefaultMaxHeaderValueCount).

## Shared commitment pipeline

1. Preflight the application map's resource charge without allocating a sorted
   key slice or cloning values. Reject over-budget input before snapshot growth.
2. Recognize Go's TrailerPrefix as an unsupported feature before generic name
   validation. Reject invalid token names, invalid UTF-8, CR/LF, NUL, DEL and other control
   bytes except HTAB. Reuse small syntax predicates where correct; response
   UTF-8 validation remains explicit. Reject invalid data rather than silently
   removing it or replacing bytes during JSON encoding.
3. Sort original keys lexically, canonicalize names, concatenate same-name
   slices in that deterministic key order, and preserve slice element order.
   This keeps the accepted mixed-case policy. A Go map cannot recover insertion
   order across differently spelled keys; recommend Header.Add/Set to callers.
4. Normalize only leading/trailing SP and HTAB in values. Preserve interior
   whitespace, quoting, commas and parameters. Do not split values on commas,
   deduplicate values, rewrite CSP directives, or parse arbitrary field grammars.
5. Preserve the distinction between absence and an explicitly present nil/empty
   slice until automatic-header decisions finish. Empty slices emit no field;
   a slice containing an empty string represents an actual empty field value.
   If differently cased aliases include actual values, those values win over a
   nil alias: a suppression marker cannot erase explicit content.
6. Detect unsupported upgrades/trailer declarations before hop-header removal.
   Parse Connection's comma-separated tokens case-insensitively; malformed
   tokens are faults. Remove Connection-nominated fields and the fixed
   connection-specific set listed below. Preserve removal/suppression state so
   automatic Content-Type/Content-Length cannot reintroduce a nominated field.
7. Apply the accepted status/method, Content-Type and Content-Length rules to
   the resulting snapshot. Buffering can infer length at completion; streaming
   cannot infer an unpublished final body length after handoff. Generated fields
   must fit the same resource budget and the applicable encoded-output limit.
8. Encode using the transport-specific rules below. Pre-handoff failures remain
   invocation errors; no partial headers are published. Unsupported trailers
   attempted after commitment remain faults under the accepted late-error policy.

The fixed removal set follows the categories also handled by Go's ReverseProxy:
Connection, Proxy-Connection, Keep-Alive, Proxy-Authenticate,
Proxy-Authorization, TE, Trailer, Transfer-Encoding and Upgrade. Upgrade/trailer
attempts first produce the accepted unsupported-feature fault; removal must not
hide that fault. This is application-response handling, separate from the Lambda
SDK's own transport headers and error trailers.
[Go ReverseProxy source](https://go.dev/src/net/http/httputil/reverseproxy.go),
[Connection semantics](https://www.rfc-editor.org/rfc/rfc9110.html#section-7.6.1).

Keep the shared layer concerned with syntax, ownership, transport controls and
representation. Beyond the already accepted Content-Length rules, do not expand
this milestone into a universal semantic validator for every HTTP header.
Applications remain responsible for field-specific validity in every format.

## Format-specific rules

| Output | Ordinary fields | Set-Cookie |
| --- | --- | --- |
| Buffered V1, including HTTP API payload 1.0 | MultiValueHeaders; no redundant Headers map | Separate elements in MultiValueHeaders |
| Buffered V2 | One value unchanged; multiple values combined only for the audited set | Move values into Cookies; never comma-combine |
| REST streaming | MultiValueHeaders in JSON v2 metadata; no redundant Headers map | Separate elements in MultiValueHeaders; omit redundant Cookies |

Preserve the accepted V1 treatment of unknown repeated fields. V2 rejects a
repeated field outside the audited set, even if its values happen to be equal.
Do not silently use the first/last value or deduplicate to avoid an error.
Examples of V2 rejections include Content-Type, Location, ETag, Date and
Access-Control-Allow-Origin. Content-Length retains its stricter shared rules.

For a known combinable field, concatenate complete values with comma-space.
This is a representation conversion, not validation that each value is legal for
that field. In particular, registration does not repair malformed list syntax,
empty list elements, invalid dictionary members, or invalid singleton-alternative
combinations. Original within-slice order is preserved, including for ordered
fallbacks, challenges and signature dictionaries.

### Accepted initial V2 combination table

| Fields | Definition supporting combination |
| --- | --- |
| Accept-Ranges, Allow, Content-Encoding, Content-Language, Vary, WWW-Authenticate | [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110.html) list grammars |
| Cache-Control | [RFC 9111 section 5.2](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2) |
| Access-Control-Allow-Headers, Access-Control-Allow-Methods, Access-Control-Expose-Headers | [Fetch CORS grammars](https://fetch.spec.whatwg.org/#http-new-header-syntax) |
| Link | [RFC 8288 section 3](https://www.rfc-editor.org/rfc/rfc8288.html#section-3) |
| Content-Security-Policy, Content-Security-Policy-Report-Only | [CSP sections 3.1–3.2](https://www.w3.org/TR/CSP3/#csp-header) |
| Referrer-Policy | [Referrer Policy section 4.1](https://www.w3.org/TR/referrer-policy/#referrer-policy-header) |
| Server-Timing | [Server Timing section 3](https://www.w3.org/TR/server-timing/#the-server-timing-header-field) |
| Accept-Patch | [RFC 5789 section 3.1](https://www.rfc-editor.org/rfc/rfc5789.html#section-3.1) |
| Accept-CH | [RFC 8942 section 3.1](https://www.rfc-editor.org/rfc/rfc8942.html#section-3.1) structured List |
| Cache-Status, Proxy-Status | [RFC 9211](https://www.rfc-editor.org/rfc/rfc9211.html#section-2), [RFC 9209](https://www.rfc-editor.org/rfc/rfc9209.html#section-2) structured Lists |
| Content-Digest, Repr-Digest, Want-Content-Digest, Want-Repr-Digest | [RFC 9530 sections 2–4](https://www.rfc-editor.org/rfc/rfc9530.html#section-2) structured Dictionaries |
| Signature, Signature-Input | [RFC 9421 sections 4.1–4.2](https://www.rfc-editor.org/rfc/rfc9421.html#section-4.1) structured Dictionaries |

This is an audited starting set of 25 fields, not an exhaustive HTTP registry or
an AWS allowlist. Signature-field transport does not imply signature verification
or a guarantee that Gateway transformations preserve signed message components.

Do not add a public joining callback or mutable global registry initially.
Applications with another list-defined extension can explicitly construct one
valid combined value using ordinary Header.Set, or use V1 when separate values
are required. Add fields to the built-in set with a specification reference and
fixtures when needed. Revisit a per-adapter extension option only if middleware
integration demonstrates that the ordinary HTTP boundary is insufficient.

## Resource accounting and encoded limits

Recommend a single preflight charge based on the HTTP/2 field-section accounting
model: for each original value, charge len(name) + len(value) + 32 bytes.
Charge a nil/empty slice as one name-plus-32 entry. Count original bytes before
trimming, merging or dropping fields, including cookies and hop headers.
Use overflow-safe incremental subtraction from the remaining budget.
[RFC 9113 section 6.5.2](https://www.rfc-editor.org/rfc/rfc9113.html#section-6.5.2).

The 32-byte per-line term is established HTTP accounting reused as an edge
resource model. It is not an AWS quota or an exact measurement of Go heap usage.
Counting suppressed entries is an additional edge policy that bounds copying
and sorting even when map values are empty. It also avoids an independent,
arbitrary value-count limit.

Following the allocation probe, recommend a **262,144-byte (256 KiB) default**
resource budget on both buffered and streaming paths, with an explicit
WithResponseHeaderBudget(bytes int) Option for applications that need another
budget. Recommend accepting positive values up to the existing 6,291,456-byte
response ceiling, rejecting invalid configuration in New/NewStreaming. Omission
uses the default; an explicit zero does not disable the bound. This follows the
existing options' last-assignment-wins rule and fixed constructor configuration.
The numeric default and option are accepted API/library policy, not AWS quotas.

The earlier proposal used 6 MiB unconditionally. A prototype with 157,286 distinct
names near that weighted ceiling retained approximately 14.41 MiB for its
snapshot and cumulatively allocated 16.82 MiB per operation, even after
preallocating sorting storage. At a 256 KiB fixture charge, 6,553 distinct names
retained approximately 0.48 MiB and cumulatively allocated 0.58 MiB. A 1 MiB
candidate retained approximately 1.90 MiB. These are measured shapes, not
worst-case proofs. See [the allocation report](../benchmarks.md#response-header-allocation-probe).

The default aims to keep additional metadata storage modest while leaving an
explicit route for applications with unusual headers. The original 6 MiB remains
the largest opt-in budget; it is no longer the default memory tradeoff for every
application. The 256 KiB choice is an engineering judgment informed by these
measurements, not a uniquely correct threshold derived from AWS documentation.
An override does not change the independent response-envelope or streaming-prefix
limits, and does not guarantee acceptance by API Gateway.

At the default, every valid key costs at least 33 bytes per charged entry,
allowing at most 7,943 entries; the 6 MiB override permits at most 190,650.
Counting only name/value bytes would admit arbitrarily large slices of empty
strings; the per-entry charge prevents this without a second count setting.

This is a library policy. A response below an AWS envelope limit can
still exceed the weighted budget. A passing weighted budget does not promise
that a response fits Lambda JSON, API Gateway, a browser, or a low-memory Lambda
configuration. Map overhead, backing strings, encoding temporaries and the
application's existing allocations are not an exact 6 MiB heap guarantee.

The isolated, test-only internal/headerprobe package now measures ordinary
headers/cookies, many empty values, many distinct names, suppressed names,
mixed-case collisions, escaped values and rejection before copying. It does not
implement production header validation, filtering or projection. Retained heap
deltas keep the input alive and exclude application allocations; neither these
deltas nor cumulative allocations measure peak memory or RSS. Repeat the probes
against the actual implementation before treating these figures as its baseline.

Independently enforce the existing complete buffered JSON envelope limit on raw
Invoke. Typed buffered methods still perform no response JSON round trip and
leave the caller's final serialized-envelope limit with that caller/runtime.

For streaming, recommend a conservative initial limit of **16,000 bytes for the
entire encoded JSON prefix plus all eight delimiter bytes**, checked by bounded
direct JSON v2 encoding before publish. That leaves at most 15,992 JSON bytes,
including status, names, array syntax and escaping. The guide does not explicitly
resolve decimal/binary KB or delimiter-start versus delimiter-end accounting.
Keeping the complete delimiter within 16,000 bytes satisfies the stricter
interpretations; it deliberately gives up up to 384 bytes compared with a
16,384-byte interpretation. Label this as edge's conservative compatibility
policy and relax it only on authoritative clarification or approved deployment
evidence. This resolves the implementation choice without claiming AWS specified
an exact 16,000-byte service boundary.

Neither the weighted budget nor the exact streaming-prefix cap is a stream body
limit. The streaming writer retains only its accepted sniffing prefix and uses
the already implemented bridge for the body.

## Alternatives and next implementation

- Join every repeated V2 field: simplest mechanically, but can alter singleton,
  cookie and extension semantics. AWS's representation does not authorize that.
- Preserve every field in every format: impossible with V2's single-string map.
- Fixed 64 KiB plus 1,024 entries: still lacks a demonstrated resource/compatibility
  reason for those numbers. A weighted budget replaces both controls.
- Fixed 6 MiB weighted default: broad compatibility, but the prototype retains
  roughly 14.41 MiB for one high-cardinality snapshot before application and
  response-encoding allocations. Prefer a smaller default with an explicit
  override rather than assigning that tradeoff to every consumer.
- Reuse Gateway's smallest quota globally: would conflate request/response,
  endpoint configuration, HTTP field bytes and JSON framing bytes.
- Full semantic registry/parser: significantly larger scope than transport
  adaptation; unnecessary for ordinary single values and V1 preservation.

The shared layer is implemented in response_headers.go. It owns canonical maps
and value slices, tracks the original preflight charge without refunds, preserves
automatic-header suppression and returns independent V1/V2 projections. New
validates WithResponseHeaderBudget after option application, so the final scalar
assignment wins. Final status/body behavior and HTTP commitment timing belong
to the separate writers; the buffered writer now implements those rules.

Connection parsing tolerates empty list elements under RFC 9110 section 5.6.1.2;
the input budget bounds that scanning. Nonempty malformed tokens remain faults.
TrailerPrefix entries, represented Trailer/Upgrade values and upgrade nominations
are unsupported; nil/empty Trailer/Upgrade slices emit nothing and are removed.
Strict Content-Length validation follows hop-field removal. A nominated length
cannot reappear through inference. These details follow the accepted transport
and suppression rules; they add no new public policy controls.
[HTTP list recipient requirements](https://www.rfc-editor.org/rfc/rfc9110.html#section-5.6.1.2).

stream_encoding.go encodes explicit status and multivalue metadata through direct
JSON v2, using the shared bounded destination with a 15,992-byte JSON limit. It
returns exactly sized storage containing JSON plus eight NUL bytes. It never
returns a partial prefix. A bridge test verifies that oversized metadata fails
before either headers or body become available.

Tests cover input/projection ownership, casing, suppression, syntax/UTF-8,
Connection filtering, cookies, all 25 joinable names, ordered challenges and
signature dictionaries, rejected repeats, Content-Length syntax, configuration,
exact resource boundaries, generated-field accounting and exact encoded prefix
limits including escaping. Over-budget preflight rejection allocates no snapshot.
Production allocation samples are recorded in [the benchmark report](../benchmarks.md).

Validation on Go 1.27.1: full race tests, vet, formatting/diff checks and Linux
arm64/amd64 builds pass. The default's measured retained snapshot matches the
probe at approximately 0.48 MiB for the high-cardinality fixture. Projection
allocations are reported separately; no new resource policy is needed.

The buffered writer now uses these primitives for status/method rules, sniffing,
length enforcement, commitment timing and late unsupported-trailer detection.
The streaming HTTP writer remains to be implemented. Constructor options and
private primitives do
not make the unfinished adapter usable as a Lambda handler yet.

Public invocation/identity work and the SDK Runtime API header compatibility
question remain separate. No account APIs or live AWS deployments were used for
this proposal.
