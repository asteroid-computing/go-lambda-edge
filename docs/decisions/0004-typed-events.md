# 0004: Typed event entry points

Status: accepted by the user;
implemented and SDK-tested on 2026-09-16.

## Question

How should callers with an AWS event struct bypass edge's JSON envelope work, while retaining the same HTTP and gateway-identity behavior?

## Evidence

- The user requested an option to skip JSON when the caller already has typed events and accepted reuse of AWS event structs in decision 0003.
- Our Adapter currently only holds a handler and immutable configuration.
  There is no invocation implementation to preserve or duplicate.
- AWS permits a handler with context and a JSON-compatible input, returning a JSON-compatible response and error.
  Its runtime chooses the input type from the function signature and owns decoding/encoding for that function.
  [AWS handler signatures](https://docs.aws.amazon.com/lambda/latest/dg/golang-handler.html) [SDK implementation inspected at v1.54.0](https://github.com/aws/aws-lambda-go/blob/v1.54.0/lambda/handler.go)
- AWS supplies matching request and response types for both payload families.
  V1 represents REST proxy and HTTP API payload 1.0;
  the V1 request struct has no version discriminator.
  The V2 JWT claims representation is map[string]string.
  [AWS event types](https://pkg.go.dev/github.com/aws/aws-lambda-go@v1.54.0/events)

## Decision

Expose two methods on the same configured Adapter:

```go
func (a *Adapter) HandleV1(ctx context.Context, event events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error)
func (a *Adapter) HandleV2(ctx context.Context, event events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error)
```

The user approved these names, exact signatures, and the boundary contract below.
Both methods are implemented, sharing the same request, response, ownership and gateway identity primitives as raw invocation.

The ordinary raw registration remains lambda.Start(adapter).
A caller that chooses a fixed payload family can register lambda.Start(adapter.HandleV1) or lambda.Start(adapter.HandleV2), or pass an already decoded struct directly.
These registrations are exercised through the actual SDK handler wrapper.

Use shared internal HTTP translation, response capture, and identity policy for both raw and typed entry points.
Do not marshal a typed event and call Invoke;
do not encode a response just to decode it into an AWS struct.
Typed methods skip envelope decoding, automatic format selection, and response serialization inside edge.
They still perform HTTP conversion, including base64 body handling.

All JSON handled by edge continues to use encoding/json/v2 directly.
Registering a typed function with the inspected SDK delegates envelope decoding/encoding to the SDK's encoding/json path outside edge.
Applications requiring direct v2 control of the Lambda envelope should use the raw Adapter registration.

Keep semantic validation and transport-error behavior consistent where the typed values retain the necessary information.
On a conversion error, return a zero response and a non-nil error.
Ordinary application HTTP errors remain responses with nil Go error, following decision 0002.

Typed values cannot prove that original JSON had unique keys, exact spelling, correct field presence, or preserved arbitrary numbers.
In particular, V1 has no Version field to validate.
Do not promise raw-wire validation equivalence or recover values lost by a caller's decoder.
Identity extraction must respect the fidelity of the supplied source and must never supplement it with unverified header JWT claims.
Authorizer mappings are settled in accepted decision 0018.

## Alternatives and tradeoffs

- A WithSkipJSON boolean cannot change a Go method's parameter or result type;
  a distinct callable boundary is needed for typed input and output.
- One any-based method with a type switch can accept both event types but loses the compile-time request/response pairing.
  Registering that method directly with Lambda would receive generic JSON values instead of AWS structs.
- Separate constructors or adapter types could encode the same distinction but add configuration surface without a demonstrated need.

AWS event types become a deliberate public dependency of edge.
Keep them out of transport-independent identity/authentication/authorization packages.
Future SDK upgrades must preserve the typed API and pass boundary compatibility tests.

## Validation plan

- Compile and exercise each typed method through the pinned SDK handler wrapper.
- Compare raw and typed HTTP results using independently constructed equivalent events for both formats, including cookies, multivalue headers, binary bodies, application error responses, and conversion failures.
- Verify reviewed gateway-identity defaults and opt-in behavior on both paths.
- Test and document cases where original wire information is unavailable after upstream decoding, without claiming identical raw-wire rejection guarantees.

## Resolution

The user approved the proposed methods and boundary contract.
This extends the earlier decision 0002 deferral of typed proxy methods in response to a concrete consumer requirement;
its raw invocation contract remains accepted.
The later HTTP and identity decisions are now implemented behind both complete typed methods.
Tests verify SDK registration and the documented upstream-codec qualifications.
