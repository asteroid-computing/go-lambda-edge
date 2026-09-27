# 0026: Exported API review against Google Go style

Status: proposed on 2026-09-27;
awaiting owner review.
No exported API has changed.
Each numbered recommendation can be approved, redirected or rejected independently.

## Scope and method

The review covers every exported identifier in the five public packages: `edge`, `identity`, `authn`, `iamproof` and `authz`, as reported by `go doc -all` at the base of this branch.
Internal packages and `examples/` are evidence of consumer use, not review subjects.

The basis is Google's Go style guide, read in full, and the primary sources it links:

- [Style guide](https://google.github.io/styleguide/go/guide) (normative and canonical), [style decisions](https://google.github.io/styleguide/go/decisions) (normative) and [best practices](https://google.github.io/styleguide/go/best-practices) (auxiliary).
  Where they differ, the guide takes precedence over decisions, and decisions over best practices.
- [Effective Go](https://go.dev/doc/effective_go), [Go Doc Comments](https://go.dev/doc/comment), [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments), [Package names](https://go.dev/blog/package-names), [Organizing Go code](https://go.dev/blog/organizing-go-code), [Working with errors in Go 1.13](https://go.dev/blog/go1.13-errors), [Contexts and structs](https://go.dev/blog/context-and-structs), [Go concurrency patterns: Context](https://go.dev/blog/context) and [Testable examples](https://go.dev/blog/examples).

Effective Go describes itself as a 2009 document that is not actively updated;
newer, more specific sources win where they disagree.

The module has no release tag and an empty release manifest.
Renames and package moves therefore break no published consumer, but that stops being true after 0.1.0.
Each recommendation below was checked against the accepted decision records;
where one reopens an accepted decision, that is stated explicitly.

## Summary

| ID | Recommendation | Breaking | Priority |
| --- | --- | --- | --- |
| E1 | Move the action-header selector out of the root `edge` package | Yes | High |
| E2 | Give the action sentinels consistent names | Yes | High (with E1) |
| E3 | Rename `identity.WithCaller` to `identity.NewContext` | Yes | Medium |
| E4 | Type the `InvocationError` operation and limit names | Mostly source-compatible | Medium |
| E5 | Add `String` methods to the `identity` enumerations | No | Medium |
| E6 | Complete doc comments for sentinels, links, callback parameters and paired results | No | Medium |
| E7 | Move the Cognito verifier into its own package | Yes | Low; owner judgment |

Items considered and not recommended are listed after E7, with reasons, so the review can be checked for omissions.

## E1. Move the action-header selector out of the root `edge` package

### Evidence

`action.go` imports only `errors`, `net/http` and `strings`.
[Decision 0011](0011-request-header-processing.md) says the selector "needs no context, AWS event types, or JSON processing", yet placed it in the root package.
The root package is otherwise the Lambda transport adapter, and imports `github.com/aws/aws-lambda-go/events`.

As a result, a native HTTP server that only needs the selector links the whole Lambda adapter.
`go list -deps ./examples/dispatcher/native` includes `github.com/aws/aws-lambda-go/events` and the root adapter package, although the native entry point never touches Lambda;
its only use of `edge` is `edge.NewActionHeader` in `examples/dispatcher/internal/orders/app.go`.

### Recommendation

Create a standard-library-only package for action selection and move `ActionHeader`, its constructor and its sentinels there unchanged in behavior.
Proposed shape:

```go
package actionheader

var (
    ErrMissing   = errors.New("actionheader: missing action")
    ErrAmbiguous = errors.New("actionheader: ambiguous action")
    ErrInvalid   = errors.New("actionheader: invalid action")
)

type Selector struct{ /* unexported */ }

func NewSelector(name string) (Selector, error)
func (s Selector) Parse(headers http.Header) (string, error)
```

### Reasoning

- The style guide's [maintainability](https://google.github.io/styleguide/go/guide#maintainability) principle: maintainable code "minimizes its dependencies".
  The only dependency the selector needs is `net/http`.
- [Package size](https://google.github.io/styleguide/go/best-practices#package-size) suggests merging packages when a user "must import both packages in order to use either in any meaningful way".
  The inverse applies here: selector users need nothing else from the adapter, and adapter users do not need the selector.
  A conceptually distinct idea gets "its own small package".
- [Package names](https://go.dev/blog/package-names) warns that a package whose name does not make a meaningful prefix for its contents has the wrong boundary.
  `edge.ActionHeader` in a Lambda-adapter package is exactly that case.
- AGENTS.md asks that transport, identity, authentication and authorization stay separate.
  Action selection is request dispatch, not Lambda transport.

Naming: `action` would be the most natural package name, but it collides with the local variable `action` used throughout consumer code and examples.
[Package names](https://google.github.io/styleguide/go/decisions#package-names) says to avoid "package names that are likely to be shadowed by commonly used local variable names".
`actionheader` avoids that.
Inside it, the exported names drop the repeated context ([package vs. exported symbol name](https://google.github.io/styleguide/go/decisions#package-vs-exported-symbol-name)): `actionheader.ErrMissing` rather than `actionheader.ErrActionMissing`.
The constructor is `NewSelector` rather than `New`, because the returned type is not named after the package ([Package names](https://go.dev/blog/package-names#naming-package-contents): `time.NewTimer`, not `time.New`).

### Alternatives

- Keep the selector in `edge`.
  This costs nothing now, but it permanently couples native HTTP consumers to the Lambda adapter's dependencies.
  A later move would break published consumers.
- Put the selector in `authz`.
  [Decision 0021](0021-authorization-rules.md) keeps `authz` free of HTTP concerns, so this would blur an accepted boundary.
- Put the selector in `authn`.
  Selecting an action is not authentication.

### Consequences

This reopens part of decision 0011, which said: "Do not add an edge.Action type, global action context key, or action package at this stage."
That sentence was about action _metadata_ ownership;
the proposed package moves only the existing selector, adds no action type or context key, and keeps metadata consumer-owned.
The owner should still confirm that the move is compatible with 0011's intent.

Examples, guides, the support matrix and the external consumer check need their imports updated.
Behavior, errors and tests move unchanged apart from names.

## E2. Give the action sentinels consistent names

### Evidence

Across the module, sentinel names put the adjective first: `edge.ErrInvalidEvent`, `edge.ErrUnsupportedEvent`, `edge.ErrInvalidInvocation`, `authn.ErrMissingCredentials`, `authn.ErrInvalidCredentials` and `identity.ErrInvalidCaller`.
The three action sentinels put the noun first: `ErrActionMissing`, `ErrActionAmbiguous` and `ErrActionInvalid`.

### Recommendation

With E1: `actionheader.ErrMissing`, `ErrAmbiguous` and `ErrInvalid`, as shown above.
Without E1: rename them in `edge` to `ErrMissingAction`, `ErrAmbiguousAction` and `ErrInvalidAction`.

### Reasoning

[Consistency](https://google.github.io/styleguide/go/guide#consistency) notes it "can be very jarring … if the same concept has many names", and consistency within a package is the most immediate level.
[Maintainability](https://google.github.io/styleguide/go/guide#maintainability) asks for predictable names: a user who knows `ErrInvalidEvent` should be able to guess `ErrInvalidAction`.

### Consequences

This is a rename only; the error strings and meanings are unchanged.

## E3. Rename `identity.WithCaller` to `identity.NewContext`

### Evidence

Every other exported `With…` function in the module is a functional option:
`identity.WithIAMAccountID`, `WithIAMPrincipalID`, `WithGatewayScopes`, `WithClaimsBudget`, `edge.WithGatewayIdentity`, `WithResponseHeaderBudget`, `WithIdentityClaimsBudget`, `WithStreamErrorReporter`, `iamproof.WithTimeout` and `WithTransport`.
`identity.WithCaller(ctx, caller) (context.Context, error)` is the one exception.
In `identity`'s godoc it sits beside four same-looking option constructors.

### Recommendation

Rename it `NewContext`, keeping its signature and semantics, so it pairs with the existing `FromContext`:

```go
func NewContext(ctx context.Context, caller Caller) (context.Context, error)
func FromContext(ctx context.Context) Caller
```

### Reasoning

The Go blog's context article defines this exact pair for a package that transports a value in a context: `NewContext` "returns a new Context that carries a provided" value, and `FromContext` extracts it ([Go concurrency patterns: Context](https://go.dev/blog/context)).
[Predictable names](https://google.github.io/styleguide/go/guide#maintainability): a reader who sees `FromContext` expects `NewContext`.
Within this module, the `With` prefix has come to signal "option"; one exception weakens that signal ([consistency](https://google.github.io/styleguide/go/guide#consistency)).

### Alternatives

Keep `WithCaller`.
The standard library's `context.WithValue` also uses `With` for a derived context, so the current name is defensible;
the recommendation rests on the module-local convention and the blog's canonical pair, not on a rule.

### Consequences

Update `authn`'s handler, examples, guides and decision 0012's cross-reference.
The error-returning signature stays, because conflict detection is a security invariant (decision 0012).

## E4. Type the `InvocationError` operation and limit names

### Evidence

`(*InvocationError).Operation() string` returns one of eight words: validate, decode, request, identity, response, encode, cleanup or stream.
`Limit() (name string, maximum int64, ok bool)` returns one of five names: response_headers, buffered_body, buffered_envelope, stream_metadata or identity_claims.
Both sets exist only in prose;
internally they are string literals passed to `invocationError` and `limitError`.
A consumer that branches on them has to copy literals, and a typo compiles and silently never matches.

### Recommendation

```go
type Operation string

const (
    OperationValidate Operation = "validate"
    OperationDecode   Operation = "decode"
    // … one constant per documented operation
)

type Resource string

const (
    ResourceResponseHeaders  Resource = "response_headers"
    ResourceBufferedBody     Resource = "buffered_body"
    // … one constant per documented limit
)

func (e *InvocationError) Operation() Operation
func (e *InvocationError) Limit() (resource Resource, maximum int64, ok bool)
```

### Reasoning

[Error structure](https://google.github.io/styleguide/go/best-practices#error-structure): information callers need programmatically "should ideally be presented structurally", with `os.PathError` as the model.
The accessors already exist;
typed constants make the documented value sets part of the checked API instead of prose.
[Working with errors in Go 1.13](https://go.dev/blog/go1.13-errors#errors-and-package-apis) asks packages to document which error properties callers may rely on;
exported constants are the most direct form of that.
[Constant names](https://google.github.io/styleguide/go/decisions#constant-names): name by role (`ResourceBufferedBody`), not by value.

### Alternatives

- Export untyped string constants and keep the `string` results.
  That is fully source-compatible but gives no type checking.
- Keep prose only.

### Consequences

Comparisons against untyped string literals (`e.Operation() == "decode"`) keep compiling.
Code that passes a result where a `string` is required needs a conversion.
The diagnostic text of `Error()` is unchanged.

## E5. Add `String` methods to the `identity` enumerations

### Evidence

`Kind`, `Source`, `ClaimKind`, `Representation` and `IAMPrincipalType` have no `String` method.
`Caller.String` builds its text with `strconv.Itoa`, so a caller prints as `identity.Caller{kind:2,source:4}`.
Any `%v` of these values in logs or test failures prints a bare number.

### Recommendation

Add a `String` method to each type that returns a stable lowercase word (for example `iam`, `verified_iam_proof`, `gateway_text`), and `Kind(7)`-style text for values outside the declared set.
Make `Caller.String` use them.

### Reasoning

[Useful test failures](https://google.github.io/styleguide/go/decisions#useful-test-failures): "It should be possible to diagnose a test's failure without reading the test's source."
Consumers comparing `Kind` or `Source` in their own tests currently get numbers.
[Clarity](https://google.github.io/styleguide/go/guide#clarity): the reader should not need to reverse-engineer a value.
Effective Go's printing section uses a `String` method for exactly this purpose.
These values carry no identifiers or claims, so the change keeps `identity`'s rule that diagnostics omit facts.

### Consequences

`Caller.String` output changes.
The package already documents diagnostics as non-contractual, so this is not a compatibility break.
Add tests for every declared value and one undeclared value.

## E6. Complete doc comments for sentinels, links, callback parameters and paired results

### Evidence

- `authn` (8), `iamproof` (5) and `authz` (5) sentinels share a single group sentence each, while `edge` and `identity` document every sentinel.
  The meaning of each `authn` sentinel, including its [0019](0019-http-authentication-selector.md) HTTP mapping (400, 401, 431, 500 or 503), is currently written down only in the decision record and the guide.
- Go doc links (`[Name]`) appear 7 times in `edge`, 2 in `identity`, once in `authn` and never in `iamproof` or `authz`.
  Names such as `ErrInvalidCredentials` and `Token.Value` are therefore not links on pkg.go.dev.
- The callback types `authn.VerifyFunc` and `authz.CheckFunc` have unnamed parameters, so godoc shows `func(context.Context, string)` without saying that the string is the credential.
- `identity.Claim.Bool` returns `(bool, bool)`.

### Recommendation

- Give each sentinel its own doc comment that states when it is returned and, for `authn`, the HTTP status its `*Error` maps to.
- Use doc links for cross-references to identifiers in the same and other module packages.
- Name the callback parameters, for example `func(ctx context.Context, credential string) (identity.Caller, error)`.
- Name `Claim.Bool`'s results `(value, ok bool)`.

### Reasoning

- [Doc comments](https://google.github.io/styleguide/go/decisions#doc-comments): "All top-level exported names must have doc comments."
  [Go Doc Comments](https://go.dev/doc/comment#var) does accept a group comment for a set of sentinels, so the current form is valid upstream Go;
  the recommendation follows Google's stricter rule and this module's own `edge`/`identity` practice.
- [Documentation conventions: errors](https://google.github.io/styleguide/go/best-practices#errors): "Document significant error sentinel values … so that callers can anticipate" the conditions they can handle.
- [Go Doc Comments: doc links](https://go.dev/doc/comment#doclinks) makes cross-references navigable, and [Preview](https://google.github.io/styleguide/go/best-practices#preview) recommends checking the rendered result.
- [Named result parameters](https://google.github.io/styleguide/go/decisions#named-result-parameters): "If a function returns two or more parameters of the same type, adding names can be useful."
  The same reasoning applies to parameters of func types, which godoc renders without any other context.

### Consequences

These are documentation and naming changes only, with no behavior or compatibility change.
Parameter and result names are not part of a Go type's identity.

## E7. Move the Cognito verifier into its own package

### Evidence

`authn` exports `CognitoConfig`, `CognitoClient`, `CognitoVerifier` and `NewCognitoVerifier`.
The repeated prefix marks a sub-domain: local RS256 and JWKS verification sits inside a package whose other exports are HTTP credential selection.
The module's other verifier, IAM proofs, is already its own package (`iamproof`).

### Recommendation

Move it to `authn/cognito`:

```go
package cognito

type Config struct{ /* unchanged fields */ }
type AppClient struct{ ClientID, Audience string; AllowUnbound bool }
type Verifier struct{ /* unexported */ }

func NewVerifier(cfg Config) (*Verifier, error)
func (v *Verifier) Verify(ctx context.Context, token string) (identity.Caller, error)
func (v *Verifier) Warm(ctx context.Context) error
```

The new package imports `authn` only for `ErrInvalidCredentials`, `ErrUnavailable` and `ErrInvalidConfiguration`.
It keeps returning them, so `Verifier.Verify` still plugs into `authn.Config.Bearer` directly.
`authn` does not import `cognito`, so there is no cycle.

### Reasoning

- [Repetition](https://google.github.io/styleguide/go/decisions#repetition) and [avoid repetition](https://google.github.io/styleguide/go/best-practices#avoid-repetition): a shared prefix on several names is context that belongs in the package name (`cognito.Verifier`, not `authn.CognitoVerifier`).
- [Package size](https://google.github.io/styleguide/go/best-practices#package-size): "When something is conceptually distinct, giving it its own small package can make it easier to use."
- The two verifiers would have symmetric layouts.
- `AppClient` instead of `Client` avoids a reader confusing the policy type with an AWS SDK Cognito client.
  [Package names](https://go.dev/blog/package-names#bad-package-names) advises against names that collide with packages commonly used alongside yours.

### Alternatives

Keep the current layout.
[Decision 0020](0020-cognito-verification.md) placed the verifier in `authn` to add it "without changing these boundaries", and did not compare it with a separate package.
The current layout means one fewer import for Bearer users and no churn.

### Consequences

This is the most discretionary item: a structural improvement, not a correctness issue.
It changes import paths and names for every Cognito consumer, and its benefit is mostly godoc organization and naming.

## Considered, not recommended

- **Keep `(*authn.Authenticator).Handler(next) (http.Handler, error)`.**
  Returning a plain `http.Handler` and panicking on a nil `next` would give the conventional `func(http.Handler) http.Handler` middleware shape, and [when to panic](https://google.github.io/styleguide/go/best-practices#when-to-panic) notes that the standard library panics on API misuse.
  But every constructor in this module reports invalid input as an error (`edge.New` rejects nil handlers the same way), and [decision 0019](0019-http-authentication-selector.md) approved this signature.
  Consistency wins; a consumer can adapt it in two lines.
- **Keep the explicit `iamproof.ErrInvalidProof` to `authn.ErrInvalidCredentials` adapter.**
  Passing `proofVerifier.Verify` directly compiles and turns invalid proofs into 503 responses.
  That is the module's clearest remaining misuse trap, and [simplicity](https://google.github.io/styleguide/go/guide#simplicity) accepts extra code "so that the end user of the API may more easily call the API correctly".
  However, decision 0019 A3 weighed and rejected the alternatives: making `iamproof` import `authn` "reverses its transport-independent boundary", and a classification protocol adds machinery.
  The review found no new evidence against that trade-off, so it is listed only for completeness.
- **Keep a single `edge.Option` type, even though `New` rejects `WithStreamErrorReporter` at run time.**
  A separate `StreamingOption` type would catch the misuse at compile time, but it adds a type and duplicated option constructors for a single streaming-only option ([least mechanism](https://google.github.io/styleguide/go/guide#least-mechanism)).
  Revisit this if more streaming-only options appear.
- **Keep the per-package configuration styles.**
  `authn.Config` and `CognitoConfig` are option structs;
  `edge`, `identity` and `iamproof` use variadic options.
  This matches the selection criteria in [option structure](https://google.github.io/styleguide/go/best-practices#option-structure) and [variadic options](https://google.github.io/styleguide/go/best-practices#variadic-options).
  Structs fit when every caller supplies several required values (verifiers, issuer, clients).
  Options fit when most callers use defaults.
  Options already take values rather than using presence as a signal (`WithGatewayIdentity(enabled bool)`), as that section asks.
- **No `authz.Must` helper.**
  Composing rules needs one error check per constructor, but rule inputs normally come from runtime configuration.
  [Must functions](https://google.github.io/styleguide/go/decisions#must-functions) are for program start-up with known-good inputs, "not on things like user input".
- **Keep `iamproof.NewGenerator` and `NewVerifier` taking `(region, audience string, …)` positionally.**
  [Function argument lists](https://google.github.io/styleguide/go/best-practices#function-argument-lists) warns that adjacent same-typed parameters are easy to swap.
  Here the risk is contained: the region must match a region pattern and resolve to a standard STS endpoint, so swapped arguments fail construction unless the audience is itself region-shaped.

## Already conformant

- Package names are short, lowercase single words without stutter.
  The `go-lambda-edge` path with package `edge` follows the common `go-` repository convention;
  documentation already shows the import.
- Constructors follow `New` for a package's primary type (`edge.New`, `authn.New`) and `NewT` otherwise.
  There are no `Get` prefixes, and initialisms are consistent (`AccountID`, `PrincipalARN`, `ClientID`, `CacheTTL`).
- Functions return concrete types and accept small function types instead of exported interfaces ([interfaces](https://google.github.io/styleguide/go/decisions#interfaces)).
  Error results are typed `error`, with documented `*InvocationError`, `*authn.Error` and `*identity.ClaimsLimitError` ([returning errors](https://google.github.io/styleguide/go/decisions#returning-errors)).
- `context.Context` is always the first parameter and is never stored in an exported struct ([contexts](https://google.github.io/styleguide/go/decisions#contexts)).
- There is no mutable package-level state ([global state](https://google.github.io/styleguide/go/best-practices#global-state)).
- Concurrency, cleanup, zero values and context behavior are documented wherever they are non-obvious ([documentation conventions](https://google.github.io/styleguide/go/best-practices#conventions)).
  Comma-ok results replace in-band sentinels ([in-band errors](https://google.github.io/styleguide/go/decisions#in-band-errors)).
- Every public package has runnable examples.

## Related observation: comment line length

The previous change moved comments to one sentence per line.
[Go Doc Comments](https://go.dev/doc/comment) explicitly accepts semantic linefeeds.
Google's [comment line length](https://google.github.io/styleguide/go/decisions#comment-line-length) decision prefers wrapping long comment lines and calls a whole paragraph on one line a poor reading experience, while setting no column width.
One sentence per line avoids that failure mode, but 46 of the 1,020 `//` comment lines exceed 120 characters (one exceeds 200).
An optional follow-up could add clause-level breaks (SemBr rule 2) to those sentences, without returning to column wrapping.

## Open questions

1. Does E1 conflict with the intent of decision 0011's "no action package" sentence, or only with its wording?
2. For E1, is `actionheader` acceptable, or does the owner prefer another name that avoids shadowing `action`?
3. Should approved items land as one API-change PR before 0.1.0, or as one stacked PR per item?

## Resolution

Pending owner review.
