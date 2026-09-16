# 0015: Initial IAM caller forms

Status: proposed on 2026-09-16; awaiting review.

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
bare account IDs, IAM role ARNs, wildcards and policy-only principal forms in this
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
