# 0018: Native gateway identity recognition and fixture coverage

Status: accepted on 2026-09-16.
The user approved G1-G4 and authorized native gateway extraction and buffered invocation wiring.
Both are implemented and tested.
This is the producer-fixture follow-up explicitly reserved in 0012 and 0014.

## Evidence and existing boundary

Official AWS documentation was checked through public AWS MCP.
Current decode.go retains raw authorizer JSON separately from the SDK structs.
V1 typed events have an authorizer map;
V2 typed events have JWT, IAM and Lambda authorizer members.
The root option still defaults gateway identity off.
NewIAM/NewJWT now validate owned facts, and the shared invocation guard rejects an inherited caller.

- [REST proxy integration](https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html) documents IAM identity fields, Cognito authorizer.claims, and custom-authorizer principalId/context.
  Its example includes null claims/scopes placeholders.
- [HTTP proxy payload formats](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html) documents both payload versions and authorizer.jwt.claims/scopes in 2.0.
  The 1.0 example has null placeholders, not a complete authenticated producer matrix.
- [Context variable definitions](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-mapping-template-reference.html) distinguish API-owner accountId from identity.accountId;
  identity.caller is the signing principal, identity.user the principal authorized against resources, and identity.userArn the effective authenticated user's ARN.
- [AWS Lambda Go V2 definitions](https://github.com/aws/aws-lambda-go/blob/v1.55.0/events/apigw.go) and its [IAM fixture](https://github.com/aws/aws-lambda-go/blob/v1.55.0/events/testdata/apigw-v2-request-iam.json) establish the iam member's userArn, accountId, userId and callerId field names.
  That fixture uses a ten-digit account ID: it is serialization/schema evidence, not a valid identity fixture or proof that two principal IDs are equivalent.

Gateway assertions are trusted because the application opts into its intended integration and controls Lambda invocation permissions.
Event data cannot prove its own provenance or reveal every deployment setting.
This proposal does not turn source tags, fields or an Authorization header into cryptographic evidence.

## G1. Recognize shapes without guessing the API product

Recommend these mappings;
all paths are under requestContext:

| Payload shape | IAM caller | JWT caller |
| --- | --- | --- |
| V1 / 1.0 | identity.userArn; optional identity.accountId and identity.user | authorizer.claims plus optional authorizer.scopes |
| V2 / 2.0 | authorizer.iam.userArn; optional iam.accountId and iam.userId | authorizer.jwt.claims plus optional jwt.scopes |

Use the selected shape consistently for both raw and typed inputs.
Do not infer REST versus HTTP API from payload version, invent another HTTP 1.0 claim encoding, or probe arbitrary nested claim maps until something looks like a caller.

For V1 native claims, source documentation directly establishes the REST Cognito path.
For HTTP 1.0, the structure is documented but the authenticated matrix is not fully established.
Recommend implementing the same explicit shape contract with synthetic fixtures and documenting that qualification.
Do not advertise deployed coverage of every HTTP 1.0 authorizer combination.
An unexpected nonempty authorizer fails as unsupported;
it must not become anonymous.

Alternative: restrict the whole V1 transport to REST, or infer identity from token headers when the assertion differs.
The former discards an accepted transport surface and the latter changes the trust boundary.
Neither is recommended.

## G2. Effective user fields and partial IAM assertions

Require the exact ARN for IAM construction.
Cross-check a nonempty account field from the identity/iam object;
never pass the API-owner accountId as caller account.
Preserve V1 user or V2 userId as the optional opaque principal ID.
Do not substitute caller/callerId or require equality with it: the documented roles differ.
If user/userId is missing, leave PrincipalID unavailable rather than inventing it.

For V1, nonempty userArn, user or caller signals an IAM candidate.
A candidate with missing/invalid userArn fails with ErrIdentity and identity.ErrInvalidCaller.
Account/access-key/Cognito-pool/network/API-key/mTLS metadata alone does not establish IAM identity.
It remains metadata;
authorization sees anonymous.
For V2, a nonnull iam object is a candidate even when empty.
Missing ARN fails.
Validate consumed scalar fields as strings or null;
reject wrong types without printing their contents.
Raw empty optional strings match the SDK's empty values.

This is a field-mapping policy, not a claim that a valid ARN authenticates itself.
The four accepted NewIAM forms and their literal path semantics apply unchanged.

## G3. Absence, empty assertions and collisions

Recommend the following precise presence policy:

| Input with gateway identity enabled | Outcome |
| --- | --- |
| Missing/null authorizer, or empty untagged object, with no IAM candidate | Anonymous |
| V1 only claims/scopes missing or null | Anonymous; documented placeholder |
| V1 nonnull claims or scopes | JWT candidate; missing/empty required identity facts fail |
| V2 nonnull jwt object, including an empty object or null claims/scopes inside it | JWT candidate; missing identity facts fail |
| V2 nonnull iam object, including an empty object | IAM candidate; missing identity facts fail |
| Nonnull V1 principalId or V2 lambda member, including an empty custom context | Custom candidate; unsupported until a mapper is reviewed |
| Multiple native/custom candidates | ErrIdentity with identity.ErrConflict |
| Other nonempty unrecognized authorizer | ErrIdentity with errors.ErrUnsupported |

The V2 rule clarifies 0012's null-placeholder language: a nonnull tagged producer object expresses more intent than the documented V1 placeholder.
Treat an empty tagged assertion as broken integration data, rather than silently unauthenticated.
Null V2 members are absent.
Unknown sibling metadata alongside a supported native candidate is ignored for identity, as accepted in 0012;
explicit native/custom producer markers still collide.
Inspect all markers before selecting a producer.

Alternative: interpret every empty object as anonymous.
That can conceal a configured producer that failed to supply its required facts.
Recommend failing closed for explicit tagged producers while keeping the documented anonymous events valid.

With gateway identity disabled, none of these fields produces a caller and no identity interpretation runs.
Structural event JSON/type checks still apply as already specified;
opting out does not make malformed event JSON valid.

## G4. Preserve fidelity and make test coverage honest

Raw claims use ParseClaims on the retained claim-object bytes;
V1 typed claims use NewClaims directly;
V2 typed claims use NewTextClaims.
Do not serialize typed events to recover unavailable presence or numeric information.
Normalize all of them with SourceGatewayAssertion and the accepted C5 collection qualifications.
Dedicated scopes use WithGatewayScopes and the configured remaining claims budget.
Claim budget exhaustion maps to ErrIdentity/ErrLimitExceeded and identity_claims.

SDK typed decoding may already discard unknown authorizer members and collapse null/absent scalar fields.
Preserve raw/typed equivalence where the SDK retained the distinction, and explicitly test/document the exceptions.
Prefer the raw entry point when full wire-shape inspection is required.
Do not manufacture an unknown custom producer from a zero SDK description after its fields were lost.

Prepare independently authored fixtures labeled by evidence level:

- Documentation-shaped anonymous V1/HTTP 1.0 placeholders and V2 JWT objects;
  replace documentation placeholder claims with valid synthetic iss/sub/client_id.
- SDK-shaped V2 IAM with valid twelve-digit caller account, explicit cross-account API owner, all four principal forms, and distinct userId/callerId values.
- REST Cognito/IAM cases using documented field meanings;
  label constructed values synthetic, never as captured live traffic.
- HTTP 1.0 native cases labeled shape-contract tests, with deployed coverage unverified;
  include an unexpected-shape rejection case.
- Adversarial collisions, partial/empty assertions, custom/unknown producers, invalid types, claim/scope budget boundaries, flattened permissions and inherited caller rejection, in both gateway modes and raw/typed paths where representable.

The fixture suite must prove no handler runs after identity failure and no identity is derived from a bearer header.
Test actual SDK registration only after the shared producers are reviewed and wired.
No live deployment is required by default;
a future separately authorized capture can improve the coverage matrix.

## Resolution

The user approved G1-G4 on 2026-09-16 and authorized implementation.
Existing identity constructors, native extraction and buffered public invocation are now implemented.
Raw and typed paths share marker discovery and construction policy;
typed events are never serialized to inspect them.
Scope preflight prevents large temporary collections from bypassing the configured budget before NewJWT checks their combined charge with claims.

The [fixture inventory](../gateway-fixtures.md) labels documented shapes, SDK schema evidence, synthetic/adversarial cases and unverified deployed coverage.
Tests exercise all four IAM forms, optional effective-user IDs, source conflicts, raw numeric fidelity, flattened V2 text, owned inputs and discarded unknown SDK fields.
Actual Lambda SDK wrappers cover raw object and both typed registrations, including V1 number preservation.
Full race tests, vet and Lambda builds pass.
The raw identity-boundary fuzz target completed 482,700 synthetic executions in an initial ten-second run without failures.
Public raw/typed allocation baselines are recorded in docs/benchmarks.md, including binary and near-limit payloads.
