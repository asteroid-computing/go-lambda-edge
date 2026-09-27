# 0025: Enforce main-branch validation

Status: proposed on 2026-09-27;
awaiting owner review.
No repository policy has been changed.

Owner direction on 2026-09-27: update readiness documentation, then the owner will merge and return to the outstanding AWS qualifications.
Branch protection was not approved as part of that instruction;
this proposal remains unimplemented.

## Evidence

PR #1 at f6a2624469d14a150d89e0f5911644db86449e31 is mergeable, with all four [Go checks passing](https://github.com/asteroid-computing/go-lambda-edge/actions/runs/35619720590).
GitHub's main-branch endpoint reports protected=false, and the effective branch rules endpoint returns no rules.
The PR has no submitted GitHub reviews.

The workflow implements PR validation, but cannot itself require success before merge.
The release workflow validates main again before running Release Please.
[GitHub's protected-branch documentation](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches) supports required PRs, required checks with an expected source, up-to-date branch requirements, and restrictions on force pushes and deletion.
Protection of this private organization repository depends on the organization's GitHub plan;
availability has not been verified.

## Recommendation and alternatives

Before merging, require a PR and these four checks from GitHub Actions on main: test (1.27.0), test (1.27.x), lambda-build (arm64), lambda-build (amd64).
Require the branch to be current with main, block force pushes and deletion, and apply the policy without a routine administrator or release-App bypass.
The App creates release branches and PRs;
it does not need to push directly to main.
Do not introduce a mandatory reviewer count without an owner decision about reviewer availability.

This makes the existing validation an enforced merge condition.
The alternative is to retain manual inspection of checks before every merge;
it avoids repository configuration but permits accidental merges with missing or failing checks.

Consequences: direct main pushes are prevented, and updating a PR after main advances can require another CI run.
Check-name changes must also update policy.
An unavailable repository feature requires an explicit fallback decision.

## Related readiness observations

Refresh the release checklist's historical CI/credential status.
Credentials have been provisioned and PR CI has passed;
App installation/permissions and the main release workflow still require execution evidence.
Squash-merge the foundation with its Conventional Commit feat title, then verify the generated release PR and its checks.
The license and documented live AWS qualifications remain separate release-readiness items under decisions 0023 and 0024.

No merge, policy change, release or live AWS test is authorized by this record.
