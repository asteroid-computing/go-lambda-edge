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
| [0012: Shared identity](0012-shared-identity.md) | Accepted; detailed claims follow-up pending | What caller, claim fidelity, provenance, and producer-composition contracts should be shared? |
| [0013: Invocation errors](0013-public-invocation-errors.md) | Accepted | How should callers inspect sanitized failures across raw, typed, and streaming boundaries? |
