# Design decisions

Statuses: proposed, accepted, superseded, or rejected. A proposed record is not
authorization to implement the behavior it describes.

Each substantive decision records the question, relevant code and official
sources, alternatives, recommendation, consequences, and the user's resolution.
Keep related decisions small enough to review and implement independently.

| Record | Status | Question |
| --- | --- | --- |
| [0001: Gateway identity](0001-gateway-identity.md) | Accepted | How should gateway identity be enabled, and which assertions does it cover? |
| [0002: Adapter boundary](0002-adapter-boundary.md) | Proposed | How should construction fail, and which failures become Lambda invocation errors? |
