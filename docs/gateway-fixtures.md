# Gateway identity coverage

The fixtures in gateway_identity_test.go and adapter_invoke_test.go are synthetic and independently authored.
They are not captured AWS events or copied Beakley tests.
No live AWS service is invoked by the tests.

| Cases | Evidence | Qualification |
| --- | --- | --- |
| REST anonymous and claims/scopes null placeholders | [REST proxy documentation](https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html) | API owner, network, pool and access-key metadata never become a caller |
| HTTP API 1.0 placeholders and envelope | [HTTP payload formats](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html) | Authentication combinations use the reviewed shape contract; deployed combinations are unverified |
| V1 IAM and Cognito | REST proxy documentation and [context definitions](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-mapping-template-reference.html) | Exact userArn; caller account differs deliberately from API owner; user differs from signing caller |
| V2 JWT | HTTP payload formats | Valid synthetic issuer/subject replace documentation placeholders; raw arrays/numbers do not imply every gateway preserves them |
| V2 IAM | [SDK v1.55.0 schema](https://github.com/aws/aws-lambda-go/blob/v1.55.0/events/apigw.go) and [SDK IAM fixture](https://github.com/aws/aws-lambda-go/blob/v1.55.0/events/testdata/apigw-v2-request-iam.json) | The SDK fixture has a ten-digit account ID; our authentication fixtures use valid twelve-digit IDs |
| Malformed, ambiguous and unknown assertions | Accepted decisions 0012, 0014 and 0018 | These prove library failure policy, not that AWS generates these events |

Raw and typed paths share native shape recognition, caller construction and resource policy.
Typed V1 claims are decoded values;
typed V2 claims are gateway text.
Array/number fidelity lost by upstream decoding cannot be recovered.
The real Lambda SDK registration tests cover typed V1 WithUseNumber behavior.

SDK decoding also discards unknown V2 authorizer keys.
A raw unknown authorizer fails as unsupported, while its already-decoded empty SDK description may be indistinguishable from an empty authorizer.
Typed calls cannot promise inspection of discarded wire data.
Use the raw Adapter when complete JSON shape validation is required.
Typed SDK V2 input cannot represent the raw array-claim fixture.

In V1, IAM lives outside the authorizer object.
A separate nonempty unknown authorizer still fails as unsupported when IAM is present;
it is not sibling metadata within a recognized native authorizer.
Unknown metadata within a recognized native authorizer is ignored as specified in decision 0018.

The tests exercise gateway identity disabled/enabled, required identity facts, scope consistency and limits, explicit empty producer objects, native/custom collisions and no fallback to a bearer header.
Public invocation tests cover SDK object/function dispatch, binary bodies, cookies, application error responses, request isolation, inherited callers, panic/cancellation and multipart cleanup.

NewIAM's own contract suite covers all four accepted principal forms and literal user-path punctuation.
Gateway fixtures additionally exercise those forms through the invocation boundary, including preserved session IDs and optional principal IDs.

The SDK's upstream decoder owns duplicate/unknown fields and number handling on typed registration;
the caller/runtime also owns the final typed response's serialized envelope limit.
Edge never serializes a typed event to inspect it.
