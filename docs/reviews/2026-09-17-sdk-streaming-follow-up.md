# AWS Lambda Go streaming: verification and AWS questions

Checked 2026-09-17 using AWS public MCP documentation, version-specific Go docs,
downloaded SDK source, AWS's public GitHub repository and local Runtime API tests.
No AWS resources were deployed or invoked; this draft has not been sent to AWS.

## Result

All direct AWS dependencies match the latest stable versions returned by the Go
module proxy:

| Module | Required and latest stable | Version timestamp |
| --- | --- | --- |
| github.com/aws/aws-lambda-go | v1.55.0 | 2026-08-27 |
| github.com/aws/aws-sdk-go-v2 | v1.47.0 | 2026-09-09 |
| github.com/aws/aws-sdk-go-v2/service/sts | v1.51.0 | 2026-09-14 |

AWS's [latest Lambda Go release](https://github.com/aws/aws-lambda-go/releases/tag/v1.55.0)
independently confirms v1.55.0 and its Go 1.27 compatibility work. The Lambda Go
runtime library owns this streaming transport; the SDK v2 core and STS modules
serve our IAM-proof path. No dependency change is necessary.

The strongest positive evidence is the SDK's own
[APIGatewayProxyStreamingResponse documentation](https://pkg.go.dev/github.com/aws/aws-lambda-go@v1.55.0/events#APIGatewayProxyStreamingResponse).
It explicitly describes AWS_PROXY, the response-streaming-invocations URI and
STREAM transfer mode. [PR 603](https://github.com/aws/aws-lambda-go/pull/603), merged
on 2025-12-05, introduced that type; its review discusses API Gateway testing.
This was already noted in decision 0008 and is reconfirmed here. It supports the
interpretation that this is an intended SDK path, not an accidental use of a
Function URL-only API.

The reader contract is also documented by
[lambda.Start](https://pkg.go.dev/github.com/aws/aws-lambda-go@v1.55.0/lambda#Start):
reader returns, closing responses that implement io.Closer, reporting read errors,
and JSON serialization precedence. The response type's
[implementation](https://github.com/aws/aws-lambda-go/blob/v1.55.0/events/apigw.go#L37-L93)
uses the same content type, metadata/delimiter structure and deliberate JSON
serialization rejection as edge. Edge retains its own bounded JSON v2 encoder
and lifecycle; replacing it with the SDK response wrapper would not change the
underlying Runtime API transport.

## Remaining discrepancy and local control

The current [custom-runtime guide](https://docs.aws.amazon.com/lambda/latest/dg/runtimes-custom.html#runtimes-custom-response-streaming)
still requires an explicit streaming-mode header, chunked encoding and closing
the underlying connection after writing the response. The SDK's
[runtime client](https://github.com/aws/aws-lambda-go/blob/v1.55.0/lambda/runtime_api_client.go#L128-L160)
uses Go's chunked request transport, but does not set the mode header or request
connection closure. Closing an io.ReadCloser or an HTTP response body is distinct
from closing the underlying TCP connection.

The local probe was extended with `sdk_gateway`, returning
`*events.APIGatewayProxyStreamingResponse` directly through lambda.Start. It uses
neither edge's adapter nor its prefix encoder. The body contains a gated reader;
the local Runtime API server must receive the first body segment before it
releases the remaining segment. A body wrapper only observes Close.

Run from the repository:

```sh
go test -race -count=1 -v ./internal/streamprobe -run '^TestSDKStreamsThroughRuntimeAPI$'
```

All ten cases pass on Go 1.27.1. The direct SDK control and edge cases all show:

```text
incremental=true response-mode=""
runtime-request-close=false response-connection-reused-for-next=true
```

The test also checks chunked transfer, integration content type, metadata/body
framing and Close before the next invocation. Fault cases emit runtime error
trailers. It verifies behavior against a permissive local server, not AWS's
acceptance or client-visible delivery. A real Lambda endpoint might close the
connection itself; the local result cannot establish what the service does.

No reviewed source explains whether invocation mode, content type or another
service-side rule makes the header/connection requirements optional. That is a
hypothesis to ask AWS about, not an established explanation.

## Upstream findings and limits

- [Issue 500](https://github.com/aws/aws-lambda-go/issues/500) explicitly asks about
  the missing mode header. Its collaborator responses and eventual closure
  address reader/writer API design, not that protocol question.
- [Issue 565](https://github.com/aws/aws-lambda-go/issues/565#issuecomment-2221292052)
  contains a collaborator's working Function URL example on provided.al2023.
  This supports Go streaming generally but is not a test of our REST integration.
- [PR 603 review](https://github.com/aws/aws-lambda-go/pull/603#discussion_r2593680120)
  resolves a default-status detail. It does not discuss the mode header or TCP
  lifetime, so it cannot settle those questions.
- [Issue 610](https://github.com/aws/aws-lambda-go/issues/610), updated in September
  2026, concerns prelude and mixed-output expectations. Its comments do not
  provide a maintainer resolution of this transport discrepancy.
- The SDK's [errorCapturingReader](https://github.com/aws/aws-lambda-go/blob/v1.55.0/lambda/runtime_api_client.go#L176-L188)
  discards `n` when Read returns bytes together with a non-EOF error. The local
  unguarded case loses those bytes; edge's existing guard returns bytes first and
  the error on the next read. No reviewed issue/release supplied a fix.

The [Gateway integration guide](https://docs.aws.amazon.com/apigateway/latest/developerguide/response-transfer-mode-lambda.html)
confirms the metadata format and omitted-length fallback, but does not define
the exact byte accounting of its 16KB delimiter constraint.
The [troubleshooting guide](https://docs.aws.amazon.com/apigateway/latest/developerguide/response-streaming-troubleshoot.html)
provides streaming timing log fields and recommends deployed curl tests with
`--no-buffer`. Its console/TestInvokeMethod path buffers responses. It discusses
timeout truncation but does not establish how Lambda runtime error trailers map
to an API Gateway client's response or to failure metrics.

## Draft for AWS

Subject: Clarify aws-lambda-go REST response streaming contract and runtime documentation

We are building a Go 1.27 net/http adapter for API Gateway REST Lambda proxy
response streaming, using the latest stable aws-lambda-go v1.55.0. Our intended
deployment is provided.al2023 with AWS_PROXY, response transfer mode STREAM and
the InvokeWithResponseStream integration URI. We reuse the SDK runtime transport.
We have not deployed this integration yet.

The SDK documents APIGatewayProxyStreamingResponse for this configuration.
However, its Runtime API client omits Lambda-Runtime-Function-Response-Mode and
does not request TCP connection closure, both required by the custom-runtime
guide. A local Runtime API control using the SDK response type directly confirms
incremental chunked output, no mode header and TCP reuse for the next invocation.

Could the Lambda runtime/Go SDK and API Gateway teams clarify:

1. **Supported configuration:** Is unmodified aws-lambda-go v1.55.0 on
   provided.al2023, returning APIGatewayProxyStreamingResponse through
   lambda.Start, the supported REST streaming path? Does an equivalent io.ReadCloser
   with the same content type and wire framing have the same support contract?
   Are any additional runtime settings or build requirements necessary?

2. **Mode header:** Is Lambda-Runtime-Function-Response-Mode: streaming required
   for this path? If the SDK may omit it, what precise condition enables
   streaming, and can AWS document that exception? If it is required, is this an
   SDK defect, and which release will correct it?

3. **Connection lifetime:** Does the documented connection-close requirement
   mean a TCP close, or just completing the chunked request? Is keep-alive reuse
   explicitly supported? If the runtime must close TCP, does the Lambda endpoint
   enforce closure itself, or must the Go SDK change?

4. **Midstream failure contract:** After metadata and body bytes reach API
   Gateway, how are Lambda-Runtime-Function-Error-Type/Body trailers exposed to
   the HTTP client? Does the client see an incomplete/reset transfer, forwarded
   trailers, or a normally completed response with the original status? Which
   access-log fields and Lambda/API Gateway metrics reliably identify the
   failure, especially for an unknown-length response?

5. **Exact prefix limit:** Does the first 16KB mean 16,000 or 16,384 bytes? Must
   the entire eight-NUL delimiter fit within that bound, or only its first byte?
   Please specify the maximum accepted byte offset/total prefix length.

6. **Reader error handling:** Is the SDK's loss of `n > 0` bytes when Read also
   returns a non-EOF error a known issue, and is a fix planned? We currently
   preserve bytes by returning them before surfacing the error on the next read.

Questions 1–3 are the immediate deployment-contract qualification. Question 4
defines reliable production failure detection. Questions 5–6 do not block our
local implementation: we currently cap the entire prefix at 16,000 bytes and
guard the reader error case.

## Recommendation

Keep the approved implementation and SDK reuse. The public SDK type and upstream
work strengthen the case that Go REST streaming is intended to work. They do not
justify silently waiving the documented transport mismatch. Retain the deployment
qualification pending AWS's answers or a separately approved deployed test.
No new public API, trust-boundary or error-policy decision is proposed here.
