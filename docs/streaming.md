# REST response streaming

`edge.NewStreaming` adapts a REST API Lambda proxy event to an ordinary `http.Handler`.
The returned reader contains bounded JSON metadata, eight NUL bytes, then raw body bytes.
AWS consumes that prefix;
it is not application body content.
Streaming input requests and API Gateway HTTP API responses are not supported.

The local implementation is complete.
**Deployment compatibility remains unverified.**
AWS Lambda Go v1.55.0 is the latest stable release checked on 2026-09-17.
Local Runtime API probes show incremental delivery and reader closure, but the SDK omits the documented streaming-mode header and reuses the response connection.
See [decision 0022](decisions/0022-streaming-writer-review.md#s3-sdk-issue-retain-the-existing-deployment-release-gate).
These observations neither prove real-service rejection nor establish deployed support.
No live AWS test has run, and edge does not patch the SDK or transport.

## Registration and configuration

In a Lambda main, construct the adapter, handle its startup error, and register one method:

```go
adapter, err := edge.NewStreaming(handler,
    edge.WithStreamErrorReporter(func(ctx context.Context, err error) {
        logger.ErrorContext(ctx, "response stream failed", "error", err)
    }),
)
if err != nil {
    log.Fatal(err)
}
lambda.Start(adapter.Handle)
```

`Handle(context.Context, jsontext.Value)` captures raw event JSON through the SDK, then validates/decodes it using direct JSON v2 inside edge.
Explicitly versioned HTTP API envelopes are rejected.
Alternatively register `lambda.Start(adapter.HandleV1)` for an `events.APIGatewayProxyRequest`;
this skips request-envelope JSON work inside edge, with input codec validation and numeric fidelity owned by the caller/SDK.
The V1 type also represents HTTP API payload 1.0, so its type cannot prove that the deployment is a REST streaming integration.
Both entry points encode output metadata using direct JSON v2.

Do not register the StreamingAdapter object or wrap it in a byte-returning Invoke method.
The SDK must receive the reader to consume incrementally.
Direct callers own reading and closing it.
There is no HandleV2 streaming entry point.

The AWS configuration requires a REST proxy integration with response transfer mode STREAM and an InvokeWithResponseStream integration URI.
The buffered integration configuration alone does not enable streaming.
See the official [REST streaming overview](https://docs.aws.amazon.com/apigateway/latest/developerguide/response-transfer-mode.html) and [Lambda streaming integration format](https://docs.aws.amazon.com/apigateway/latest/developerguide/response-transfer-mode-lambda.html).

Existing gateway-identity, claims-budget and response-header-budget options also apply.
Gateway identity remains disabled by default;
inherited callers are rejected.
The adapter is immutable and reusable;
the application owns handler concurrency safety.
Each invocation has its own request, context and writer.

## Commitment, flushing and limits

The first final WriteHeader, Write or Flush commits status and an owned header snapshot.
Later ordinary header mutations cannot change the response.
An empty Write commits 200 when needed but does not force publication.
WriteHeader alone does not necessarily hand off a reader.

When Content-Type needs detection, edge retains at most 512 body bytes.
Filling that prefix, flushing or completing the handler publishes metadata and drains pending bytes through an unbuffered pipe.
An explicit or suppressed Content-Type, or a nonblank Content-Encoding, permits immediate publication on the first nonempty Write.
Empty Content-Encoding values do not suppress detection.

Use `http.NewResponseController(w).Flush()` to observe flush errors.
The writer also supports `http.Flusher`;
its void Flush records failures for finalization.
An empty Flush publishes without inventing a type, and later writes cannot change it.
Flush provides an application publication boundary, not a promise of client receipt or preserved network chunk boundaries.
Middleware wrappers must preserve FlushError/Flusher or expose ResponseController-compatible Unwrap.

Streaming never infers Content-Length, including tiny, empty and HEAD responses.
A valid supplied length is honored when status permits it.
A whole Write that would exceed it fails before any bytes from that Write are accepted.
A short non-HEAD body fails at handler completion.
Edge does not generate Transfer-Encoding;
Gateway owns its transport framing.
This length policy intentionally differs from buffered edge and native net/http length inference.

HEAD counts/discards representation writes, may detect their type and permits a supplied representation length without requiring that many writes.
Statuses 204, 205 and 304 suppress bodies and Content-Length;
304 also suppresses Content-Type.
Nonempty writes for these statuses return http.ErrBodyNotAllowed without invalidating the response.
The 205 rule is an intentional difference from net/http.
Informational responses, application trailers, Hijacker, Pusher, deadline controls and full duplex are unsupported.

The shared default header resource budget is 256 KiB, adjustable through WithResponseHeaderBudget.
Separately, the complete metadata prefix including delimiter must fit 16,000 bytes, edge's conservative interpretation of the AWS metadata constraint.
Validation happens before reader handoff.
MultiValueHeaders preserves repeated response fields, including Set-Cookie.

The streaming body is raw, without base64 or a JSON envelope.
It does not use the buffered adapter's 6 MiB envelope limit;
edge does not add an arbitrary body cap.
This is not a claim to exceed AWS service size, bandwidth or timeout limits.
Retained sniff bytes and metadata are bounded, not the application's allocations.

## Authentication and application protocols

Authenticate, resolve the action/resource and authorize before writing or flushing.
The same `authn` and `authz` components compose with both adapters.
Denied access, dependency outages, challenges, CORS and JSON error bodies remain ordinary HTTP responses selected by application middleware.
A valid 401, 403 or 503 response is a successful transport outcome.

After commitment, a later authorization failure cannot replace the status or earlier bytes.
The application owns ongoing authorization, heartbeats and protocol-specific terminal messages such as an SSE error event.
Edge does not turn a late transport failure into an application record or retry a stream.

The [runnable examples](../streaming_example_test.go) cover SSE, direct JSON v2 NDJSON, binary and gzip bodies.
Their local reader collector is only for displaying test output;
a Lambda wrapper must return the reader directly.
For gzip, negotiate Accept-Encoding in the application, set the response type/encoding before writing, flush the encoder before the HTTP writer, and close the encoder to finish its footer.
Gateway does not perform streaming content compression for the integration.

## Errors and ownership

Logical HTTP commitment and reader handoff are different boundaries.
Before the handoff, invocation faults return a nil reader and error;
early producer panics propagate after cleanup.
After handoff, failures preserve status and prior bytes and become sanitized terminal reader errors.
Late panics are recovered as ErrStream.
Use errors.Is for public categories and context.Canceled/context.DeadlineExceeded;
errors.AsType can inspect sanitized InvocationError details.

WithStreamErrorReporter runs once for a terminal failure, after request cleanup and before completion.
Pre-handoff failures are returned directly and are not reported.
The callback receives the invocation context, possibly canceled, and must return promptly.
It must not read or close its own stream, which would wait for the callback itself to finish.
A callback panic adds a sanitized ErrStream without replacing the original failure.
New rejects this streaming-only option;
NewStreaming rejects a final nil reporter.
Options use their existing ordered, last-assignment-wins policy.

The bridge uses one producer and backpressure, not a goroutine or queued body per Write.
Read and Close coordinate with producer completion.
Terminal reads wait for cleanup/reporting;
Close cancels the request, unblocks pipe writes and joins the producer.
Handlers must observe request cancellation and stop on write/flush errors.
A handler that ignores both can delay Close until invocation termination.
Do not abandon a returned reader without closing it.

Request bodies and multipart files on the served request remain owned until producer cleanup;
multipart files attached only to a middleware-created request copy remain that middleware's responsibility.
The request context is canceled on completion.
Local SDK probes verify reader Close before the next invocation.
Client disconnection need not immediately cancel Lambda execution;
this API cannot promise disconnection notification that AWS does not provide.

The SDK probe also covers its handling of readers returning bytes plus an error: edge exposes those bytes first and the error on the next read, preserving the payload despite the current SDK reader wrapper.
See the accepted [ownership design](decisions/0009-streaming-boundaries.md) and [writer review](decisions/0022-streaming-writer-review.md) for evidence and limits.
