# Performance baselines

Initial response-codec measurements on 2026-09-14: Go 1.27.1, darwin/arm64,
Apple M2. Command: `go test -run '^$' -bench '^BenchmarkMarshalResponse$'
-benchmem -benchtime=200ms`.

| Case | Time/op | Allocated bytes/op | Allocations/op |
| --- | ---: | ---: | ---: |
| Small JSON body in V2 envelope | 623 ns | 696 | 4 |
| Text body 1 KiB below 6 MiB | 5.71 ms | 18,875,672 | 11 |
| NUL body whose JSON escaping exceeds envelope limit | 5.25 ms | 27,559,434 | 19 |

These are exploratory local samples, not CI thresholds or Lambda performance
claims. Allocated bytes/op measures cumulative allocations, not peak live memory.
The bounded destination retains at most 6 MiB, but JSON codec temporaries still
allocate. Do not advertise a 6 MiB total-process or peak-memory guarantee.

Raw/typed adapter comparisons, binary response benchmarks, and peak-memory
measurements follow when the complete response and invocation paths are wired.

## Response-header allocation probe

2026-09-15, Go 1.27.1, darwin/arm64, Apple M2. This is an isolated candidate
algorithm in [internal/headerprobe](../internal/headerprobe/probe_test.go), not
production edge behavior. It performs weighted preflight, preallocated key
sorting, canonicalization, merging and outer SP/HTAB trimming. It deliberately
omits syntax validation, hop-header filtering, automatic headers and transport
projection; the real writer will have additional costs.

Commands (three samples each):

```sh
go test ./internal/headerprobe -run 'TestSnapshotMemory|TestPreflightBoundaries' -v -count=3
go test ./internal/headerprobe -run '^$' -bench '^BenchmarkSnapshot$' -benchmem -benchtime=200ms -count=3
```

Median benchmark results and approximate retained heap after GC:

| Fixture | Time/op | Cumulative allocation/op | Additional retained heap |
| --- | ---: | ---: | ---: |
| Ordinary headers and two cookies | 487 ns | 576 B | Small; 512–5,832 B observed |
| 190,650 empty values under one name | 1.00 ms | 3,056,017 B | 3,056,016 B |
| 157,286 distinct names, near 6 MiB charge | 41.69 ms | 17,633,168 B | 15,110,032 B |
| 26,214 distinct names, near 1 MiB charge | 4.87 ms | 2,429,490 B | 1,993,632 B |
| 6,553 distinct names, near 256 KiB charge | 1.04 ms | 607,486 B | 498,432 B |
| 157,286 suppressed names, near 6 MiB charge | 35.13 ms | 15,116,592 B | 12,593,456 B |
| Mixed-case aliases, near 6 MiB charge | 1.16 ms | 4,473,267 B | 3,178,912–3,181,320 B |
| One quote-filled value, near 6 MiB charge | 191 ns | 416 B | 416 B |
| Empty values exceeding budget by one entry | 390 µs | 0 B | 0–112 B noise |

The smaller fixtures use the same algorithm and allocate inputs whose weighted
charge fits the named candidate budget. The prototype still enforces the original
6 MiB ceiling; it does not implement the proposed configurable production option.
Over-budget preflight rejection allocates no snapshot. The quote-filled value
shares immutable string storage with the still-live input; the tiny snapshot
allocation does not include a copy of its bytes. Unbounded JSON v2 encoding of
that snapshot alone produces 12,582,856 bytes, demonstrating why exact encoded
limits remain independent of the weighted charge.

Memory samples keep both the original header map and snapshot alive across
measurement. Two garbage collections before and after clear previous cases'
sync.Pool buffers. Small deltas remain noisy. Reported retained heap excludes the
application's existing input and is not peak memory, total process memory or RSS.
Encoding happens outside the snapshot memory sample. The suppressed-name case
retains all suppression entries; production may eliminate some once automatic
header decisions finish. Timings are local samples, not Lambda latency claims.

