# go-lambda-edge

A Go 1.27 module being built to serve AWS API Gateway requests through ordinary
`net/http` handlers in AWS Lambda, with companion identity, authentication, and
authorization packages.

The root package is `edge`. This project is being designed and built from scratch.
The constructor, options, private event decoder, and request conversion are implemented. The decoder
uses AWS Lambda Go v1.55.0 event structs and encoding/json/v2 directly, retaining
authorizer JSON separately. Request conversion covers URLs, headers, body bytes,
and request lifetime. A shared invocation scope now covers cleanup and cancellation;
private response encoding provides text/base64 selection and bounded direct JSON
v2 output. The buffered HTTP writer, gateway header conversion, public invocation
methods, and identity extraction are not implemented yet; the adapter cannot yet
be registered as a Lambda handler.

The accepted streaming design now has a private bridge with incremental delivery,
backpressure, cancellation, cleanup, and terminal-error handling. Its lifecycle
is covered by race-enabled synthetic concurrency tests. HTTP streaming framing
and public streaming entry points remain under construction; deployed API Gateway
streaming has not been verified.

## Header-selected actions

`edge.NewActionHeader("Action")` creates an immutable selector whose
`Parse(r.Header)` method returns an action string or an error. Configure the
header name explicitly. Selection preserves case, trims outer spaces/tabs, and
requires a nonempty HTTP token. Repeated values and comma-bearing values are
rejected, including Gateway payload 2.0's comma-combined duplicates. Use
`errors.Is` with `edge.ErrActionMissing`, `edge.ErrActionAmbiguous`, or
`edge.ErrActionInvalid` to distinguish request-input failures.

Use ordinary `http.Handler` middleware to process this and other custom headers.
The consumer owns its action registry, metadata, authorization and HTTP error
responses. Select once, authorize that selection, then execute it; do not select
again from mutable headers. An action header does not authenticate a caller.
The selector is usable now with ordinary Go HTTP servers, independently of the
unfinished Lambda invocation methods.

See the [runnable middleware example](action_example_test.go) and
[accepted header contract](docs/decisions/0011-request-header-processing.md).

- [Implementation plan](docs/plan.md)
- [Design decisions](docs/decisions/README.md)
- [Repository working agreement](AGENTS.md)
- [Initial codec benchmarks](docs/benchmarks.md)
