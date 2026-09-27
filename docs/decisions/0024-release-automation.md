# 0024: Release Please and pull-request validation

Status: accepted and locally implemented on 2026-09-21. The user approved
GitHub App authentication and initial v0.1.0 following the recommended v0 policy.
Release Please, its GitHub Action and PR checks against main are user-directed.

## Evidence

The existing Go workflow already runs formatting, race tests, vet, module
verification, an external consumer check and Linux arm64/amd64 builds. It previously
ran for pushes and PRs against every branch. There are no local release tags or
release configuration files. The repository secret/variable name listings returned
no entries; no secret values were requested. Organization-scoped credentials may
need separate configuration. This review makes no repository-settings changes.

Official sources checked:

- [Release Please Action v5.0.0](https://github.com/googleapis/release-please-action/releases/tag/v5.0.0)
  is the latest stable GitHub release on this date. The tagged action uses Node 24
  and supports manifest/config inputs, a token and target-branch. Its commit is
  `45996ed1f6d02564a971a2fa1b5860e934307cf7`.
- [Manifest configuration](https://github.com/googleapis/release-please/blob/main/docs/manifest-releaser.md)
  supports the root package, Go strategy, bootstrap version and pre-major bump
  policies. The Go strategy updates a changelog; no generated version.go is needed.
- [GitHub workflow triggers](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow)
  now allow built-in-token PR opened/synchronize/reopened events to create runs
  requiring manual approval. App/PAT tokens allow automatic runs. This is newer
  than Release Please README wording that says such events do not trigger CI.
- [PR events](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request)
  filter by base branch and normally check the proposed merge result.
- [GitHub App token action](https://github.com/actions/create-github-app-token)
  supports short-lived installation tokens scoped to a repository and explicit
  permissions. Latest stable release checked: v3.2.0.

## Accepted PR checks

Use pull_request against main, including opened, synchronize, reopened and
ready_for_review. Retain the existing validation matrix, read-only default token
and checkout without persisted credentials. Cancel superseded runs for the same
PR. Do not use path filters that could omit required checks on release-only PRs.
Main validation moves to the reusable job called by the release workflow.

## Accepted A: Release workflow and credential

Use a dedicated push-to-main release workflow that first calls the Go workflow
as a reusable validation job, then runs Release Please only after success. The Go
workflow retains direct PR triggers and gains workflow_call; remove its direct
push trigger at that point to avoid duplicate main validation. Keep release jobs
serialized without canceling a running release. Human merging of the generated
release PR remains the publication decision; do not enable auto-merge.

Recommend a repository-scoped GitHub App installation token, using a variable
RELEASE_PLEASE_CLIENT_ID and secret RELEASE_PLEASE_PRIVATE_KEY. The current token
action prefers client-id; app-id remains supported but deprecated. Give the app Contents,
Pull requests and Issues write permissions for Release Please operations. Mint
only those permissions for this repository in the release job. The PR validation
workflow needs no app credentials. Pin both new actions to reviewed stable commit
SHAs with version comments; retain existing checkout/setup-go choices.

Alternative: a fine-grained PAT stored as RELEASE_PLEASE_TOKEN, with matching
repository permissions. It works but ties rotation/ownership to a user credential.
The built-in GITHUB_TOKEN avoids an extra credential, but release PR checks need
manual approval and other downstream events remain suppressed. The repository's
current workflow PR-creation/approval setting is disabled, so that route also
needs an owner settings change. Do not silently fall back between token types.

No app, credential, remote PR or release is created during local wiring. App
installation/credential provisioning is an explicit setup prerequisite.

## Accepted B: Initial version and v0 evolution

Recommend one root Go module with ordinary v-prefixed tags, starting at v0.1.0.
Use manifest mode, release-type go, include-component-in-tag false, an empty
initial manifest and initial-version 0.1.0. The v5 action bundles release-please
17.6.0; its tagged schema explicitly supports initial-version. This avoids
pretending an actual earlier release exists.
Set bump-minor-pre-major true and bump-patch-for-minor-pre-major false: breaking
changes and features bump the minor version during v0; fixes bump the patch.
Move to v1 deliberately in a separately reviewed release rather than by an
incidental breaking commit. Do not rewrite the Go toolchain directive or add a
runtime version API. Release Please owns CHANGELOG.md and the version manifest.

Alternative: normal breaking-change-to-major behavior, which can unexpectedly
turn a v0 breaking change into v1.0.0, or permanently forcing release-as, which
must be removed after the intended release. The bundled release-please supports initial-version; use it only for bootstrap.

Use Conventional Commits in merged history. Squash-merge feature PRs with suitable
feat/fix/chore titles; a release PR's generated title/body must be preserved for
Release Please recognition. Publishing creates a Go module tag/GitHub release,
not an npm artifact or deployment of the example binaries.

The existing license and AWS interoperability qualifications remain release-review
items. Configuring release automation does not claim they are resolved, and does
not authorize publishing a release now.

## Implementation and validation

The release workflow calls the existing Go jobs before minting a scoped App token
and invoking Release Please. Go retains direct main-targeted PR events and uses
workflow_call for main validation, with no duplicate push trigger. Both release
and App-token actions use the verified stable commit SHAs. Missing configuration
fails explicitly without printing credentials or falling back to GITHUB_TOKEN.

Root Go manifest configuration selects initial-version 0.1.0, an empty bootstrap
manifest, unprefixed v-tags and the accepted pre-major bump flags. The first
release PR creates CHANGELOG.md. [The release guide](../releases.md) documents
App setup, permissions, checks, human publication review and remaining limits.

Both workflows pass local actionlint v1.7.12 and diff whitespace checks. JSON
configuration validates against the bundled release-please 17.6.0 schema. Local
structure checks confirm PR triggers, reusable validation, release's dependency
on validation, scoped credential use, and unchanged Go test/build jobs. No Go
source changed, so runtime suites were not rerun for this workflow-only change.
That local implementation did not install an App, provision credentials, dispatch
a workflow, change repository settings or publish a release.

Subsequent authorized setup, verified on 2026-09-27: the repository variable
RELEASE_PLEASE_CLIENT_ID and encrypted Actions secret RELEASE_PLEASE_PRIVATE_KEY
are provisioned. The working branch was pushed and PR #1 opened.
[All four PR checks at f6a2624 passed](https://github.com/asteroid-computing/go-lambda-edge/actions/runs/35619720590).
App installation/permissions and the main-branch release path still need execution
evidence after merge. No release has been published.
