# Security policy

This module sits on the authentication and authorization path of the applications that use it, so please report suspected vulnerabilities privately.

## Supported versions

Before v1, only the latest minor release receives security fixes.
Fixes ship as a new release;
earlier minor versions are not patched.

## Reporting a vulnerability

Use GitHub's [private vulnerability reporting](https://github.com/asteroid-computing/go-lambda-edge/security/advisories/new) (Security tab, then "Report a vulnerability").
Do not open a public issue, pull request or discussion for a suspected vulnerability.

Include the affected package and version, the impact you expect, and steps to reproduce.
Use synthetic credentials only.
Never send real access tokens, private keys, AWS credentials or signed IAM proofs, even in a private report.

## Scope

In scope:

- Authentication or authorization bypass in `authn`, `iamproof` or `authz`.
- Caller confusion or replacement in `identity` or gateway identity handling.
- Credentials, claims or request values leaking through errors or diagnostics.
- Ways around documented resource limits.
- Action-selection ambiguity in `actionheader` that lets one request act as another.

Out of scope:

- Deployment configuration documented as the consumer's responsibility, such as API Gateway authorizers, CORS or TLS termination.
- Behavior the [support matrix](docs/support.md) already lists as unverified or deferred.
- Vulnerabilities in dependencies; report those upstream, and tell us if this module's use makes them exploitable.

## Process

The maintainer coordinates the fix and disclosure through a GitHub security advisory and credits reporters who want credit.
