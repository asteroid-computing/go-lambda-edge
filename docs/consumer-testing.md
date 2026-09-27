# Testing an edge consumer

Keep each test's trust boundary explicit.
Identity constructors validate facts, not credentials.
Gateway fixtures describe shapes, not deployed authorizers.

| Boundary | Tools | Evidence |
| --- | --- | --- |
| Business rule | Identity constructors and `authz.Request` | Exact action/resource policy outcomes |
| Authenticated handler | `httptest`, `identity.NewContext` | Behavior after admission |
| Authentication | Real verifier with local JWKS/STS transport | Local signature/profile/proof processing |
| Gateway conversion | Explicit SDK events or raw JSON v2 | Chosen payload format's behavior |
| Stream lifecycle | Public reader, controlled producer, incremental reads | Delivery, backpressure, cancellation and errors |
| External module | Local replace and public imports | Package and registration boundaries |

For business-handler fixtures, prefer `SourceCustomAssertion` unless modeling a different source deliberately.
Use issuer-qualified JWT claims and valid IAM caller ARNs, checking all constructor/context errors.
Never inject a caller into an adapter's or authenticator's parent context: both reject inherited callers.
An explicitly labeled verifier stub must return its contract's required source;
the source label alone is not cryptographic evidence.

For authentication tests, use real verifiers, locally signed JWTs and explicit local JWKS transports.
Generate IAM proofs with synthetic credentials and return synthetic STS responses.
Keep rejection distinct from dependency outage.
An unexpected test URL must fail, never fall through to a network transport.
The production `iamproof` suite separately checks reconstruction/signatures;
a canned STS response cannot establish AWS acceptance.

Keep REST, HTTP API 1.0 and HTTP API 2.0 fixtures distinct.
The shared SDK V1 type does not include the raw HTTP 1.0 discriminator: explicitly encode `version: "1.0"`.
V2 combines repeated header/query values and represents cookies separately.
Do not infer a lossless Gateway event from an ordinary request, or split all commas into header values.
See the [fixture inventory](gateway-fixtures.md).

`httptest.ResponseRecorder` is useful for small native outcomes.
It does not reproduce AWS mapping, network cancellation, incremental delivery or terminal stream errors.
Go 1.27's `httptest.NewTestServer(t, handler)` uses an in-memory network and can participate in `testing/synctest` without wall-clock sleeps.

Always close a returned stream.
Read metadata through its eight-NUL delimiter, then consume body chunks incrementally and inspect the terminal read error.
Synchronize with a controlled producer to assert delivery before completion, block consumption to prove backpressure, and close early to prove producer exit.
A generic response collector or final-body assertion cannot establish these.

The [dispatcher](../examples/dispatcher/README.md) demonstrates these boundaries with private helpers.
Small-response assertions retain V2 header/cookie differences;
separate tests exercise streaming lifecycle.
`sh scripts/check-consumer.sh` runs a temporary consumer module with public imports and a local replace;
CI runs it explicitly.
There is no exported `edgetest` package: decision 0023 defers it until actual consumers establish useful stable operations beyond existing tools.
