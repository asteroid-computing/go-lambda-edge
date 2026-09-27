# Repository Guidelines

## Purpose and working agreement

Build `github.com/asteroid-computing/go-lambda-edge` from first principles.
The root package is `edge`;
ordinary `http.Handler` is the application boundary.
Beakley is reference material for requirements and failure cases, not source or tests to copy, nor a compatibility target.

Read [the implementation plan](docs/plan.md) and relevant [decision records](docs/decisions/README.md) before work.
Use the [support matrix](docs/support.md) to distinguish implemented behavior, deferred scope and deployment qualifications.
Keep accepted direction, proposed API contracts and implemented behavior clearly separated.

## Repository map

- Root Go files: raw/typed Lambda adapters, Gateway event decoding, HTTP request translation, buffered/streaming responses, gateway identity and action headers.
- `identity/`: immutable callers, owned claims and context transport;
  standard library only, with no AWS dependencies.
- `authn/`: credential selection, HTTP authentication middleware, Cognito token verification and JWKS caching.
- `iamproof/`: exported signed STS GetCallerIdentity proof generator and verifier.
- `authz/`: identity predicates, ordered rules and application grant callbacks.
  Consumers own grant resolution, action registries and HTTP dispatch.
- `examples/dispatcher/`: complete application with native HTTP, buffered Lambda and streaming Lambda entry points;
  application details stay in its `internal/`.
- Tests live beside code in `*_test.go`;
  `testdata/` contains fixtures and the external consumer source.
  `scripts/check-consumer.sh` tests public imports from a temporary, separate module.
- `docs/`: guides, evidence, benchmarks, plan and decisions.
  The nested module in `docs/probes/iamproof/` is historical research, outside root `go test ./...`.
- `.github/workflows/`: PR validation and Release Please.
  Release configuration lives in `release-please-config.json` and `.release-please-manifest.json`.

## Local commands and CI

Use Go 1.27 or later, preserving the `go 1.27.0` baseline.
Run commands from the repository root.
There is no Makefile or required custom commit helper.