Preallocating the sorted key slice reduced cumulative allocation for the largest
distinct-name case from roughly 29.35 MB to 17.63 MB; retained map/slice costs
still dominate. This optimization alone does not make a 6 MiB accounting budget
an equivalent heap bound.

Recommendation for review: use a 256 KiB weighted default and an explicit override
up to 6 MiB, preserving separate encoded limits. The lower default targets modest
additional metadata storage; its exact number is an engineering choice informed
by the measured 0.48 MiB retained/0.58 MiB cumulative high-cardinality case, not
an AWS quota or a proven universal memory bound. See
[decision 0010](decisions/0010-response-header-design.md).

The isolated probe passes race detection, vet and formatting/diff checks. No
production adapter code changed, and no live AWS services were invoked.

## Implemented response-header layer

After approval of decision 0010, repeated measurements against the actual
snapshot and projection functions on the same Go 1.27.1 / Apple M2 environment:

```sh
go test -run '^TestResponseHeaderRetainedMemory$' -v -count=3
go test -run '^$' -bench '^BenchmarkResponseHeaders$' -benchmem -benchtime=200ms -count=3
```

Median cumulative allocations in bytes per operation; projection columns include
snapshot construction and independently owned projection storage:

| Fixture | Snapshot only | Snapshot + V1 | Snapshot + V2 | Retained snapshot heap |
| --- | ---: | ---: | ---: | ---: |
| Ordinary headers/cookies | 600 | 712 | 1,016 | 728–1,048 |
| 6,553 distinct names near default 256 KiB charge | 607,557 | 1,106,130 | 937,527 | 498,648–498,760 |
| 157,286 distinct names near explicit 6 MiB charge | 17,633,192 | 32,743,176 | 28,129,496 | 15,110,248–15,126,208 |

The default's high-cardinality retained snapshot remains about 0.48 MiB, matching
the candidate probe. Projections add allocations, especially with the largest
opt-in budget. Suppressed entries are omitted from output; projection maps are
sized for represented fields, not the number of suppression markers.

These helper benchmarks exclude the HTTP writer, body handling and final JSON
encoding. Compiler escape analysis can also differ once they are called through
the future public invocation path. Retained heap excludes the application input
and projections; it is not peak memory or RSS. Median snapshot timings were
approximately 1.36 µs, 1.27 ms and 52.17 ms respectively, with substantial timing
variation in the largest case. These are exploratory samples, not CI thresholds
or Lambda latency claims. They do not warrant changing the approved default.

## Candidate claims storage (2026-09-16)

The isolated `internal/claimprobe` test package measures a candidate owned tree,
not an implemented identity API. Go 1.27.1, darwin/arm64, Apple M2; three samples:

```sh
go test ./internal/claimprobe -run '^TestSnapshotMemory$' -v -count=3
go test ./internal/claimprobe -run '^$' -bench 'Benchmark(Snapshot|JSONScan)$' -benchmem -benchtime=100ms -count=3
```

Median snapshot results, with retained heap measured separately:

| Fixture | Weighted charge (bytes) | Time | Cumulative B/op | Allocs/op | Retained heap (bytes) |
| --- | ---: | ---: | ---: | ---: | ---: |
| Ordinary claims | 1,185 | 1.25 µs | 2,691 | 29 | 2,696 |
| Distinct names, 256 KiB allowance | 262,144 | 266 µs | 750,387 | 3,658 | 750,384 |
| Distinct names, 1 MiB allowance | 1,048,528 | 1.19 ms | 3,001,434 | 14,628 | 3,001,424 |
| Distinct names, 6 MiB allowance | 6,291,424 | 8.63 ms | 12,238,702 | 87,638 | 12,238,688 |
| Array of nulls | 262,085 | 56.1 µs | 262,902 | 4 | 262,912 |
| Array of singleton objects | 262,132 | 599 µs | 1,661,213 | 6,097 | 1,661,184 |
| Large string | 262,144 | 23.3 µs | 262,902 | 4 | 262,912 |
| 64 nested objects | 4,417 | 16.5 µs | 48,388 | 193 | 48,400 |

