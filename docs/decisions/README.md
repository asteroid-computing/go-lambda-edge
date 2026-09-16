# Design decisions

Statuses: proposed, accepted, superseded, or rejected. A proposed record is not
authorization to implement the behavior it describes.

Each substantive decision records the question, relevant code and official
sources, alternatives, recommendation, consequences, and the user's resolution.
Keep related decisions small enough to review and implement independently.

| Record | Status | Question |
| --- | --- | --- |
| [0001: Gateway identity](0001-gateway-identity.md) | Accepted | How should gateway identity be enabled, and which assertions does it cover? |
| [0002: Adapter boundary](0002-adapter-boundary.md) | Accepted | How should construction fail, and which failures become Lambda invocation errors? |
| [0003: Event decoding](0003-event-decoding.md) | Accepted | How should SDK types, dispatch, and wire validation work together? |
| [0004: Typed events](0004-typed-events.md) | Accepted | How can already typed events bypass edge's envelope JSON work? |
| [0005: HTTP requests](0005-http-request-translation.md) | Accepted | How should raw and typed events become ordinary server requests? |
| [0006: Buffered responses](0006-buffered-responses.md) | Accepted | How should HTTP responses, unsupported capabilities, and completion failures behave? |
| [0007: Response metadata](0007-response-metadata.md) | Qualification resolved by 0010 | What metadata budgets and V2 joinable-field set should apply? |
| [0008: Streaming architecture](0008-streaming-architecture.md) | Accepted | How should REST response streaming fit alongside the buffered adapter? |
| [0009: Streaming boundaries](0009-streaming-boundaries.md) | Accepted | What API, lifecycle, handoff, and error rules should streaming use? |
| [0010: Response header design](0010-response-header-design.md) | Accepted | How should shared snapshots, V2 combination, resource accounting, and streaming prefix limits work? |
| [0011: Request header processing](0011-request-header-processing.md) | Accepted | How can consumers process custom headers and safely select actions for their own dispatchers? |
| [0012: Shared identity](0012-shared-identity.md) | Accepted; caller/context foundation implemented | What caller, claim fidelity, provenance, and producer-composition contracts should be shared? |
| [0013: Invocation errors](0013-public-invocation-errors.md) | Accepted | How should callers inspect sanitized failures across raw, typed, and streaming boundaries? |
| [0014: Claims API](0014-claims-api.md) | Accepted; claims foundation implemented | Which checked claim reads, SDK numeric inputs, ownership rules, and measured resource limits should identity expose? |
| [0015: IAM caller forms](0015-iam-caller-forms.md) | Accepted | Which ARN forms represent an authenticated request caller in the initial IAM constructor? |
| [0016: Mixed IAM/OAuth authentication](0016-mixed-iam-oauth-authentication.md) | Architecture accepted; IAM proof and HTTP selector implemented; first-party JWT verification pending | How can one route accept IAM credentials or OAuth tokens without built-in AWS_IAM authorization? |
| [0017: IAM proof protocol](0017-iam-proof-protocol.md) | Accepted; generator and verifier implemented with local validation | What helper/verifier API, wire format, destination binding, replay policy and STS boundary should the IAM path use? |
| [0018: Gateway identity recognition](0018-gateway-identity-recognition.md) | Accepted; extraction and buffered invocation implemented | Which native field mappings, presence rules and fixture qualifications should precede public invocation wiring? |
| [0019: HTTP authentication selector](0019-http-authentication-selector.md) | Accepted; selector, middleware and examples implemented | What public selector API, verifier/error boundary, header policy and HTTP challenges should compose IAM proof and Bearer authentication? |
