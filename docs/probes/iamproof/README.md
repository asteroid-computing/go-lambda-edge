# IAM proof design probe

This standalone Go 1.27 module is research for proposed decision 0017, not a
production helper/verifier. It uses synthetic credentials and makes no AWS calls.
Its SDK requirements do not change the parent module's dependencies. Root
`go test ./...` and current CI do not traverse this nested module.

Run from this directory:

```sh
go test -v -race ./...
go vet ./...
```

The probe checks that AWS SDK v2 signing with an application header yields the
expected signed-header list, that a JSON v2 envelope can reconstruct all signed
URL parameters, and that changing the application header changes the signature.
Query ordering is normalized for comparison: SigV4 sorts parameters, whereas
the SDK appends the signature after signing. No signature is recreated by a
server credential in the reconstruction step.

For standard commercial, GovCloud and China regional endpoints, all twelve
cases pass. These are SDK endpoint/signing checks, not deployed partition or
STS interoperability certification. No proof validation, freshness enforcement,
HTTP verification, response parsing or public API is implemented here.

Measured token bytes for eu-west-2 and the audience `orders.production`:

| Synthetic session token bytes | Compact JSON envelope | Base64url of full signed URL |
| --- | ---: | ---: |
| 0 | 282 | 493 |
| 1,024 | 1,671 | 3,935 |
| 4,096 | 5,767 | 14,175 |
| 6,144 | 8,498 | 21,002 |

Both columns include a three-byte version prefix, but exclude the HTTP scheme
and field name. Session strings deliberately repeat `+/=a` to exercise URL
escaping. These are synthetic stress cases, not observed AWS token distributions
or a promised upper bound. Actual audiences, credential scopes, and session
tokens change the sizes. The last compact case exceeds the proposed 8 KiB limit.

Latest stable versions were resolved on 2026-09-16 with `go list -m -json`:
AWS SDK core v1.47.0 and STS v1.51.0. Explicit versions and go.sum make this
experiment reproducible; they are not a policy to avoid future stable upgrades.
