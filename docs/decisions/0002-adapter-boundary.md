# 0002: Adapter construction and invocation boundary

Status: proposed; awaiting user review.

## Question

Should the adapter validate configuration at construction, and should failures to
translate an invocation return a Lambda error or a synthesized HTTP response?

The raw `Invoke` boundary and variadic gateway-identity option are accepted
direction. Constructor errors, option application, and invocation-failure policy
still need decisions before implementation.

## Evidence

- The official AWS Lambda Go SDK defines `Handler` as
  `Invoke(context.Context, []byte) ([]byte, error)`. Its v1.54.0 implementation
  recognizes this interface before reflected function dispatch and passes raw
  bytes directly. The ordinary function path decodes and encodes through
  `encoding/json`.
  [Handler interface](https://pkg.go.dev/github.com/aws/aws-lambda-go@v1.54.0/lambda#Handler)
  [SDK handler source](https://github.com/aws/aws-lambda-go/blob/v1.54.0/lambda/handler.go)
- Consequently, the adapter object should be passed to `lambda.Start(adapter)`.
  Passing `adapter.Invoke` as a function loses interface dispatch and subjects
  `[]byte` parameters/results to ordinary JSON serialization semantics.
- AWS documents function errors as a generic API Gateway 502 response; invocation
  requests rejected by the Lambda API produce 500. API Gateway does not retry
  Lambda invocations. Returning a valid proxy response instead lets the function
  control the HTTP status and body, and is a handled outcome rather than a Lambda
  function error.
  [API Gateway/Lambda error behavior](https://docs.aws.amazon.com/lambda/latest/dg/services-apigateway-errors.html)
- Beakley's adapter synthesizes a 500 response and returns a nil error for
  extraction/conversion failures. This obscures the distinction between an
  application HTTP response and failure of the adapter itself.
- Our repository currently contains plans and decision records only. There is
  no implementation or compatibility commitment to preserve.

The SDK version above was inspected as evidence, not selected as the new module's
dependency version. That selection and its checks belong to implementation setup.

## Recommendation

### Public shape

Proposed signatures, not implemented API:

```go
func New(handler http.Handler, opts ...Option) (*Adapter, error)
func WithGatewayIdentity(enabled bool) Option
func (a *Adapter) Invoke(ctx context.Context, payload []byte) ([]byte, error)
```

`New` validates the handler and configuration without I/O. Missing handlers,
invalid options, and invalid configuration return an error before the process
begins serving. Do not implicitly use `http.DefaultServeMux` or turn a missing
handler into a production fallback response.

Options are applied in argument order, with the last assignment winning for
scalar settings such as gateway identity. Reject nil options. The adapter owns
its resulting configuration, exposes no mutating setters, and uses fresh request
state for each invocation. The supplied handler remains caller-owned and must be
safe for concurrent use if invocations are made concurrently.

Application startup handles constructor failure in `main`, then calls
`lambda.Start(adapter)`. The library does not terminate the process. No `MustNew`
or separate library-owned start function is needed initially.

### Failure ownership

| Outcome | Proposed behavior |
| --- | --- |
| Invalid adapter configuration | `New` returns an error; startup caller decides how to report/terminate |
| Invalid JSON, unsupported event, or missing required envelope structure | `Invoke` returns an error without invoking the HTTP handler |
| Failure to convert the event, such as invalid base64 | `Invoke` returns an error without invoking the HTTP handler |
| Failure to encode a valid gateway response | `Invoke` returns an error; no partial payload is usable |
| Handler/middleware writes an HTTP response, including 4xx/5xx | `Invoke` returns the encoded response with nil error |

In particular, malformed application JSON inside an otherwise valid request body
is the HTTP handler's concern. It is not an invalid Lambda event. The adapter
must not interpret application payloads to decide whether an invocation failed.

Return contextual errors rather than logging and returning them. Error strings
must not include the raw event, authorization header, JWT, or application body.
An HTTP client normally receives the gateway's generic 502 for adapter failures;
operators retain the Lambda failure signal and diagnostic error. Direct callers
of `Invoke` also receive the error.

This policy covers transport decoding/conversion/encoding failures. It does not
yet decide authentication outcomes, malformed authorizer contexts, context
cancellation timing, handler panics, unsupported response capabilities, or the
public sentinel/type taxonomy. Those require the relevant contracts.

## Alternatives and tradeoffs

- Return only `*Adapter` from `New`: a shorter startup line, but configuration
  errors must become panics or surface during an invocation. An explicit error
  keeps validation at the point the caller still controls startup.
- Return synthesized HTTP 500 responses for transport faults: gives clients a
  controlled response body and status, but records the Lambda invocation as
  handled. Before classifying the event, the correct response format is also
  unknown. Prefer a consistent error path for adapter failures.
- Return Lambda errors for every HTTP 4xx/5xx: loses the handler's intended
  response and misclassifies ordinary application outcomes as invocation faults.
- Export event unions or typed proxy methods now: adds public compatibility
  obligations without improving the ordinary handler or Lambda startup path.

## Validation after approval

- Constructor cases: absent handler, nil option, invalid configuration, explicit
  gateway enable/disable, and duplicate scalar options.
- SDK-boundary tests pass an adapter object through `lambda.NewHandler(...).Invoke`
  and verify object-shaped responses without base64/double encoding.
- Malformed/unsupported envelopes never reach the application handler.
- Application HTTP error statuses and bodies survive the round trip with nil
  invocation error; transport faults return errors with no sensitive payloads.

These tests will follow the reviewed payload and response contracts; this
decision does not authorize inventing those contracts during implementation.

## Resolution

Pending user approval or redirection.
