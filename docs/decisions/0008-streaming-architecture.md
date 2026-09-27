# 0008: Streaming architecture alongside buffered invocation

Status: architecture accepted;
concrete boundaries proposed in decision 0009.

## Question

How can edge support streaming HTTP responses without changing the accepted buffered/raw/typed contracts or abandoning ordinary net/http handlers?

## AWS evidence

- API Gateway currently supports response streaming for REST APIs, including Regional, private, and edge-optimized endpoints.
  HTTP APIs and request streaming are not supported.
  Integration response transfer mode must be STREAM.
  Streams can run for up to 15 minutes, subject to invocation and idle timeouts;
  a disconnected client need not stop Lambda execution.
  [Streaming support and constraints](https://docs.aws.amazon.com/apigateway/latest/developerguide/response-transfer-mode.html)
- Lambda proxy streaming uses InvokeWithResponseStream and a different integration URI ending in response-streaming-invocations.
  Input remains the REST proxy event.
  Output is JSON metadata, eight NUL delimiter bytes, then raw payload bytes.
  The delimiter must appear within the first 16 KB.
  Metadata supports statusCode, headers, multiValueHeaders, and cookies.
  There is no base64 body field.
  These deployment and framing requirements must match.
  [Lambda streaming integration contract](https://docs.aws.amazon.com/apigateway/latest/developerguide/response-transfer-mode-lambda.html)
- The full API Gateway streaming guide explicitly states that streaming can exceed the ordinary 10 MB response limit.
  Use that service-specific guide over the conflicting 10 MB statement in an AWS .NET blog article;
  do not impose a 10 MB streaming cap.
  Lambda documents a separate 200 MB streamed-response ceiling versus 6 MB buffered.
  Both stages can apply bandwidth shaping;
  the gateway describes 2 MB/s after its first 10 MB.
  No deployed throughput or boundary measurements have been performed by this project.
  [API Gateway streaming guide](https://docs.aws.amazon.com/apigateway/latest/developerguide/response-transfer-mode.html) [Lambda streaming](https://docs.aws.amazon.com/lambda/latest/dg/configuration-response-streaming.html)

## SDK and repository evidence

The pinned aws-lambda-go v1.55.0 already provides events.APIGatewayProxyStreamingResponse, with status, headers, multivalue headers, cookies, and an io.Reader body.
It implements Read, Close, and ContentType;
Read generates the prefix using encoding/json.
Reusing that serializer inside edge would conflict with our direct JSON v2 requirement.
[Pinned SDK streaming response](https://github.com/aws/aws-lambda-go/blob/v1.55.0/events/apigw.go)

On 2026-09-14, both `go list -m -json github.com/aws/aws-lambda-go@latest` and AWS's GitHub latest-release endpoint resolve to v1.55.0.
The release is neither draft nor prerelease, was published on 2026-08-27, and explicitly includes Go 1.27 compatibility work.
go.mod already requires this version.
"Pinned" means the exact version recorded for reproducible builds, not a policy of remaining on an older release.
Use the latest stable SDK and verify compatibility when updating.
[Latest stable release verified](https://github.com/aws/aws-lambda-go/releases/tag/v1.55.0)

The SDK's function-handler path can return readers.
Its runtime loop consumes and closes the reader and honors its ContentType method.
Its legacy/public Handler.Invoke byte-slice interface instead reads the complete response into memory.
Calling lambda.NewHandler(...).Invoke can check content but cannot prove incremental streaming.
Reader dispatch has JSON-serialization precedence rules;
the exact wrapper must be tested against this pinned implementation.
[Handler dispatch](https://github.com/aws/aws-lambda-go/blob/v1.55.0/lambda/handler.go) [Runtime loop](https://github.com/aws/aws-lambda-go/blob/v1.55.0/lambda/invoke_loop.go) [Runtime transport and error trailers](https://github.com/aws/aws-lambda-go/blob/v1.55.0/lambda/runtime_api_client.go)

Our request conversion can be shared.
The buffered response encoder creates a complete body and envelope, so it cannot implement streaming.
withInvocation currently ends its scope when its callback returns;
returning a reader from that callback would cancel and clean up too early.

## Recommendation

Keep both modes in the root edge package with a common http.Handler application boundary.
Add an explicit streaming adapter/entry point rather than changing the return contract of Invoke, HandleV1, or HandleV2.
A boolean option cannot make a byte-slice result incremental.
Constructor/method names and exact raw/typed streaming signatures remain a separate proposal;
NewStreaming is a possible name, not an approved API.

Start streaming support with REST Lambda proxy integration only.
Do not infer streaming from payload version, an Accept header, or a call to Flush.
The AWS integration must be configured for it.
HTTP API payload 1.0 shares the V1 event type but does not gain streaming support from that type.
Function URLs are a possible later target requiring their own request/authentication contract.

Share request conversion, gateway metadata, identity production, and applicable HTTP header/status validation.
Use distinct buffered and streaming writers:

| Concern | Buffered | Streaming |
| --- | --- | --- |
| Application boundary | http.Handler | http.Handler |
| Response delivery | Complete AWS response/envelope | Metadata prefix followed by raw bytes |
| Body retention | Bounded full body | Small bounded buffering with backpressure |
| Flush | Unsupported | Flush/Error-capable writer; no promise about downstream packet timing |
| Binary bytes | Base64 when required | Raw bytes |
| Content length | Can infer after handler returns | Usually unknown; do not prebuffer to infer |
| Failure | No usable result on transport failure | Depends on whether transmission has started |
| Lifetime | Through synchronous completion | Through stream consumption/closure and producer completion |

Retain AWS's runtime transport.
Implement the small metadata encoder/framing reader in edge with encoding/json/v2 directly;
do not build a separate Runtime API client.
Use a bounded producer/consumer bridge, such as io.Pipe, between ServeHTTP writes and the runtime's reader.
Close must cancel the producer and unblock pending writes.
Prove that no producer survives invocation completion.

Authenticate and authorize before exposing stream content.
Validate the prefix before emitting it and preserve multivalue headers supported by this format.
Unlike the earlier arbitrary snapshot proposal, the streaming prefix has a documented AWS framing bound;
verify its exact byte boundary in the wire tests.

Define commitment and failure semantics explicitly.
Pre-transmission failures can remain invocation errors.
After transmission begins, headers/status cannot be replaced and bytes cannot be retracted.
Terminate a failed stream and use the runtime's error path;
do not inject an unrequested error document into arbitrary application data.
Application-defined SSE/NDJSON error events remain the application's decision.
Consumer-visible truncation/error behavior needs testing.

Goroutine panics require a deliberate bridge to the runtime failure mechanism;
the SDK's synchronous handler recovery does not catch a producer panic after the function returns.
Preserve sanitized diagnostics and explicitly review any necessary difference from buffered panic propagation.
Do not advertise arbitrary HTTP trailers, Hijacker, WebSockets, request streaming, or full duplex merely because response flushing works.

## Sequence and proof before public API

Design the shared boundaries now;
finish the first buffered adapter slice, then implement streaming under an agreed contract.
First make a local Runtime API protocol probe using the pinned SDK.
Verify that initial bytes arrive before the producer completes, along with ContentType handling, raw/typed input capture, JSON v2 prefix framing, backpressure, Close, cancellation, and late read errors.
Do not use a fully collecting test wrapper as evidence of streaming.

Then exercise ordinary SSE, NDJSON, and binary handlers with a slow reader and an early-closing reader.
Cover prefix limits, headers/status commitment, HEAD, bodyless statuses, compression, panic cleanup, and warm-invocation reuse.
An optional deployed test would be needed to establish end-to-end API Gateway flush timing, interruption behavior, and disputed service limits;
none is authorized or performed by this design review.

## Alternatives and resolution

Using the SDK streaming response object is attractive but delegates its internal prefix JSON to the v1 compatibility API.
Returning complete byte slices keeps the old API but defeats incremental delivery.
A separate runtime or mandatory Lambda Web Adapter adds deployment machinery that the existing SDK transport does not yet appear to require.

The user agreed with the architecture and requested the detailed design graph.
[Decision 0009](0009-streaming-boundaries.md) proposes concrete APIs, ownership, commitment, and failure rules, backed by a local SDK Runtime API probe.
SDK transport reuse remains the preferred approach, with a documented wire-header compatibility question to settle before claiming AWS deployment support.
No streaming methods or placeholder APIs have been added.
