# Working agreement

Build a new Go module from first principles. Beakley is reference material for
requirements and failure cases, not a source to copy and update.

The planned root package is `edge`. Target Go 1.27 from the start and use
`encoding/json/v2` directly for JSON handled by this module.

Read [the implementation plan](docs/plan.md) and relevant
[decision records](docs/decisions/README.md) before work. Preserve the distinction
between accepted direction, proposed API details, and implemented behavior.

## Decisions and user review

When a substantive question, design decision, or issue arises:

1. Inspect our code and tests and consult the relevant official documentation.
   Route AWS knowledge lookups through `aws-proxy-public`; do not use account
   APIs for documentation research. Use official Go documentation and the
   supplied Go skills when applicable.
2. Record the evidence, alternatives, recommendation, reasoning, and consequences
   in a decision record. Label unverified assumptions and open questions.
3. Present the question or issue and the recommendation to the user for review.
4. Await the user's approval or redirection before implementing the dependent
   behavior. Continue useful independent work within the accepted plan.
5. Record the user's resolution and update the plan before implementing it.

Do not interpret silence as approval. Routine execution within an approved
decision does not require another approval. Do not silently settle public API,
trust-boundary, compatibility, or error-policy decisions.

## Implementation and validation

- Work on a working branch, not `main`.
- Keep transport, identity, authentication, and authorization responsibilities
  distinct. Keep `identity` free of AWS dependencies.
- Preserve denied-by-default authorization and validate identity invariants.
- Test external contracts rather than mirroring implementation details.
- Use recorded/synthetic events and local test servers; do not deploy resources
  or invoke live AWS services merely to run unit tests.
- Keep documentation and decision status accurate as work progresses.
- Do not copy Beakley source or tests into this repository.