| Task | Command |
| --- | --- |
| Format changed Go files | `gofmt -w <changed.go> ...` |
| Check repository formatting | `gofmt -l .` (expect no output) |
| Focused package test | `go test ./authn -run TestName` (choose the affected package/test) |
| Full race suite | `go test -race ./...` |
| Static checks | `go vet ./...` |
| Dependency integrity | `go mod verify` |
| External consumer check | `sh scripts/check-consumer.sh` |
| Linux arm64 build | `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...` |
| Linux amd64 build | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` |
| Workflow lint | `actionlint .github/workflows/go.yml .github/workflows/release-please.yml` |
| Whitespace check | `git diff --check` |

Start with checks relevant to the change.
For Go changes, complete the applicable race, vet, module, consumer and cross-build checks before handoff.
Documentation edits need link/path and whitespace checks;
workflow edits need actionlint.
Do not rerun the full Go suite for documentation-only changes without a reason.
Report what ran and any unverified checks.

PR CI tests Go 1.27.0 and the latest 1.27 patch, and builds both Linux architectures.
Go validation runs on PRs against `main`, including release PRs.
Main pushes run release automation only;
do not reintroduce duplicate post-merge validation without user direction.

## Go conventions and package boundaries

- Use `encoding/json/v2` and `encoding/json/jsontext` directly for module-owned JSON.
  Do not use the JSON v1 compatibility codec.
  Recognition of the v1 `json.Number` type for SDK-supplied values is an existing interoperability case.
- Follow surrounding Go naming and error conventions;
  document exported APIs and provide runnable examples where they help consumers compose the packages.
- Keep transport, identity, authentication and authorization separate.
  Do not turn the root package into a facade reexporting all companion types.
- Preserve gateway identity as explicit opt-in, validated caller invariants, immutable claims and deny-by-default authorization.
  Identity construction is not credential verification or permission assignment.
- Distinguish HTTP responses from invocation failures, and denial from dependency failure.
  Preserve cancellation, resource bounds, cleanup and error sanitization.
- Prefer AWS SDK types when they meet the accepted contract.
  Keep raw and typed adapter behavior aligned without adding an envelope JSON round trip to typed entry points.
  API product and Gateway payload version are separate concepts.
- Check official Go documentation and the supplied Go release-notes/API skills when applicable.
  Verify latest stable dependency/action releases with upstream sources when updating them;
  retain reproducible module versions and action commit pins with version comments.

## Tests and evidence

- Exercise observable contracts and failure cases, not private implementation details.
  Use the standard testing package, local HTTP test servers and recorded/synthetic events;
  document fixture provenance and fidelity limits.
- Cover malformed input, ambiguous credentials, denied access, dependency errors, cancellation and cleanup where relevant.
  Streaming tests should consume data incrementally and exercise handoff and terminal failures.
- Prefer public-package examples and the external consumer check for API changes.
  Root tests do not run nested research modules;
  run those explicitly only when relying on their evidence.
- Do not deploy resources or invoke live AWS services merely to run unit tests.
  Live interoperability work needs a separately approved scope.
  Local tests do not establish deployed streaming, Cognito or STS compatibility;
  keep evidence qualifications accurate in the support and release-readiness documentation.

## Design decisions and user review

When a substantive question, design decision or issue arises:

1. Inspect our code and tests and consult relevant official documentation.
   Route AWS knowledge lookups through `aws-proxy-public`, not account APIs.
   Use official Go documentation and the supplied Go skills when applicable.
2. Record evidence, alternatives, recommendation, reasoning and consequences in a decision record.
   Label unverified assumptions and open questions;
   maintain the decision index.
3. Present the issue and recommendation to the user for review.
4. Await approval or redirection before implementing dependent behavior.
   Continue useful independent work within the accepted plan.
5. Record the resolution and update the plan before implementation.

Silence is not approval.
Routine execution within an approved decision needs no additional approval.
Do not silently settle public API, trust-boundary, compatibility or error-policy decisions.

## Branches, commits and releases

- Work on a working branch, never directly on `main`.
  Inspect the working tree first;
  preserve unrelated edits and editor files.
  Stage only intended files.
- Keep commits focused and use Conventional Commits.
  Use `feat:` for features, `fix:` for fixes and `docs:`/`ci:` for their actual scope;
  do not relabel routine maintenance merely to force a release.
- PR descriptions should explain the resulting behavior, validation and material limitations.
  For a review request, inspect with `gh pr view` and `gh pr diff` before deciding whether any checkout or edits are needed.
- Prefer squash merges with a Conventional Commit title.
  Creating or updating a PR does not authorize merging it or publishing a release.
- Release Please owns the changelog, release manifest, tags and release PRs;
  do not manually create an Unreleased changelog section or force versions unless directed.
  It considers unreleased history, not just the newest commit.
- Follow [the release guide](docs/releases.md) and [readiness checklist](docs/release-readiness.md).
  The initial version is 0.1.0;
  features/breaking changes bump minor during v0, fixes bump patch, and moving to v1 requires an explicit decision.
  Merging a release PR authorizes publication.

## Credentials and configuration

- Never commit or print private keys, access tokens, bearer credentials or signed IAM proofs.
  Keep fixtures synthetic and sanitize diagnostic output.
- Release automation uses organization Actions secrets `RELEASE_PLEASE_CLIENT_ID` and `RELEASE_PLEASE_PRIVATE_KEY`, granted to this repository.
  Avoid same-name repository secrets that override organization values.
- Keep the release App token scoped to the current repository and required permissions.
  PR validation needs no App credentials.
  Do not substitute a different credential or broaden access to work around a configuration failure.
- Keep application configuration and credential handling in consumer code;
  examples and docs must make deployment responsibilities explicit.
