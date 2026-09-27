# 0011: Consumer request-header processing and action dispatch

Status: accepted on 2026-09-15, including the Go review's standard HTTP boundary and immutable value selector.
Selector and consumer middleware example implemented.

## Requirement and terminology

Consumers will implement APIs in which a request header identifies the action to dispatch.
They need a supported interface for processing custom headers.
This is incoming request processing, separate from decision 0010's outgoing response-header conversion.
API Gateway payload format 2.0 is not module v2.

The existing request conversion preserves custom header names and values.
Its only special handling is the accepted transport normalization, such as moving Host into Request.Host and reconciling body framing.
The response-combination table must never become an incoming-header allowlist.

## Evidence

request.go canonicalizes names and preserves repeated V1 values.
API Gateway payload 2.0 provides flattened header strings: AWS combines repeated values with commas.
Original field-line multiplicity is unavailable in that format.
Reading Header.Values is necessary for V1/ordinary HTTP multiplicity, but not sufficient to establish that a payload 2.0 value originated in one field line.
[AWS payload differences](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html), [Go Header.Values](https://pkg.go.dev/net/http#Header.Values).

Header.Get returns the first associated value.
Using it alone to choose a security-relevant action could hide conflicting values.
The repository has no consumer dispatcher implementation or action-header contract to reuse;
a focused search of Beakley also found no such contract.
[Go Header.Get](https://pkg.go.dev/net/http#Header.Get).

## Recommended extension boundary

Use the standard http.Handler interface as the supported consumer boundary.
Consumers implement handlers or compose middleware with the ordinary function shape:

```go
// Consumer-defined middleware; not an additional exported edge type.
func processHeaders(next http.Handler) http.Handler
```

Header processing can inspect the complete normalized *http.Request, attach consumer-owned typed context metadata via WithContext, reject a request with an ordinary HTTP response, or call the next handler.
This avoids inventing a second error-to-HTTP protocol or putting application parsing errors into the Lambda invocation-failure channel.
It also supports headers beyond the action selector.

Recommend ordinary explicit composition into the handler supplied to New or NewStreaming, usable identically with an ordinary Go HTTP server.
A processor must preserve the request's context ancestry, honor cancellation, and obey the usual ResponseWriter lifetime.
Middleware construction occurs outside request execution;
the wrapped handler must be safe for the application's concurrency.
Do not add a raw AWS-event hook for this feature.

The HTTP handler contract is broader than a callback returning a header map: an action dispatcher needs validation, request metadata and controlled rejection, not just header rewriting.
Keep the business action registry, payload schemas, handler resolution and action-specific authorization in the consuming dispatcher.
First-class support means a documented extension contract, strict selection helpers, consumer examples and a cross-format test matrix;
it does not require edge to own the application's action registry.

## Action-selection helper and pipeline

Recommend providing a shared strict single-value header selector for consumers to use inside their middleware/dispatcher, with inspectable missing/invalid/ambiguous outcomes.
The signatures and errors below are accepted independently of the broader transport error API.
Parsing needs no AWS event types or JSON.

The accepted selection contract is:

- Configure the action header name explicitly;
  do not hard-code an X-Action convention.
  Validate its name at construction.
- Require one nonempty normalized action value.
  Preserve identifier case.
- Check every represented value case-insensitively by header name.
  Reject multiple values, including identical repeats that are still visible.
- For an action grammar that excludes commas, reject any comma-bearing value.
  This rejects both joined duplicates in payload 2.0 and literal commas;
  it does not recover which one occurred.
  Do not split a joined value and choose one.
- Do not promise detection of duplicate field lines already deduplicated or lost by Gateway or an earlier proxy.
  Validate the available representation.
- Parse the action once and use the same selected value for authorization, dispatch and consumer logging.
  Do not read a mutable header again later to choose a different action from the one authorized.
- Do not infer authentication or authorization from possession of the action header.
  Action-specific authorization must precede action execution and any streaming response publication.

The consumer pipeline is normalized HTTP request and optional gateway identity, then consumer header processing/local authentication, action selection, action-specific authorization and execution.
The application can place general authentication before selection to suit its disclosure policy;
the invariant is that action authorization and execution use the same validated selection.

Missing/malformed action selections should ordinarily produce an application 400 response, not a Lambda invocation error.
The consumer owns response bodies and unknown-action policy.
Authentication/authorization response policy remains the separate authn/authz contract.
No fallback/default action is inferred.

## Accepted API and behavior

The user accepted the following contract, including the Go review refinements recorded below.

### Interface and composition

Use http.Handler directly, with http.HandlerFunc when adapting a handler function.
Do not export RequestMiddleware or RequestMiddlewareFunc: no edge API consumes their proposed Wrap method, and the standard interface already supplies the required extension point.
Consumers may define their own processor types without adopting an additional edge interface.

Use ordinary explicit composition: outer(inner(dispatcher)).
The outer processor runs first on the request.
Do not add WithRequestMiddleware, a Chain helper, implicit reordering, or another middleware package initially.
The same composed handler goes to edge.New, edge.NewStreaming, or net/http.
This keeps execution order visible and does not tie header processing to an AWS constructor.
Consumer middleware factories can return their own configuration errors before composition and follow the ordinary HTTP handler contract.

### Action header and identifier grammar

Require an explicit header name at selector construction.
Use Action with a value such as orders.create in examples, but do not make it a default or reserve the name globally.
Existing consumer names remain supported.
Avoid introducing an X- prefix simply to designate an application-defined field.
[RFC 6648](https://www.rfc-editor.org/rfc/rfc6648.html#section-3).

For the built-in selector, trim outer SP/HTAB and require a nonempty HTTP token, preserving case.
The existing validToken helper already implements this grammar.
This admits common identifiers such as orders.create, CreateOrder and orders-create, while excluding commas, embedded whitespace, quoting, colon, slash and non-ASCII text.
HTTP does not mandate this grammar for action headers;
it is our recommended selector contract.
A consumer with a different protocol can implement its own parser in HTTP middleware without changing gateway translation or weakening the standard selector.
[HTTP token grammar](https://www.rfc-editor.org/rfc/rfc9110.html#section-5.6.2).

### Selector API and errors

Recommend a reusable immutable selector in the root edge package:

```go
func NewActionHeader(name string) (ActionHeader, error)
func (h ActionHeader) Parse(headers http.Header) (string, error)
```

Construction validates/canonicalizes the configured field name, stored privately.
The selector is a small immutable value: copying it is safe and it needs neither shared mutable state nor pointer identity.
Its zero value has no configured name, so Parse returns a configuration error rather than inferring a default header.
Parse neither modifies headers nor writes a response.
It needs no context, AWS event types, or JSON processing.
An instance can be reused concurrently;
callers must not concurrently mutate a Header map being inspected.

Match header names using ASCII case-insensitive comparison, even when a consumer supplies a Header map with noncanonical direct assignments.
Count represented values across all matching aliases;
do not rely solely on Header.Get or Header.Values.
Count a nil/empty value slice as zero values.
Multiple represented values are ambiguous even when identical, and take precedence over errors in an individual value.

Export three action-selection sentinels, inspected with errors.Is:

| Error | Meaning |
| --- | --- |
| ErrActionMissing | No represented value, including an absent field or nil/empty slices |
| ErrActionAmbiguous | More than one represented value, or one containing a comma |
| ErrActionInvalid | Exactly one value that is empty after SP/HTAB trimming or fails token grammar |

For one comma-bearing value, ambiguity classification precedes token validation;
it deliberately covers both Gateway-combined values and a literal comma, whose origins cannot be distinguished.
Return an empty action on every error.
Do not include the supplied action or other header values in error messages.
Invalid constructor configuration is separate from these request-input categories.

Keep these application-input errors separate from transport invocation faults.
The parsing helper does not encode HTTP statuses.
Recommend 400 for selection errors and, for the header-dispatched API convention, 400 with a distinct consumer-owned unknown-action code for a syntactically valid but unregistered action.
The consumer retains its response schema and disclosure policy.
General authentication and action authorization retain their separate authn/authz rules.

### Action metadata ownership

Return a string and keep action metadata consumer-owned.
Do not add an edge.Action type, global action context key, or action package at this stage.
The dispatcher can convert the validated string into its own action type, pass it directly to its authorizer/handler, or attach it through a private typed context key.

Resolve once, authorize that same selection, and execute it.
If selecting a registered operation produces an immutable operation descriptor, pass that descriptor through authorization and execution instead of resolving again from mutable headers.
This supports first-class selection without coupling the transport module to a consumer's action registry or permissions model.

## Go rules and Go 1.27 review — 2026-09-15

Reviewed the Go rules skill's core, types, naming, functions, errors, commentary and testing guidance, the official Go 1.27 release notes, and the current adapter/request code.
This review replaces the earlier proposed Wrap interface and pointer-returning selector with the recommendations above.
The changes follow the rules on least mechanism, interfaces at their consumption boundary, and value semantics for small immutable types.
They are API design judgments, not new Go 1.27 requirements.
The user-requested consumer interface remains http.Handler;
first-class header support includes the selector, documentation, examples and contract tests.

Keep the constructor because explicit startup validation is useful.
A standalone ParseActionHeader(headers, name) helper would repeat configuration validation on every request.
A useful implicit zero value would require choosing a default header name, which conflicts with the explicit configuration recommendation.
Keep three sentinel errors with errors.Is;
they classify failures without inventing a structured error payload.
errors.AsType is useful when callers need a typed error's data, but is not a replacement for sentinel matching here.

Go 1.27 supports generic methods, but this selector has no requirement for a generic method or generic action type.
Consumers can convert the returned string to their own action type.
Direct encoding/json/v2 remains the accepted JSON boundary;
the selector and HTTP middleware need no JSON processing.
[Go 1.27 release notes](https://go.dev/doc/go1.27).

For subsequent contract tests, consider httptest.NewTestServer for ordinary HTTP integration using its in-memory network, and synctest.Sleep when a streaming test needs both virtual time advancement and quiescence.
Existing tests need no mechanical rewrite.
Go 1.27's Server.MaxHeaderValueCount is an HTTP server request limit;
it neither validates action ambiguity nor bounds Lambda response headers.
It does not replace this selector or settle decision 0010's budgets.
[httptest.NewTestServer](https://pkg.go.dev/net/http/httptest#NewTestServer), [synctest.Sleep](https://pkg.go.dev/testing/synctest#Sleep), [http.Server](https://pkg.go.dev/net/http#Server).

These accepted recommendations settle the initial implementation for this feature.
They do not imply approval of decision 0010's separate response-header budgets/combination policy or settle the broader identity/error APIs.

## Implementation and validation

action.go implements the immutable value selector and three sentinel errors.
It reuses the HTTP token validator and compares canonical field names, including noncanonical direct map assignments, without Unicode case folding.
Selection does not mutate the request or encode JSON.

Tests cover custom header names, missing/empty values, token grammar, casing, repeated/conflicting and identical values, comma values, sanitized errors, zero configuration, copying and concurrent selector reuse.
The request matrix covers native HTTP through Go 1.27's in-memory test server, raw REST and HTTP payload 1.0/2.0 decoding, and already typed V1/V2 request conversion.
It preserves the accepted distinction between V1 values mirrored across maps and actual repeats within multivalue lists.

The runnable consumer example uses ordinary http.Handler middleware and a consumer-owned context key.
Composition tests cover extra-header metadata, ordering, rejected-request short circuiting, context ancestry/cancellation and using the same selection for authorization and execution after a header change.
They exercise the existing constructor and private request foundations, not public Lambda invocation, which is still pending.
Authorization-before-streaming integration remains an acceptance test for the future public streaming writer;
this feature adds no streaming API or response-header policy.

Validation on Go 1.27.1: focused action tests and the runnable example, the full race suite (including the existing SDK streaming probe), go vet, formatting/diff checks, and Linux arm64/amd64 builds all pass.
No live AWS resources were used.
