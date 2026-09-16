# 0016: IAM credentials or OAuth tokens on the same route

Status: architecture and exported Go client helper accepted on 2026-09-16.
The detailed IAM protocol/API was subsequently approved and implemented under
0017. The shared HTTP selector was accepted and implemented under 0019.
First-party JWT verification and authorization remain separate work.

## Requirement and correction

The original goal includes accepting IAM credentials or an OAuth bearer token
when calling the same API. Design for the same route and application handler,
without requiring built-in AWS_IAM authorization. The recommendation in 0015 to
prefer AWS_IAM for IAM clients alone does not meet that combined requirement.
Gateway IAM remains an optional deployment path, not a prerequisite or the sole
planned IAM authentication path.

## Evidence

- [API Gateway route authorization](https://docs.aws.amazon.com/aws-sdk-php/v3/api/api-apigatewayv2-2018-11-29.html)
  selects one authorization type, including NONE, JWT, AWS_IAM or CUSTOM. It is
  not a built-in per-request choice between IAM and JWT authorization.
- [SigV4 signing](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html)
  derives an HMAC key from the secret access key, date, region and service. Edge
  does not possess a caller's IAM secret. An execute-api signature cannot be
  transplanted onto an STS request for verification.
- [EKS authentication](https://docs.aws.amazon.com/eks/latest/best-practices/identity-and-access-management.html)
  demonstrates tokens containing a presigned STS request submitted to AWS for
  verification, followed by separate application authorization.
- [AWS IAM Authenticator](https://github.com/kubernetes-sigs/aws-iam-authenticator#how-does-it-work)
  documents client presigning, server validation and submission to STS, and a
  signed destination identifier preventing reuse across different clusters.
  This establishes a protocol pattern, not a dependency or wire-format choice.
- [GetCallerIdentity](https://docs.aws.amazon.com/STS/latest/APIReference/API_GetCallerIdentity.html)
  identifies the credentials signing that request and requires no granted
  permission for the operation. It does not establish application permissions.

AWS definitions were checked through public AWS MCP; the authenticator's own
documentation supplies implementation-pattern evidence. At proposal time there
was no authn middleware. The source rules in 0012 permit gateway/custom IAM and locally verified
JWT; online IAM verification requires a provenance refinement before implementation.

## Accepted direction

Configure a route without built-in gateway authentication and protect the handler
with explicit authn middleware. Select one configured verifier by an unambiguous
credential format, never by retrying another verifier after authentication fails.

OAuth bearer input goes to its configured token verifier. The planned Cognito
path validates JWT access tokens; opaque OAuth tokens need a separately configured
issuer-specific mechanism. IAM clients use their existing AWS credential provider
to sign an STS GetCallerIdentity proof. An exported Go helper included in this
module generates the proof; this is a required deliverable, not an optional
consumer-owned implementation. The secret key stays with the client.

The IAM verifier validates the proof contract and submits only an approved STS
request to AWS, using the client's signature rather than the Lambda's credentials.
Identity comes from the verified AWS response, not an ARN asserted by the client.
Either successful verifier constructs one immutable caller; common application
authorization decides whether it may execute the selected action. Successful IAM
authentication alone must not admit every AWS account to the application.

This is one configured authentication selector choosing one producer per request,
compatible with the no-identity-merging direction in 0012. Conflicting credentials
must fail rather than select an identity by precedence.

The proof requires signed application/environment binding, bounded freshness, an
explicit replay policy, constrained endpoint/action/method validation and bounded
network work. Dependency failures must remain distinct from invalid credentials.
Wire format, source enum, caching and detailed verification rules remain proposed
work for a concrete protocol review.

## Tradeoffs and alternatives

The IAM path adds an online STS dependency and a client protocol/helper. It is not
drop-in compatibility with generic execute-api SigV4 clients. With middleware
placement, unauthenticated traffic reaches the integration Lambda.

A custom Lambda authorizer could host the same verifier selection before the
integration. It changes deployment/caching/assertion transport but does not remove
the proof requirement. Separate IAM/JWT routes could use native authorization and
a common backend, but would not meet the stricter same-route goal.

## Resolution and deliverables

The user confirmed that the client token is a signed GetCallerIdentity proof
which the server submits to STS to obtain verified identity. The user requires
documentation and a Go helper in the module's exported API. The client-helper
compatibility question is resolved; unchanged execute-api SigV4 clients are not
the contract of this authentication path.

Deliver:

- An exported Go client helper that generates the accepted proof using the
  caller's AWS credentials, including temporary credentials where available.
- An authn verifier that validates the proof and forwards the client-signed STS
  request without signing it again using server credentials. Construct identity
  only from the successfully verified response.
- Explicit selection between IAM proof and configured OAuth bearer verification
  on the same route, producing one caller for common application authorization.
- Consumer documentation and runnable client/server examples covering credential
  acquisition, matching application binding, token transport and lifetime,
  temporary credential handling, and authentication versus authorization.
- Contract tests for client/verifier interoperability, destination binding,
  freshness, malformed/conflicting credentials, endpoint restrictions and STS
  failure handling. Use local tests; no live AWS invocation is needed by default.

The next design review will settle helper package placement and signatures,
credential-provider dependencies, versioned wire format and HTTP transport,
application binding, expiry/replay policy, endpoint/region policy, provenance and
error behavior. Acceptance of the architecture does not freeze these details.

[Decision 0017](0017-iam-proof-protocol.md) subsequently settled those details
and its production helper/verifier are implemented with local validation.
[Decision 0019](0019-http-authentication-selector.md) settled the remaining
shared HTTP selector API and response contract, now implemented with local
validation and synthetic Bearer fixtures. No live STS check has run.
