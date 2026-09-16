# 0014: Owned claims, numeric fidelity, and resource limits

Status: proposed on 2026-09-16; awaiting review. Production identity APIs are
unchanged. This completes the claims follow-up explicitly left open by 0012.

## Evidence and scope

The raw decoder keeps authorizer JSON; SDK V1 events contain map[string]any and
SDK V2 JWT claims contain map[string]string. Typed entry points must preserve
their available data without an envelope JSON round trip. The identity package
must remain independent of AWS. Existing invocation errors report byte limits;
their Limit method cannot accurately report a container-depth limit.

We reviewed the Go rules, Go 1.27 release notes, installed package documentation,
SDK v1.55.0 behavior, and AWS documentation through public AWS MCP. The isolated
[probe](../../internal/claimprobe/probe_test.go) and
[measurements](../benchmarks.md#candidate-claims-storage-2026-09-16) inform resource
policy. The probe is deliberately not a production claims implementation.

Sources:

- [Go 1.27](https://go.dev/doc/go1.27) and [JSON v2](https://pkg.go.dev/encoding/json/v2)
- [JSON token representation](https://pkg.go.dev/encoding/json/jsontext#Token.String)
- [SDK WithUseNumber](https://pkg.go.dev/github.com/aws/aws-lambda-go@v1.55.0/lambda#WithUseNumber)
- [Standard numeric data type](https://pkg.go.dev/encoding/json#Number)
- [API Gateway payload formats](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)
- [Gateway JWT validation](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-jwt-authorizer.html)
- [Cognito token verification](https://docs.aws.amazon.com/cognito/latest/developerguide/amazon-cognito-user-pools-using-tokens-verifying-a-jwt.html)
- [JWT claim definitions](https://www.rfc-editor.org/rfc/rfc7519.html)
- [OAuth scope syntax](https://www.rfc-editor.org/rfc/rfc6749#section-3.3)

## C1. Small concrete API with checked reads

Recommend these declarations; they illustrate the proposal, not existing APIs:

```go
func ParseClaims(data jsontext.Value, opts ...ClaimsOption) (Claims, error)
func NewClaims(values map[string]any, opts ...ClaimsOption) (Claims, error)
func NewTextClaims(values map[string]string, opts ...ClaimsOption) (Claims, error)
func WithClaimsBudget(bytes int) ClaimsOption

func (c Claims) Len() int
func (c Claims) Names() []string
func (c Claims) Lookup(name string) (Claim, bool)

func (c Claim) Kind() ClaimKind
func (c Claim) Representation() Representation
func (c Claim) Text() (string, bool)
func (c Claim) Bool() (bool, bool)
func (c Claim) Array() ([]Claim, bool)
func (c Claim) Object() (Claims, bool)
func (c Claim) NumberText() (string, bool)
func (c Claim) Float64() (float64, bool)
```

Claims and Claim have private fields. Zero Claims is an empty object; zero Claim
is invalid, distinct from a present JSON null. ClaimKind distinguishes invalid,
null, boolean, string, number, array and object. Representation distinguishes
unknown, received JSON, supplied decoded values, and gateway text. It describes
the immediate representation, not the original token or verification strength;
Caller.Source continues to describe the identity producer.

Lookup reports presence; the subsequent checked accessor reports whether that
read is possible. Thus missing, null, and wrong type remain distinguishable
without allocating errors for routine inspection. Names returns sorted names;
Array returns a new slice of immutable views. Object shares immutable backing
data. No getter returns a writable internal map or slice.

NumberText succeeds for exact numeric representations, including native integer
inputs, and fails for supplied floating-point inputs even if integral. It never
parses strings. Float64 is an explicitly approximate conversion; it returns
false for nonnumbers or overflow/underflow reported by the standard conversion.
Ordinary representable rounding is permitted. Exact consumers use NumberText and
their chosen numeric parser. Text is deliberately not named String: String() has
an established diagnostic convention in Go.

Reasoning: this supports custom claims without turning identity into a generic
JSON binding library. Reject generic getters, automatic coercion, arbitrary
Decode-into-target hooks, and a mutable map export. A convenience Strings method
can follow demonstrated consumer need; normalized JWT collections already have
their own comma-ok accessors under 0012.

## C2. Accept SDK number preservation without using its codec

The actual SDK handler probe sends the number 9007199254740993 through a typed
V1 event. Default SDK decoding supplies float64 9007199254740992;
lambda.WithUseNumber(true) supplies json.Number containing the original digits.
No downstream adapter can recover the lost digit in the default case.

Recommend accepting encoding/json.Number as an explicitly recognized input
DATA TYPE. This requires a narrow encoding/json import, exclusively to identify
the type and convert its underlying string. Validate that string as exactly one
JSON number using jsontext; never call the v1 encoder, decoder, or compatibility
functions. This preserves the user's direct-v2 processing requirement while
supporting the AWS SDK's existing number-preserving option.

NewClaims accepts only nil, bool, string, built-in signed/unsigned integer types
(excluding uintptr), finite float32/float64, json.Number, map[string]any,
map[string]string, []any and []string, recursively. Custom defined types,
pointers, structs, []byte and custom marshalers are rejected. Type aliases work
as their underlying identical Go type. No arbitrary String or Marshal method is
invoked. Nested string maps passed to NewClaims remain decoded values;
NewTextClaims specifically labels gateway text.

Alternative: reject json.Number and make callers recursively translate SDK
values. That adds work at the exact typed boundary we promised to simplify.
Accepting arbitrary fmt.Stringer values would invoke user code and mistake text
for numbers. Neither is recommended.

Raw JSON preserves numeric token text, semantic JSON types, and decoded string
contents. It does not preserve whitespace, object order, or escape spelling.
This representation must never be substituted for JWT signing bytes. ParseClaims
accepts exactly one object and rejects duplicates, invalid UTF-8, null roots and
trailing values. Typed nil root maps mean empty objects; nested typed nil maps
and slices retain their declared object/array shape as empty collections. A nil
interface is null. This is a declared typed-input contract, not reconstruction
of the upstream JSON null that might have produced a nil container.

## C3. Own inputs and bound expansion before copying

Recommend a 256 KiB default weighted budget, configurable to a positive value
no greater than 6 MiB. There is no unlimited setting. This is an engineering
policy, not an AWS JWT-size quota. The upper setting is an explicit application
opt-in and is not a claim that this much authentication data is ordinary.

Charge 64 bytes for every value occurrence, including root, containers and null,
plus UTF-8 bytes of object names and string values, plus retained exact numeric
text. Native integers charge their decimal representation. Floating-point
storage is covered by the node charge. Count repeated subtrees each time they
will be copied. Unknown claims count equally. Do not refund duplicate values or
claim data omitted from normalized views.

ParseClaims independently limits input byte length to the same configured
maximum before constructing a decoder, then applies the weighted tree budget.
Whitespace and escapes can therefore exhaust its wire allowance even when the
equivalent typed input fits. Typed input has no available original wire length;
we do not manufacture one by marshaling it.

Limit nesting to 64 containers, counting the root as one. This also bounds cyclic
typed input without an allocation-heavy graph traversal. Use subtractive checked
accounting and container-length lower bounds before traversing or allocating.
Preflight typed data before copying, clone strings as well as containers so a
small substring cannot retain an unrelated large backing buffer, and never call
user methods. Inputs must not change concurrently during construction. After
construction, callers may mutate their originals and share claims concurrently.

The candidate's ordinary fixture retained about 2.6 KiB. At a 256 KiB charge,
distinct names retained about 0.72 MiB and many singleton objects about 1.58 MiB.
Thus the charge is not a heap cap. JSON lexical validation, normalized JWT views,
source event storage and the rest of an invocation cost additional memory.
Decoder duplicate detection also allocates before the entire tree is built.
Keep the public accounting stable while permitting private storage improvements;
repeat allocation measurements against production code before public wiring.

Dedicated gateway scopes must not bypass this budget. Propose
edge.WithIdentityClaimsBudget(bytes int) for gateway construction, and carry the
configured budget and consumed charge privately in Claims. NewJWT's explicitly
supplied scope collection consumes the remaining allowance as an array plus its
strings, even when it agrees with a scope claim. Derived normalized views reuse
immutable strings where possible; their allocation is bounded by the input but
is not part of the advertised heap measurement. Supplying duplicate JWT options
still fails under 0012. Claims resource options are configuration, with last
assignment winning like edge options; nil or invalid options fail construction.

Alternative: a raw-byte-only limit misses large typed graphs; an entry-count-only
limit misses large values. A universal heap-byte promise is not supported by the
measurements. Arbitrarily deep or unlimited copying is not recommended.

## C4. Preserve safe, correctly dimensioned errors

Recommend identity.ErrInvalidClaims for malformed JSON, unsupported typed data,
invalid UTF-8, nonfinite numbers and excessive nesting. Recommend
identity.ErrClaimsLimit for wire/weighted byte exhaustion, with an opaque
*identity.ClaimsLimitError exposing Maximum() int64 and unwrapping only to that
sentinel. Construction errors contain no claim names, values, JSON fragments or
wrapped codec errors. Invalid configuration remains a construction error.

At the invocation boundary, claim failure becomes operation identity with
edge.ErrIdentity. Byte exhaustion additionally matches edge.ErrLimitExceeded
and reports Limit() = ("identity_claims", configuredMaximum, true). Depth failure
has no byte-limit metadata: reporting “64 bytes” would be incorrect. Keep detailed
identity errors within identity and translate deliberately at the edge boundary;
do not introduce an AWS or edge dependency into identity.

## C5. Preserve the gateway qualifications while normalizing JWT facts

Received JSON fidelity does not establish original JWT fidelity. In particular,
a gateway-origin string aud or cognito:groups remains available through Text,
but a normalized collection remains unavailable when the source may have
flattened it. Actual arrays can be interpreted according to the claim schema.
This can cause a group/audience-dependent application rule to deny a legitimate
gateway-authenticated request. Recommend accepting that consequence rather than
inventing an undocumented delimiter or trusting an unverified bearer payload.
Local verification or an explicitly reviewed source schema is the remedy.

Interpret canonical scope strings using OAuth's ASCII-space token grammar,
without Unicode-whitespace splitting or case changes. Preserve the original
claim and deduplicate normalized sets deterministically. A present dedicated
gateway scopes array is separately interpretable; an empty array means known
empty, while absent/null means unavailable. Typed SDK nil loses any distinction
between absent and null; a nonnil empty slice preserves known empty.

AWS documents support for scope or scp in gateway authorization but does not give
a universal reversible flattened scp encoding. Recommend recognizing an actual
string array for scp; retain ambiguous gateway text without manufacturing a list.
Any broader issuer-specific scp string convention needs an explicit schema.
Wrong actual types fail under 0012; ambiguity caused by flattening is unavailable.

Retain the accepted rule: prefer dedicated scopes, compare independently
interpretable scope sets, reject disagreement, never union. This is OUR strict
consistency policy, not an AWS-documented guarantee that all authorizer variants
produce equivalent collections. The checked docs establish route validation and
show a scopes array; they do not settle every projection/filtering detail.
Record this as a compatibility qualification in native-producer fixtures. Do not
broaden comparisons to opaque scp text or claim universal gateway coverage.

## Review and implementation order

Recommend approval of C1–C4, and confirmation of C5's explicit compatibility
tradeoffs. After approval:

1. Implement the AWS-independent claims/caller/context foundation, with ownership,
   numerical-fidelity, budget, cycle, type, and sanitized-error contract tests.
2. Specify the native-producer fixture matrix against documented event shapes;
   expose unresolved IAM/V1 producer-recognition details before implementing them.
3. Implement reviewed gateway producers and wire the raw/typed buffered adapter.
4. Follow with local verification/authz and streaming HTTP writer work in the
   accepted order. Do not pick a JWT library or issuer profile in this claims step.

No new production APIs, claim normalization behavior, or resource options have
been implemented by this proposal. No live AWS resources were invoked.
