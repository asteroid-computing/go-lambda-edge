# Orders dispatcher

This consumer application uses edge's existing public APIs.
The code in `internal/orders` is example application code, not a new edge API.
It configures real Cognito and IAM-proof verification, application grants and a tiny read-only order store.

| Entry point | Registration | Actions |
| --- | --- | --- |
| `native` | HTTP on `127.0.0.1:8080` | `orders.read` |
| `buffered` | `lambda.Start(adapter)` | `orders.read` |
| `streaming` | `lambda.Start(adapter.Handle)` | `orders.read`, `orders.watch` |

Streaming is for REST API only and retains the [AWS deployment qualification](../../docs/streaming.md).
An Action header cannot change the transport selected at startup.
For typed inputs, register buffered `HandleV1`/`HandleV2` or streaming `HandleV1` instead.
The SDK then owns input decoding;
edge skips an envelope JSON round trip.

## Configuration

Set all eight variables explicitly;
there are no credential or enrollment defaults:

| Variable | Meaning |
| --- | --- |
| `COGNITO_ISSUER` | Exact trusted HTTPS issuer including pool path |
| `COGNITO_CLIENT_ID` | Allowed access-token client |
| `COGNITO_AUDIENCE` | Required resource-bound audience for this client |
| `IAM_PROOF_REGION` | Regional STS endpoint, for example `eu-west-2` |
| `IAM_PROOF_AUDIENCE` | Audience shared with the proof generator |
| `ORDER_JWT_SUBJECT` | Exact subject enrolled under this issuer |
| `ORDER_IAM_PRINCIPAL` | Exact full caller ARN enrolled for this example |
| `CORS_ORIGIN` | Exact browser origin without a trailing slash |

The example accepts resource-bound Cognito access tokens with `orders.read` scope;
it does not configure the distinct unbound M2M profile.
Both actions require read permission plus an application grant for their exact action and target.
An IAM assumed-role session ARN enrolls that exact session, not every session of the role.
Replace the demonstration grant store with application enrollment and resource rules before expanding the data set.

After setting configuration, run from the repository root:

```sh
go run ./examples/dispatcher/native
```

`GET /healthz` is public.
The protected endpoint is `GET /tenants/42/orders/7`, with `Action: orders.read` and either `Authorization: Bearer <access-token>` or `Authorization: EdgeIAM <proof>`.
The streaming variant also accepts `Action: orders.watch`, emitting two finite NDJSON records.
Construction does no network I/O;
credential-bearing requests use the real configured JWKS/STS endpoints.
These commands are not offline simulators.

Generate a proof with `iamproof.NewGenerator(region, audience, credentials)` and `Generate(ctx)`, then set Authorization to `"EdgeIAM " + token.Value()`.
Supply the AWS credentials provider already configured by your client application.
See the [helper example](../../iamproof/example_test.go) and [protocol guide](../../docs/iamproof.md).
The server needs no AWS credentials to forward the signed proof.
Do not configure AWS_IAM authorization on this mixed credential route;
the Lambda mains leave gateway identity disabled.

Build a Lambda binary without deploying it:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/orders-bootstrap ./examples/dispatcher/buffered
```

Use the `streaming` directory for a separately configured REST streaming integration.
The examples provision no routes, authorizers or AWS resources.

## Request flow

Public health and narrowly scoped CORS preflight are explicit.
Protected requests must use GET and a valid order path.
The dispatcher authenticates once, installs the caller, parses Action once, selects one registry entry, checks its identity policy and application grant, then executes the same entry and target.
The demo data is immutable;
a mutable store must preserve object/version consistency between authorization and execution, for example with a transaction.

The application uses `Authenticate` to own its JSON error envelope.
It preserves challenges, no-store and CORS.
Missing/rejected credentials yield 401;
malformed credentials/actions 400 (oversized credentials 431);
unknown actions 404;
denied grants 403;
unavailable dependencies 503.
Cancellation prevents execution, but the transport may terminate instead of delivering the attempted 503.

Application-owned CORS permits the configured origin, GET, and Authorization, Action and Custom-Trace headers.
Avoid a competing Gateway CORS policy.
CORS is not authentication: direct clients still need credentials and grants.
Additional consumer middleware uses ordinary `http.Handler` and `r.Header`.

`orders.watch` authorizes before flushing and stops on context/write/flush failures.
A later producer failure uses Go's documented `http.ErrAbortHandler`;
edge converts it to a sanitized terminal stream error.
It cannot rewrite the committed status or earlier records.
Abrupt termination is not a complete result;
replay/resume protocols belong to the application.
The Lambda main returns the reader directly and logs only edge's sanitized stream diagnostic, never tokens.

## Local tests

```sh
go test -race ./examples/dispatcher/...
sh scripts/check-consumer.sh
```

Tests sign synthetic access tokens locally and run production verifiers with closed local JWKS/STS transports.
A synthetic STS response does not prove AWS accepted a signature.
No test falls through to a live provider.
Fixtures retain REST/HTTP API distinctions, V2 combined headers and separate response cookies.

Streaming tests consume incrementally and use `testing/synctest` to prove first delivery before completion, slow-reader backpressure, early Close and late errors.
Small JSON/error tests collect bodies only for assertions.
See the [consumer testing guide](../../docs/consumer-testing.md).
