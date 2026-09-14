# 0011: Consumer request-header processing and action dispatch

Status: first-class support required by the user; concrete API proposed for review.

## Requirement and terminology

Consumers will implement APIs in which a request header identifies the action
to dispatch. They need a supported interface for processing custom headers.
This is incoming request processing, separate from decision 0010's outgoing
response-header conversion. API Gateway payload format 2.0 is not module v2.

The existing request conversion preserves custom header names and values. Its
only special handling is the accepted transport normalization, such as moving
Host into Request.Host and reconciling body framing. The response-combination
table must never become an incoming-header allowlist.

## Evidence

request.go canonicalizes names and preserves repeated V1 values. API Gateway
payload 2.0 provides flattened header strings: AWS combines repeated values
with commas. Original field-line multiplicity is unavailable in that format.
Reading Header.Values is necessary for V1/ordinary HTTP multiplicity, but not
sufficient to establish that a payload 2.0 value originated in one field line.
[AWS payload differences](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html),
[Go Header.Values](https://pkg.go.dev/net/http#Header.Values).

Header.Get returns the first associated value. Using it alone to choose a
security-relevant action could hide conflicting values. The repository has no
consumer dispatcher implementation or action-header contract to reuse; a
focused search of Beakley also found no such contract.
[Go Header.Get](https://pkg.go.dev/net/http#Header.Get).

## Recommended extension boundary

Expose a small consumer-implemented interface at the ordinary HTTP middleware
boundary, with a function adapter for consumers who prefer functions:

```go
// Proposed signature, not implemented.
type RequestMiddleware interface {
    Wrap(next http.Handler) http.Handler
}
```

Header processing can inspect the complete normalized *http.Request, attach
consumer-owned typed context metadata via WithContext, reject a request with an
ordinary HTTP response, or call the next handler. This avoids inventing a second
error-to-HTTP protocol or putting application parsing errors into the Lambda
invocation-failure channel. It also supports headers beyond the action selector.

Recommend ordinary explicit composition into the handler supplied to New or
NewStreaming, usable identically with an ordinary Go HTTP server. A processor
must preserve the request's context ancestry, honor cancellation, and obey the
usual ResponseWriter lifetime. Middleware construction occurs outside request
execution; the wrapped handler must be safe for the application's concurrency.
Do not add a raw AWS-event hook for this feature.

This interface is intentionally broader than a callback returning a header map:
an action dispatcher needs validation, request metadata and controlled rejection,
not just header rewriting. Keep the business action registry, payload schemas,
handler resolution and action-specific authorization in the consuming dispatcher.
First-class support means a documented extension contract, strict selection
helpers, consumer examples and a cross-format test matrix; it does not require
edge to own the application's action registry.

## Action-selection helper and pipeline

Recommend providing a shared strict single-value header selector for consumers
to use inside their middleware/dispatcher, with inspectable missing/invalid/
ambiguous outcomes. Its exact exported signature and error types need review
alongside the shared error API. It must not require AWS dependencies or JSON.

Pending consumer details, recommend:

- Configure the action header name explicitly; do not hard-code an X-Action
  convention. Validate its name at construction.
- Require one nonempty normalized action value. Preserve identifier case.
- Check every represented value case-insensitively by header name. Reject
  multiple values, including identical repeats that are still visible.
- For an action grammar that excludes commas, reject any comma-bearing value.
  This rejects both joined duplicates in payload 2.0 and literal commas; it does
  not recover which one occurred. Do not split a joined value and choose one.
- Do not promise detection of duplicate field lines already deduplicated or lost
  by Gateway or an earlier proxy. Validate the available representation.
- Parse the action once and use the same selected value for authorization,
  dispatch and consumer logging. Do not read a mutable header again later to
  choose a different action from the one authorized.
- Do not infer authentication or authorization from possession of the action
  header. Action-specific authorization must precede action execution and any
  streaming response publication.

The consumer pipeline is normalized HTTP request and optional gateway identity,
then consumer header processing/local authentication, action selection,
action-specific authorization and execution. The application can place general
authentication before selection to suit its disclosure policy; the invariant is
that action authorization and execution use the same validated selection.

Missing/malformed action selections should ordinarily produce an application
400 response, not a Lambda invocation error. The consumer owns response bodies
and unknown-action policy. Authentication/authorization response policy remains
the separate authn/authz contract. No fallback/default action is inferred.

## Decisions still to review

- Middleware interface/function-adapter names and whether a composition helper
  materially improves ordinary Wrap calls.
- Consumer action-header name and identifier grammar. A clarification was sent;
  no concrete consumer format is assumed accepted.
- Strict selection helper signature and inspectable error categories.
- Whether action metadata is entirely consumer-owned or a demonstrated shared
  authz/dispatcher need warrants a small transport-independent action type.

Validate custom headers, missing/empty values, casing, repeated/conflicting and
identical values, flattened comma values, native HTTP behavior, both raw/typed
Gateway payloads, middleware ordering, context propagation, rejected-request
short circuiting and authorization-before-streaming. No production API changes
are made by this record.
