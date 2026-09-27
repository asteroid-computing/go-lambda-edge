# 0015: Initial IAM caller forms

Status: accepted on 2026-09-16, including the four initial caller forms, with
approval of decision 0017. Constructor/view implementation is complete.

Decision 0012 requires validation of an actual caller principal form and explicitly
defers the supported-form fixtures. Implementing NewIAM now reaches that gate.
Claims implementation under accepted decision 0014 proceeds independently.

## Evidence

The [AWS Principal documentation](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_principal.html),
read through public AWS MCP, distinguishes IAM users, account/root principals,
roles, assumed-role sessions, and STS federated-user sessions. It explains that
using assumed-role credentials makes the requester a role session principal.
Policy principal syntax also includes service and identity-provider principals;
these are not all interchangeable with an authenticated requester's ARN.

The [AWS principal-key table](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_variables.html)
distinguishes root, user, federated user and assumed-role requesters and preserves
session identifiers. Existing edge code has no IAM producer to dictate a broader
compatibility requirement. Identity must remain independent of AWS libraries.

## Recommendation

Initially accept these exact forms in NewIAM:

- IAM root: arn:<partition>:iam::<account>:root.
- IAM user: arn:<partition>:iam::<account>:user/<path-and-name>.
- STS assumed-role session: arn:<partition>:sts::<account>:assumed-role/<role>/<session>.
- STS federated-user session: arn:<partition>:sts::<account>:federated-user/<name>.

Preserve spelling, partition, account and full resource including session. Reject
bare account IDs, IAM role ARNs, wildcard expressions outside literal user paths and policy-only principal forms in this
initial request-caller constructor. Do not transform a role session into a role.
Recognizing root is a representation capability, not permission to perform an
application action. Root policy-principal delegation semantics are not applied.

Use read-only IAM accessors PrincipalARN, Partition, AccountID, Service and
PrincipalType, with optional PrincipalID. WithIAMAccountID cross-checks an
independently supplied caller account; WithIAMPrincipalID preserves the optional
opaque identifier without parsing it into an identity. Duplicate assignments
fail under 0012. Never use API-owner account metadata as caller account.

ARN lexical details and field limits must follow the relevant official API
definitions when implemented; this proposal does not invent normalization or
claim a universal API Gateway producer matrix.

## Alternative and consequence

Accepting every resource-policy Principal value would represent policy subjects
that are not the request caller the constructor describes. Accepting a bare role
ARN could also hide upstream loss of session identity. Recommend adding another
form only when a documented producer needs it, with an explicit fixture.

This review gates the IAM constructor and complete caller union; it does not gate
the approved claims API, resource limits, or numerical fidelity implementation.

## Clarification: validation versus authentication

The user asked what IAM validation can actually establish and whether authn can
authenticate an IAM token. Constructor validation means structural validity and
internal consistency only: ARN components, a supported principal resource form,
required names/session components, and agreement with an independently supplied
caller account. It cannot establish existence, possession of credentials, session
validity, permissions, or that the supplied event came from API Gateway. An ARN
and a self-declared source are identifiers/annotations, not authentication proofs.

The [IAM identifiers reference](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_identifiers.html)
documents ARN components, blank regions for these IAM/STS forms, and exact
session representations. The initial supported-form restriction is our caller
model policy, not a test proving a principal is real.

[Gateway IAM authorization](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-access-control-iam.html)
requires signed requests and checks execute-api permission before invoking a
route. Recommend that path for IAM clients of edge's API Gateway integration:
AWS authenticates the request; edge validates and owns the resulting assertion.
This uses built-in AWS_IAM authorization, not a Lambda/JWT authorizer. Trust still
depends on the intended integration and permissions controlling Lambda invocation.

An STS SessionToken alone is not a standalone bearer authentication protocol.
[Temporary credentials](https://aws.amazon.com/blogs/security/understanding-the-api-options-for-securely-delegating-access-to-your-aws-account/)
include an access key, secret key and session token; the keys sign requests and
the session token accompanies them. Calling
[GetCallerIdentity](https://docs.aws.amazon.com/STS/latest/APIReference/API_GetCallerIdentity.html)
with the Lambda's credentials identifies the Lambda's role, not the HTTP caller.

IAM-backed bearer authentication is possible with a specifically designed proof
protocol. [EKS documents](https://docs.aws.amazon.com/eks/latest/best-practices/identity-and-access-management.html)
a token containing a presigned STS request that its authenticator submits to AWS
for verification. A future edge authn component could use this general approach,
but would need its own explicit protocol, application binding, freshness/replay
policy, restricted STS endpoints and dependency-error handling. An arbitrary
execute-api signature is not interchangeable with an STS-signed proof.

Recommend keeping that optional online authentication producer separate from
NewIAM and the gateway assertion adapter. It would also require a reviewed source
designation; the current source contract must not mislabel AWS verification as
locally verified JWT authentication. No such producer is approved or implemented.

The user's subsequent same-route IAM/OAuth clarification is recorded in 0016.
For that goal, the proposed path is explicit mixed-credential middleware with an
online IAM proof verifier. Built-in AWS_IAM alone does not satisfy the goal.

## Accepted lexical refinement and implementation

On 2026-09-16 the user approved preserving literal `*` and `?` in valid IAM user
paths. [CreateUser](https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreateUser.html)
permits these characters in its path grammar. Caller identifiers are exact facts,
never policy patterns. Wildcard account IDs, usernames, role names and session
names remain invalid. Follow the formal path pattern's U+0021..U+007E range;
the page's prose mentions DEL but its formal pattern excludes it.

The constructor follows documented name/path lengths: IAM username 1..64 and
path 1..512; role name 1..64; session name 2..64 under
[AssumeRole](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html);
federated username 2..32 under
[GetFederationToken](https://docs.aws.amazon.com/STS/latest/APIReference/API_GetFederationToken.html).
The account is exactly twelve ASCII digits and the ARN's region is empty.
Partition validation is lexical, not an assertion that an AWS partition exists;
the authenticating producer must enforce its configured partition.

NewIAM and its read-only view are implemented with account consistency,
duplicate-option rejection, exact session preservation and sanitized diagnostics.
Online verification is not implemented by this constructor.
