# Application authorization

`authz` checks an authenticated caller's permission to perform an application action on an optional resource.
It is implemented with only standard-library and `identity` dependencies.
It does not authenticate credentials, evaluate AWS IAM policies, resolve grants automatically or write HTTP responses.

The [runnable examples](../authz/example_test.go) show an application grant check and a header-selected HTTP dispatcher with Cognito and IAM-proof authentication.
The dispatcher example configures real verifiers but sends no credentials, so running it makes no network calls.
Integration tests separately exercise signed Cognito tokens and production IAM-proof verification against synthetic JWKS/STS responses, then authorize requests across native HTTP and raw/typed Gateway entry points.
No live AWS interoperability test has run.

## Construct rules once

Every constructor returns `(authz.Rule, error)`.
Handle configuration errors before serving requests.
Rules are immutable;
value copies share private nodes.
Constructor slices are copied.
Custom callbacks must be safe for concurrent use.

```go
scopes, err := authz.JWTScopes(issuer, "orders.read")
if err != nil {
    return nil, err
}
role, err := authz.IAMRoleSessions("aws", "123456789012", "OrderReader")
if err != nil {
    return nil, err
}
permission, err := authz.Any(scopes, role)
if err != nil {
    return nil, err
}
```

Pass explicit facts after parsing input and selecting the operation:

```go
facts := authz.Request{
    Caller:   identity.FromContext(r.Context()),
    Action:   selectedAction,
    Resource: selectedResource,
}
err := permission.Authorize(r.Context(), facts)
```

Only nil means allow.
Action must be nonempty UTF-8;
Resource is optional UTF-8.
Neither is trimmed, case-folded or interpreted as a path, URL or ARN.
The action header parser has its own stricter HTTP-token grammar.
A resource-aware check must reject an absent resource.
The core uses `Request.Caller` and does not read a second identity from context or install authorization state there.

## Exact predicates

| Constructor | Matches |
| --- | --- |
| `Sources(sources...)` | Any listed nonanonymous identity producer |
| `JWTSubject(issuer, subject)` | Exact issuer and subject |
| `JWTClient(issuer, clientID)` | Exact issuer and client_id |
| `JWTScopes(issuer, required...)` | Exact issuer and every required OAuth scope |
| `CognitoGroups(issuer, required...)` | Exact issuer and every required Cognito group |
| `IAMPrincipal(arn)` | One full, supported caller ARN |
| `IAMRoleSessions(partition, accountID, roleName)` | Any valid session of that exact role name/account/partition |

Identifiers must be nonempty UTF-8.
Source and collection lists must be nonempty;
SourceNone and unknown sources are invalid.
Scopes follow RFC 6749's scope-token grammar.
Duplicate required values do not add requirements.
All comparisons are case-sensitive.
Use `Any` of separate rules for alternative scope/group choices.

Wrong caller kinds, missing claims and unavailable collections do not match.
Flattened gateway groups or `scp` text are never split or reparsed.
Dedicated gateway scopes can be used when supplied through the identity producer.
A scope named `orders/*` matches that literal value, with no wildcard expansion.
Cognito groups, OAuth scopes and server-resolved application grants remain separate facts.

JWT predicates are always issuer-qualified.
Subject does not imply a human;
client_id does not imply a machine grant and is not an audience.
Predicates do not check signature, expiry, audience, token_use or revocation themselves.
The configured authentication producer must establish those guarantees.

Predicates accept every supported producer of their matching kind.
An outer `All(Sources(...), permission)` can restrict provenance.
Sources alone is a broad allow for those producers.
Source is trusted-code attribution: constructing an identity with a source label does not establish credential possession.

`IAMPrincipal` uses `identity.NewIAM`'s caller grammar.
A root ARN allows only that root caller, not its whole account.
An assumed-role ARN includes its exact session name.
Literal `*` and `?` in valid IAM user paths remain literal.
Bare IAM role ARNs are invalid caller forms.

`IAMRoleSessions` deliberately broadens matching to a role's sessions.
It rejects role paths and wildcard configuration.
It preserves the caller's full session ARN without inventing an IAM role ARN.
This is a **role-name policy**: deleting and recreating the role with the same name can match again.
It does not bind a unique role ID, identify the original actor, evaluate session policies or prove execute-api permission.
Lifecycle-sensitive enrollment needs a consumer-owned stable identifier and resolution policy;
PrincipalID remains opaque.

## Ordered composition and application grants

