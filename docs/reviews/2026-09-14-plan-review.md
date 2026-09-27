# Plan review after request translation

Status: accepted enhancements (2026-09-14).

Reviewed the plan, decisions 0001–0006, and the implemented constructor, decoder, request conversion, and tests on `work/edge-foundation`.
The user approved these enhancements with “go with your recommended enhancements.”
They refine decision 0006 and milestone sequencing, preserving accepted AWS-type reuse, JSON-free typed entry points, direct JSON v2, and opt-in gateway identity.
Exact metadata budgets, the initial V2 joinable-field set, exported errors, and identity APIs still require the concrete specifications identified below.

## 1. Make response limits precise and enforce them during encoding

AWS defines its MB unit as 1,024 KB.
The buffered response limit is therefore 6 MiB, or 6,291,456 bytes, including the serialized envelope.
The original 6,000,000-byte draft cap leaves capacity unused;
decision 0006 now records the correct documented unit.
A lower application budget could be a future option.
[AWS Lambda quotas](https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html)

Recommend using `encoding/json/v2.MarshalWrite` with a bounded destination for raw invocation output.
Stop retaining encoded output at the limit, rather than allocating an oversized complete envelope before rejecting it.
Keep body-write and base64-expansion checks.
Preflight the committed header/cookie data before deep copying it: bound retained field bytes and entry counts as well as the body.
Set the exact metadata budget in the response contract before implementation;
do not borrow an unrelated AWS request-header quota.
[MarshalWrite](https://pkg.go.dev/encoding/json/v2#MarshalWrite)

This bounds retained adapter output, not all codec temporaries or memory already allocated by an application.
Measure peak allocations near the limit.
Typed methods stay JSON-free and continue to delegate the exact final serialized envelope limit to their caller/runtime.
Test exact boundaries, escaped strings, binary expansion, large cookies, and many empty header entries.

## 2. Do not combine arbitrary repeated V2 headers

Decision 0006 currently proposes comma-joining every field except Set-Cookie.
That can change meaning: two Location values cannot safely become one URI.
HTTP field definitions determine whether comma-separated lists are supported;
Set-Cookie requires separate treatment.
[RFC 9110 field ordering](https://www.rfc-editor.org/rfc/rfc9110.html#section-5.3) [Location](https://www.rfc-editor.org/rfc/rfc9110.html#section-10.2.2)

Recommend preserving V1 multivalue output and V2 Cookies as proposed.
For V2, join repeated values only for a documented, audited set of list-valued fields;
reject repeated singleton or unknown fields with an invocation error.
A single value passes through without splitting embedded commas.
Preserve value order.
Specify the initial set before coding it;
do not add a public policy callback without a demonstrated consumer need.

The alternative of taking the first or last value silently loses data.
Blind joining changes semantics.
Strict rejection makes V2's limitation visible;
applications can explicitly construct a field value according to its definition, or choose V1 when separate field values are required.
[AWS HTTP API response formats](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)

## 3. Specify a small response behavior matrix before writing the writer

Retain the existing snapshot, binary encoding, and unsupported-capability direction.
Add these cases to the contract and differential tests:

- A zero-length Write commits an implicit response but returns `(0, nil)`, including after 204/304.
  Only a nonempty forbidden-body write returns `http.ErrBodyNotAllowed`.
- An explicitly present nil/empty Content-Length slice suppresses inferred length, just as the draft already specifies for Content-Type.
  Distinguish this from an explicit empty string, which is an invalid length.
- Capture the original request method for HEAD response handling so a handler's mutation of its request cannot change transport finalization.
- Add 205 Reset Content.
  Recommend suppressing its body and rejecting nonempty writes with `http.ErrBodyNotAllowed`.
  RFC 9110 prohibits response content here, while the inspected Go 1.27.1 server's body-allowed helper does not exclude 205.
  Record this as an intentional standards-based difference, not Go parity.

Use the installed Go 1.27.1 server as the implementation reference and the HTTP specification to identify deliberate differences.
The draft's 200–599 status range, sticky errors, and unsupported 1xx/trailer behavior are also stricter than ordinary Go serving;
keep those differences explicit in consumer documentation.
[Go server implementation](https://go.dev/src/net/http/server.go) [Go body/status handling](https://go.dev/src/net/http/transfer.go) [205 semantics](https://www.rfc-editor.org/rfc/rfc9110.html#section-15.3.6)

## 4. Give the whole invocation one lifetime

Currently `serveHTTP` creates the child context.
The planned identity step runs before this helper, so it would not share that scope.
Recommend moving ownership to a shared invocation coordinator covering conversion, identity production, handler execution, response finalization, and cleanup.
Pass that scope through the existing conversion functions instead of adding a second handler-only scope.

Reject an already-canceled parent before invoking application code.
Continue to check the parent before returning success;
ordinary child cancellation during cleanup must not turn success into a context error.
Ensure identity failures, writer faults, and panics all reach cleanup.
Preserve panic propagation.

Define simultaneous-error behavior before implementation: retain a primary conversion/identity/response fault, join sanitized cleanup errors, and let parent cancellation replace only an otherwise successful result.
Preserve `errors.Is` for context and standard HTTP errors.
Review a small set of programmatic adapter error categories before publishing the invocation methods, so applications need not match diagnostic strings.
Exact exported names remain a separate API detail.

Retain the documented multipart limitation: a form first parsed on a separate request copy is that middleware's cleanup responsibility.
Eager parsing would change handler behavior and does not belong in transport conversion.
[Request.WithContext](https://pkg.go.dev/net/http#Request.WithContext)

## 5. Make the next deliverable a complete adapter

The private decoder and request conversion already have substantive unit, differential, race, and fuzz coverage.
The next major uncertainty is composition, not another private conversion layer in isolation.

Recommend this sequence after the response refinements are resolved:

1. Implement the buffered writer and response conversion.
2. Review and implement the minimum identity/context and native gateway-producer contracts needed to honor `WithGatewayIdentity(true)`.
   Keep the option's accepted default and do not publish a callable adapter that silently ignores an enabled option.
3. Wire Invoke, HandleV1, and HandleV2 through the common invocation coordinator.
4. Add runnable raw and typed Lambda registration examples and integration tests through the pinned SDK's `lambda.NewHandler` boundary.
5. Continue with Cognito/JWKS resilience, broader identity composition, authorization, and consumer helpers as already planned.

For the first complete adapter, test sequential reuse and concurrent invocations, raw/typed HTTP equivalence where input fidelity permits it, cancellation/cleanup, and absence of cross-invocation body, header, or identity leakage.
Exercise real handlers such as ServeMux, Redirect, ServeContent with Range, compression, and cookie round trips.
Some handlers may expose an intentional transport limit;
document that outcome instead of weakening the contract to make a test pass.

Add Go 1.27 CI for tests, race detection, vet, and Linux arm64/amd64 builds.
Add allocation benchmarks comparing raw and typed paths, plus binary and near-limit responses.
Establish measured baselines before choosing performance thresholds.
These checks complement the existing fuzzing and make a usable first release the next concrete acceptance milestone.

## Resolution

Accepted by the user on 2026-09-14.
Decision 0006 and the implementation plan incorporate the refinements.
Implementation may proceed for settled behavior;
the remaining concrete policy/API details are reviewed separately.
