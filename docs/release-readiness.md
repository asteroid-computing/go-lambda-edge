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

## Verified readiness (2026-09-27)

[PR #1](https://github.com/asteroid-computing/go-lambda-edge/pull/1) merged on
2026-09-27. All four jobs in
[the run for f6a2624](https://github.com/asteroid-computing/go-lambda-edge/actions/runs/35619720590)
passed: test (1.27.0), test (1.27.x), lambda-build (arm64) and lambda-build (amd64).
This provides GitHub evidence for formatting, race tests, vet, module
verification, external consumer checks and both Linux builds. Later commits
require their own passing PR checks. Per the user's 2026-09-27 direction, Go
validation runs only on PRs; main pushes run only release automation.

The repository variable RELEASE_PLEASE_CLIENT_ID and encrypted Actions secret
RELEASE_PLEASE_PRIVATE_KEY are provisioned; their names were verified on
2026-09-27 without reading the private key. No branch protection was configured.

## Outstanding evidence and owner choices

The [first main run](https://github.com/asteroid-computing/go-lambda-edge/actions/runs/36281181036)
passed the original repeated validation but failed at App token creation: the
repository installation lookup returned 404. The organization installation list
is empty. Install the App on asteroid-computing with access to go-lambda-edge and
the documented permissions, then verify token creation, the generated v0.1.0
release PR and its checks. Release Please has not yet executed successfully.

The owner's license and copyright holder remain undecided. No license, repository
visibility change, release tag or release publication has been performed.
Resolve the license before external publication.

REST streaming's SDK deployment qualification remains open: local probes cannot
establish AWS acceptance of the observed mode-header/connection behavior.
Authoritative clarification or a separately approved live test is still needed.
Live Cognito/STS interoperability is also unverified; local fixtures are synthetic.
No cloud resources were deployed.

The nested `docs/probes/iamproof` module is historical research. Root tests and
CI do not traverse it. Run its tests explicitly if relying on new probe evidence;
current production proof tests live in the root module's `iamproof` package.
