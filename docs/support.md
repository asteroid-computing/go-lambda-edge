# Support and evidence

The module targets Go 1.27, processing module-owned JSON directly with JSON v2 and jsontext.
Its five public libraries are `edge`, `identity`, `authn`, `authz` and `iamproof`.
The dispatcher's internal application and commands are examples.

| Capability | Implementation | Evidence / qualification |
| --- | --- | --- |
| Buffered REST proxy | Raw object Invoke; typed HandleV1 | Local contracts and SDK registration |
| Buffered HTTP API 1.0 | Raw version 1.0; typed HandleV1 | Shared SDK type cannot identify API product |
| Buffered HTTP API 2.0 | Raw version 2.0; typed HandleV2 | Combined headers/query values cannot be recovered; separate cookies |
| REST streaming | NewStreaming, raw Handle, typed HandleV1 | Local reader/SDK tests pass; AWS deployment qualification open |
| Custom headers/actions | Ordinary HTTP middleware, ActionHeader | Strict selection tested across transports |
| Gateway identity | Opt-in native IAM/JWT/Cognito mapping | Synthetic recognition/fidelity tests; no credential re-verification |
| Local OAuth | Cognito RS256 access-token verifier | Local signatures, independent vector, cache/rotation/outage tests; no live Cognito run |
| Local IAM | Signed GetCallerIdentity proof generator and verifier | Local protocol/HTTP contracts; no live STS run; not raw execute-api SigV4 validation |
| Mixed authentication | Bearer or EdgeIAM on one route | Both paths locally tested; AWS_IAM must not preempt this route |
| Authorization | Exact predicates, ordered rules, grant callbacks | Deny-by-default, outage/cancellation and dispatch consistency |
| Consumer composition | Complete orders example, three mains | Local application matrix and external-module check |

Gateway identity is disabled by default.
Enabling it is a deployment trust choice;
custom authorizer output is not a native IAM/JWT assertion.
Flattened claims cannot recover original arrays or exact numbers.
Typed SDK inputs inherit their upstream decoding fidelity.
See [fixture qualifications](gateway-fixtures.md).

REST buffered binary-media configuration remains the application's deployment responsibility.
Streaming emits raw bytes and does not imply HTTP API streaming support.
The SDK mode-header/connection questions remain in the [streaming guide](streaming.md) and [AWS follow-up](reviews/2026-09-17-sdk-streaming-follow-up.md).

Deferred: custom Lambda-authorizer identity mappers;
Gateway request-ID, stage and route metadata accessors;
mTLS identity;
Function URLs and ALB;
generic grant resolvers;
ongoing stream reauthorization;
exported `edgetest` helpers.
The root package currently has no Gateway metadata accessor.
Broad original package responsibilities are not a claim that those APIs exist.

Beakley supplied requirements/failure cases, not copied code or compatibility targets.
[Decision 0023](decisions/0023-consumer-readiness.md#original-beakley-failure-cases-traced-to-current-coverage) maps its nine recorded defects to current tests;
this is not an exhaustive API parity audit.
Local success differs from deployed interoperability and a GitHub CI result.
The [release checklist](release-readiness.md) tracks remaining items.
