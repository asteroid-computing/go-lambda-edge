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
