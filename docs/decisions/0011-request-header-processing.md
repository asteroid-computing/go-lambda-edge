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

## Concrete recommendations for the open decisions

The user asked for recommendations for these remaining choices. The following
replaces the open-ended alternatives with a proposed initial contract; approval
is still pending.

### Interface and composition

Keep RequestMiddleware.Wrap and add the function adapter:

```go
type RequestMiddlewareFunc func(http.Handler) http.Handler
func (f RequestMiddlewareFunc) Wrap(next http.Handler) http.Handler
```

Use ordinary explicit composition: outer.Wrap(inner.Wrap(dispatcher)). The
outer processor runs first on the request. Do not add WithRequestMiddleware,
a Chain helper, implicit reordering, or another middleware package initially.
The same composed handler goes to edge.New, edge.NewStreaming, or net/http.
This keeps execution order visible and does not tie header processing to an AWS
constructor. Nil function adapters/next handlers are invalid programmer inputs,
like using an unusable http.HandlerFunc; the adapter is not a fallible factory.
Consumer middleware factories can return their own configuration errors.

### Action header and identifier grammar

Require an explicit header name at selector construction. Use Action with a
value such as orders.create in examples, but do not make it a default or reserve
the name globally. Existing consumer names remain supported. Avoid introducing
an X- prefix simply to designate an application-defined field.
[RFC 6648](https://www.rfc-editor.org/rfc/rfc6648.html#section-3).

For the built-in selector, trim outer SP/HTAB and require a nonempty HTTP token,
preserving case. The existing validToken helper already implements this grammar.
This admits common identifiers such as orders.create, CreateOrder and
orders-create, while excluding commas, embedded whitespace, quoting, colon,
slash and non-ASCII text. HTTP does not mandate this grammar for action headers;
it is our recommended selector contract. A consumer with a different protocol
can implement its own parser behind RequestMiddleware without changing gateway
translation or weakening the standard selector.
[HTTP token grammar](https://www.rfc-editor.org/rfc/rfc9110.html#section-5.6.2).

### Selector API and errors

Recommend a reusable immutable selector in the root edge package:

```go
func NewActionHeader(name string) (*ActionHeader, error)
func (h *ActionHeader) Parse(headers http.Header) (string, error)
```

Construction validates/canonicalizes the configured field name. The zero value
and nil receiver are unusable and Parse returns a configuration error for them.
Parse neither modifies headers nor writes a response. It needs no context,
AWS event types, or JSON processing. An instance can be reused concurrently;
callers must not concurrently mutate a Header map being inspected.

Match header names using ASCII case-insensitive comparison, even when a consumer supplies a Header map
with noncanonical direct assignments. Count represented values across all
matching aliases; do not rely solely on Header.Get or Header.Values. Count a
nil/empty value slice as zero values. Multiple represented values are ambiguous
even when identical, and take precedence over errors in an individual value.

Export three action-selection sentinels, inspected with errors.Is:

| Error | Meaning |
| --- | --- |
| ErrActionMissing | No represented value, including an absent field or nil/empty slices |
| ErrActionAmbiguous | More than one represented value, or one containing a comma |
| ErrActionInvalid | Exactly one value that is empty after SP/HTAB trimming or fails token grammar |

For one comma-bearing value, ambiguity classification precedes token validation;
it deliberately covers both Gateway-combined values and a literal comma, whose
origins cannot be distinguished. Return an empty action on every error. Do not
include the supplied action or other header values in error messages. Invalid
constructor configuration is separate from these request-input categories.

Keep these application-input errors separate from transport invocation faults.
The parsing helper does not encode HTTP statuses. Recommend 400 for selection
errors and, for the header-dispatched API convention, 400 with a distinct
consumer-owned unknown-action code for a syntactically valid but unregistered
action. The consumer retains its response schema and disclosure policy. General
authentication and action authorization retain their separate authn/authz rules.

### Action metadata ownership

Return a string and keep action metadata consumer-owned. Do not add an edge.Action
type, global action context key, or action package at this stage. The dispatcher
can convert the validated string into its own action type, pass it directly to
its authorizer/handler, or attach it through a private typed context key.

Resolve once, authorize that same selection, and execute it. If selecting a
registered operation produces an immutable operation descriptor, pass that
descriptor through authorization and execution instead of resolving again from
mutable headers. This supports first-class selection without coupling the
transport module to a consumer's action registry or permissions model.

These recommendations settle what to implement next for this feature if
approved. They do not imply approval of decision 0010's separate response-header
budgets/combination policy or settle the broader identity/error APIs.

Validate custom headers, missing/empty values, casing, repeated/conflicting and
identical values, flattened comma values, native HTTP behavior, both raw/typed
Gateway payloads, middleware ordering, context propagation, rejected-request
short circuiting and authorization-before-streaming. No production API changes
are made by this record.