`All` stops at the first nonmatch or error.
`Any` stops at the first match or error.
An error never becomes a nonmatch followed by another permission path.
Thus `Any(allow, unavailable)` allows without running the second check, while `Any(unavailable, allow)` fails without running the second check.
Ordering can change dependency work and outcomes.

Put mandatory application guards outside alternative paths:

```text
All(mandatoryApplicationGuard, Any(jwtPermissionPath, iamPermissionPath))
```

Within a branch, put cheap identity predicates before its provider lookup.
A custom check receives the caller context and explicit action/resource:

```go
grant, err := authz.Check(func(ctx context.Context, req authz.Request) (bool, error) {
    if req.Resource == "" {
        return false, nil
    }
    return store.Allowed(ctx, req.Caller, req.Action, req.Resource)
})
```

The application chooses that store and its enrollment keys.
Use issuer plus subject for subject identity, or explicitly issuer plus client_id for a supported client identity.
Do not silently fall back from missing subject to a user named by client_id, or use email, account or session name as an inferred global key.
Resolve tenant membership, suspension and resource grants in one logical check.
Known unregistered/suspended/unauthorized callers return false, nil.
A dependency that cannot decide returns an error.
Never obtain grants from client-supplied headers or inject them into authenticated claims.

If execution needs a resolved object, resolve it once before the policy and keep the immutable result in the application's own execution plan.
The core provides no automatic grant model, cache or deduplication.
A reused check can run once per evaluated occurrence.
It is not an audit callback.
Checks run synchronously with the caller context;
applications own dependency timeouts and must honor cancellation.
Programmer panics propagate.

Empty All/Any and zero child rules are invalid.
The maximum depth is 32 with a leaf at depth 1.
A rule can contain at most 1,024 expanded nodes, counting every occurrence of a shared subtree.
Constructors reject excess work before evaluation.
These are policy configuration bounds, not AWS quotas or limits on callback work.
There is no Not, anonymous allow rule, retry or parallel evaluation.
Public routes intentionally bypass protected-route authorization.

## Outcomes and HTTP dispatch

Authorize checks rule/non-nil context configuration, cancellation, request validity and nonanonymous identity in that order, then evaluates checks.
Anonymous callers never reach a custom callback, even a permissive one.
Cancellation is checked between nodes and before returning;
it returns `ctx.Err()`, never the private cancellation cause.
It cannot preempt arbitrary callback code.

Every nonnil callback error becomes `ErrUnavailable`, even if the callback returns true, a package sentinel such as ErrDenied, or an internal context error while the caller context remains live.
Return false, nil for denial.
Public errors contain no callback error chain or caller/action/resource diagnostics.

| Result (`errors.Is`) | Consumer HTTP treatment |
| --- | --- |
| nil | Execute the selected action/resource |
| `ErrUnauthenticated` | 401 with configured challenges; normally handled earlier by authn |
| `ErrDenied` | 403; include the applicable Bearer challenge on Bearer-protected routes |
| `ErrUnavailable` | 503 without an authentication challenge |
| `ErrInvalidConfiguration`, `ErrInvalidRequest` | 500; reject malformed client input earlier with an appropriate 4xx |
| Caller cancellation | Never execute; best-effort 503 if transport still permits a response |

For denial on a route configured to accept Bearer, a generic `WWW-Authenticate: Bearer realm="orders"` avoids exposing the reason.
Only use `insufficient_scope` and a scope hint when that is accurately the requirement.
Do not describe every group, tenant or IAM denial as an OAuth scope error.
The example includes a generic Bearer challenge because its route accepts Bearer, including when the current caller used IAM.
The core does not infer a wire authentication scheme from Caller.Kind.
Gateway-only routes own their configured challenge policy.
Preserve CORS, use safe error codes and no-store responses.

The consumer owns an immutable registry entry pairing a rule with its handler.
Parse the action header once, select once, resolve and validate the target, then authorize and execute that same entry/target.
Unknown actions have no unprotected fallback.
Do not reread mutable headers for policy or execution.
Resource version and transaction consistency remain the application's responsibility.

The same handler works through ordinary net/http, `edge.New(handler)` or `edge.NewStreaming(handler)`.
Complete authentication, target resolution and authorization before response commitment or streaming flush.
Ongoing stream reauthorization remains application-owned;
see the [streaming guide](streaming.md).

See [decision 0021](decisions/0021-authorization-rules.md) for the approved design, alternatives and official AWS/IETF evidence.
