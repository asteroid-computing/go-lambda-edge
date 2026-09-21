# First v0 milestone

Decision 0023 accepts preparing a consumer milestone before a v1 compatibility
commitment. This checklist records preparation, not authorization to publish:

- Complete application with native/buffered/streaming registration.
- Local tests for both credentials, grant outcomes and streaming lifecycle.
- External-module check using public imports.
- Accurate [support matrix](support.md), [testing guide](consumer-testing.md) and
  [allocation baselines](benchmarks.md).
- Reproducible SDK versions and an isolated Smithy patch update.
- Race tests, vet, module verification, formatting and Linux arm64/amd64 builds.

Release Please and GitHub App authentication are now configured for an initial
v0.1.0 under decision 0024. [The release guide](releases.md) documents the workflow
and required App setup. Merging a generated release PR is the publication decision;
automation does not resolve the remaining readiness items below.

## Outstanding evidence and owner choices

GitHub returned no runs for `work/edge-foundation` during this milestone. The
workflow covers Go 1.27.0/latest 1.27, race tests, vet, module verification,
external consumer checks and both Linux architectures. It runs directly for PRs
against main and is reused by the release workflow on main pushes. Obtain a passing run for
the actual release commit after an authorized push. Local tests do not establish
the Go 1.27.0 matrix result.

Install/configure the release App and provide RELEASE_PLEASE_CLIENT_ID and
RELEASE_PLEASE_PRIVATE_KEY before activating releases. No App credentials or
repository policies were changed during local implementation.

The owner's license and copyright holder remain undecided. No license, repository
visibility change, push, tag or publication has been performed in this milestone.
Resolve the license before external publication.

REST streaming's SDK deployment qualification remains open: local probes cannot
establish AWS acceptance of the observed mode-header/connection behavior.
Authoritative clarification or a separately approved live test is still needed.
Live Cognito/STS interoperability is also unverified; local fixtures are synthetic.
No cloud resources were deployed.

The nested `docs/probes/iamproof` module is historical research. Root tests and
CI do not traverse it. Run its tests explicitly if relying on new probe evidence;
current production proof tests live in the root module's `iamproof` package.
