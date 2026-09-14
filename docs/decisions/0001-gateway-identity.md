# 0001: Explicit gateway identity

Status: proposed; awaiting user review of the concrete contract.

## Context and user direction

The user accepted the overall rebuild plan, then refined the initial review's
gateway-default recommendation: no gateway assertions by default feels more
natural, with a positive variadic option such as `WithGateway` to enable them.

This record distinguishes receiving API Gateway traffic from using gateway
assertions to establish application identity.

## Evidence

- AWS documents that a JWT authorizer validates the token from its configured
  identity source, then passes claims to the integration. The event's claims are
  the asserted result; comparing selected claims does not authenticate a second
  raw token from another source.
  [JWT authorizer contract](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-jwt-authorizer.html)
- AWS documents IAM authorization as route configuration requiring SigV4 and
  `execute-api` permission. Choosing whether this module consumes that assertion
  does not alter the deployed route's authentication.
  [IAM authorization](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-access-control-iam.html)
- Beakley's `lambda/adapter.go` attaches an identity even when anonymous;
  `authn/middleware.go` skips verification whenever the context slot is present.
  That coupling prevents a local verifier from naturally following the adapter
  on a route with no gateway-authenticated caller.
- Our repository currently has no runtime code. No existing API constrains the
  new option name or default.

## Recommendation for review

Use the provisional variadic option `WithGatewayIdentity(enabled bool)` with a
default of false. The positive opt-in call reads `WithGatewayIdentity(true)`;
the boolean permits configuration helpers to explicitly disable it without a
second negatively named option.

Proposed behavior:

| Configuration | Result |
| --- | --- |
| No identity option | Translate the HTTP event; do not derive identity from gateway assertions, parse credentials, or fetch keys |
| `WithGatewayIdentity(true)` | Derive identity from supported native gateway JWT/Cognito and IAM assertions |
| Local bearer middleware explicitly configured | Verify the supplied token under caller-provided issuer/client policy |

The option should cover both IAM and JWT/Cognito assertions. Custom-authorizer
contexts require an explicit mapper; their schema must not be guessed.

The base adapter should not insert an anonymous identity solely to mark itself
as having run. Reading absent identity still yields the anonymous zero value,
and protected routes deny it. Invocation metadata can remain available separately.
This does not promise to preserve an identity from an unrelated invocation or
settle precedence between multiple configured producers.

Gateway mode should use asserted event claims without supplementing permissions
from an unverified header token. It assumes the deployment restricts invocation
to the intended trusted integration. It does not independently verify SigV4 or
JWT signatures, discover an issuer, or configure AWS authorization.

## Alternatives

- `WithGateway`: concise, but suggests enabling the transport, which is already
  the adapter's purpose.
- `NoGateway`: encodes a trust-default that the user's follow-up rejects.
- `WithGatewayIdentity()` without an argument: concise, but provides no direct
  way for reusable option configuration to turn the policy back off.
- Automatically verify JWTs by default: cannot safely choose trusted issuers,
  clients, audiences, and networking policy from untrusted request data.
- Opt in for JWT only while always consuming IAM: possible, but gives the same
  gateway trust boundary two different defaults.

## Consequences and remaining decisions

- Applications must explicitly select how identity is established. An adapter
  without authentication can serve public routes; it does not imply that all
  routes are protected or that gateway authentication was disabled in AWS.
- Exact option mechanics, constructor validation, and error handling will be
  reviewed with the adapter API.
- Local-versus-gateway precedence, re-verification, conflicting credentials,
  and custom-authorizer mapping need further decisions before implementation.
- Claims unavailable at full fidelity from the gateway remain unavailable through
  that source. Local verification is the path to trusted raw-token fidelity.

## Review requested

Approve or redirect the proposed name/shape and the policy that gateway identity
is opt-in for both IAM and JWT/Cognito, while the default performs only transport
adaptation and leaves authentication to explicit composition.

## Resolution

Pending. The user's default-direction preference is recorded above; the concrete
API and behavior in this proposal have not yet been approved.
