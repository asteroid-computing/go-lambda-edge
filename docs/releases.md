# Releases

Release Please manages this single root Go module. Its first release is
`v0.1.0`. While versions remain below v1, features and breaking changes bump the
minor version; fixes bump the patch version. A v1 release is an explicit review
decision. The Go toolchain directive is independent of the module version.

## Workflow

1. Opening or updating a PR against main runs the Go workflow. Reopening a PR
   or marking it ready for review also runs checks. Superseded PR runs cancel.
2. A push to main starts Release Please's workflow. It calls the same Go workflow
   for formatting, race tests, vet, module verification, external consumption and
   Linux arm64/amd64 builds. No separate push-triggered Go run duplicates it.
3. Only after validation succeeds does the release job mint a GitHub App token
   and run Release Please. It creates or updates a release PR with CHANGELOG.md
   and the version manifest. The App's PR events can run CI automatically.
4. Review and merge that release PR when ready to publish. After the main checks
   succeed, Release Please creates the version tag and GitHub release. There is
   no automatic merge, binary deployment or separate package-registry upload.

Release runs share one concurrency group and do not cancel a running release.
GitHub may replace an older pending run with a newer one; this is not a guarantee
of one release operation per push. Release Please reconciles the main branch.

The configuration is [release-please-config.json](../release-please-config.json).
The [initial manifest](../.release-please-manifest.json) is empty because no release
exists yet. `initial-version` selects 0.1.0 only for the first release. Release
Please subsequently maintains the manifest; it does not force every release
back to 0.1.0. Plain `vX.Y.Z` tags identify this root module, without a component
prefix. CHANGELOG.md is created by the first release PR.

## GitHub App setup

Use an organization-owned App installed on `asteroid-computing/go-lambda-edge`.
Its repository permissions must include:

- Contents: read and write.
- Pull requests: read and write.
- Issues: read and write, for release labels and related operations.

In this repository's **Settings → Secrets and variables → Actions**, configure:

| Kind | Name | Value |
| --- | --- | --- |
| Variable | `RELEASE_PLEASE_CLIENT_ID` | The App's Client ID |
| Secret | `RELEASE_PLEASE_PRIVATE_KEY` | Its PEM private key |

Organization-level settings are also usable when explicitly granted to this
repository. Use the Client ID, not the installation ID; the current token action
prefers `client-id` over its deprecated `app-id` input. Store the private key as
an Actions secret, not in this repository.

The workflow explicitly requests only this repository and the three permissions
above. The token action revokes its short-lived token after the job. The release
job's built-in GITHUB_TOKEN has no permissions; validation gets contents read and
no inherited App secrets. Missing App configuration fails with a setup message
instead of silently using a different credential.

An App must be installed and allowed by organization policy before this works.
The workflow does not create/install the App, provision secrets, change rulesets,
or bypass branch protection. Configure required PR checks using the check names
observed in the first successful run, and review any existing rules that restrict
the App's release-branch or version-tag writes. PR checks use the proposed merge
result; main checks validate the push that triggered the release workflow.

## Commit and release review

Use Conventional Commits in merged history: `feat:`, `fix:` and explicit `!` or
`BREAKING CHANGE:` markers where appropriate. Squash-merge feature PRs with an
appropriate conventional title. Preserve Release Please's generated release PR
title/body and labels so it can recognize the merged release. Ordinary docs/CI
changes alone need not create a release PR.

Merging the release PR is the publication decision. Confirm the
[release-readiness prerequisites](release-readiness.md), including the license
choice and accurate AWS support claims, before merging it. Those are human
review items, not automatically verified by the workflow. No live AWS tests run
in this automation, and a passing CI run does not close deployment qualifications.

## Maintenance

All published actions are pinned to reviewed commit SHAs with version comments.
The latest stable upstream releases were verified on 2026-09-21:

| Action | Stable release |
| --- | --- |
| actions/checkout | [v7.0.1](https://github.com/actions/checkout/releases/tag/v7.0.1) |
| actions/setup-go | [v7.0.0](https://github.com/actions/setup-go/releases/tag/v7.0.0) |
| actions/create-github-app-token | [v3.2.0](https://github.com/actions/create-github-app-token/releases/tag/v3.2.0) |
| googleapis/release-please-action | [v5.0.0](https://github.com/googleapis/release-please-action/releases/tag/v5.0.0) |

The previous checkout/setup-go v7 aliases resolved to these same commits when
checked; pinning makes that selection reproducible. Review upstream release notes
and resolve each release tag to its commit when updating the SHA and version
comment together. The config schema is pinned to release-please 17.6.0, bundled
by the release action. No runtime version API or generated version.go is required.

Validate workflow edits locally with `actionlint`; validate config edits against
the referenced JSON schema. A real GitHub run is still needed to verify App
installation, permissions, repository policies and remote release recognition.

References: [Release Please action](https://github.com/googleapis/release-please-action/tree/v5.0.0),
[manifest configuration](https://github.com/googleapis/release-please/blob/v17.6.0/docs/manifest-releaser.md),
[App token action](https://github.com/actions/create-github-app-token/tree/v3.2.0),
[reusable workflows](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows).
