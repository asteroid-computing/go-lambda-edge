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