Depth 65, a cyclic map, an exponentially expanded shared subtree and an oversized
array all reject before snapshot allocation: 0 B/op and 0 allocs/op in these
samples. This excludes any eventual public error construction. Shared subtrees
are charged per occurrence, not per distinct pointer. Rejection of that fixture
took about 20.6 µs; the container-length fast rejection took about 46 ns.

The separate lexical JSON scan used direct jsontext, including strict duplicate
name validation, a wire-length check, an object root and a depth bound. Median
cumulative allocations were 1,200 bytes for ordinary claims, 692,717 for distinct
names, 524,696 for a large string and 12,904 for depth 64. These are lexical scan
costs only: no full raw-JSON-to-owned-tree implementation exists yet. Do not add
the timings to predict production latency or call lexical allocation a heap cap.

The tree copies map keys, strings, maps and slices. Inputs remain live across
memory samples. Two garbage collections before and after each snapshot reduce
pool retention; small deltas remain noisy. Retained heap excludes input storage,
transient allocation, normalized JWT views, event decoding, and process RSS. The
singleton-object case shows why a 256 KiB accounting limit can retain 1.58 MiB.
No sample establishes the worst possible amplification across all shapes.

The prototype covers a subset of the proposed typed inputs, using float64 for
decoded numbers. It does not implement exact numeric nodes, provenance, the
public getters, JWT validation, error translation, or dedicated scope accounting.
Those require production measurements and contract tests after approval.

The SDK probe invokes a real local lambda.NewHandlerWithOptions around a typed
APIGatewayProxyRequest. For the literal 9007199254740993, default decoding yields
float64 9007199254740992; WithUseNumber(true) yields json.Number with the original
digits. This is evidence for data-type compatibility in
[decision 0014](decisions/0014-claims-api.md), not a proposal to use the v1 codec
inside edge. No live Lambda or API Gateway request was made.

## Implemented claims constructors (2026-09-16)

The same Go 1.27.1 / darwin-arm64 Apple M2 environment, three samples per case:

```sh
go test ./identity -run '^TestClaimsRetainedMemory$' -v -count=3
go test ./identity -run '^$' -bench '^BenchmarkClaims$' -benchmem -benchtime=100ms -count=3
```

Median bytes per operation and independently sampled retained heap in bytes:

| Fixture | Typed B/op | Raw B/op | Typed retained | Raw retained |
| --- | ---: | ---: | ---: | ---: |
| Ordinary claims | 2,690 | 4,968 | 2,696 | 2,776 |
| Distinct names, 256 KiB budget | 750,396 | 2,691,610 | 750,400 | 689,360 |
| Distinct names, 6 MiB budget | 12,238,708 | 53,483,244 | 12,238,688 | 12,937,712 |
| Singleton objects, 256 KiB budget | 1,661,221 | 1,922,539 | 1,661,184 | 1,669,376 |
| Large string, 256 KiB budget | 262,912 | 1,311,952 | 262,912 | 262,912 |

Ordinary typed construction took a median 1.24 µs and 30 allocations; ordinary
raw construction took 3.02 µs and 81 allocations. Raw construction includes strict
lexical validation and preflight, then owned-tree construction, while typed input
has no JSON round trip. Raw numbers retain their literal, while input float64
values retain only the available floating-point value. Both paths clone strings.

The largest opt-in raw case has substantial transient allocation (about 51 MiB
cumulative versus 12.3 MiB retained). Two parser passes, duplicate-name tracking,
string conversion/copying, and map growth contribute. These measurements do not
justify equating the 6 MiB weighted budget with memory use, or making it the
default. Future storage optimizations can retain the approved accounting policy.

The raw and typed retained totals can differ because map construction capacity,
array growth and exact numeric storage differ. Inputs remain alive during memory
samples; two collections before and after reduce pool retention. Small deltas
are noisy (ordinary typed samples ranged from 2,696 to 8,016 bytes). No figure is
peak memory, RSS, total invocation allocation, or a Lambda latency prediction.
JWT normalized views and dedicated scopes are not implemented in these samples.
