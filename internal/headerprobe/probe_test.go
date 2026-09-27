// Package headerprobe measures a candidate for decision 0010.
// Nothing in edge imports this package.
// This is an allocation experiment, not the response header implementation: filtering, semantic validation and projection are absent.
package headerprobe

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const budget = 6 * 1024 * 1024

// preflight uses incremental subtraction so neither long names nor large value slices can overflow a summed charge.
// Empty slices still cost a name plus 32.
func preflight(h http.Header, remaining int) bool {
	for name, values := range h {
		for i := range max(1, len(values)) {
			for _, n := range []int{len(name), 32} {
				if n > remaining {
					return false
				}
				remaining -= n
			}
			if len(values) != 0 {
				if len(values[i]) > remaining {
					return false
				}
				remaining -= len(values[i])
			}
		}
	}
	return true
}

func snapshot(h http.Header) http.Header {
	if !preflight(h, budget) {
		return nil
	}
	keys := make([]string, 0, len(h))
	for key := range h {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make(http.Header, len(h))
	for _, key := range keys {
		name := http.CanonicalHeaderKey(key)
		values := slices.Grow(out[name], len(h[key]))
		for _, value := range h[key] {
			values = append(values, strings.Trim(value, " \t"))
		}
		out[name] = values
	}
	return out
}

func fixture(name string) http.Header {
	ceiling := budget
	if strings.HasSuffix(name, "_256k") {
		name = strings.TrimSuffix(name, "_256k")
		ceiling = 256 * 1024
	}
	if strings.HasSuffix(name, "_1m") {
		name = strings.TrimSuffix(name, "_1m")
		ceiling = 1024 * 1024
	}
	switch name {
	case "ordinary":
		return http.Header{
			"Content-Type":  {"application/json"},
			"Cache-Control": {"private", "no-store"},
			"Set-Cookie":    {"a=1; Secure; HttpOnly; SameSite=Lax", "b=2; Secure; HttpOnly"},
			"Vary":          {"Origin", "Accept-Encoding"},
		}
	case "empty_values":
		return http.Header{"A": make([]string, budget/33)}
	case "distinct_names", "suppressed_names":
		h := make(http.Header)
		for i := range ceiling / 40 {
			key := fmt.Sprintf("H-%06d", i)
			if name == "suppressed_names" {
				h[key] = nil
			} else {
				h[key] = []string{""}
			}
		}
		return h
	case "case_collisions":
		count := budget / (2 * (len("Example") + 32))
		return http.Header{"Example": make([]string, count), "example": make([]string, count)}
	case "escaped_value":
		return http.Header{"A": {strings.Repeat(`"`, budget-33)}}
	case "over_budget":
		return http.Header{"A": make([]string, budget/33+1)}
	default:
		panic("unknown probe fixture")
	}
}

var cases = []string{"ordinary", "empty_values", "distinct_names", "distinct_names_1m", "distinct_names_256k", "suppressed_names", "case_collisions", "escaped_value", "over_budget"}

func BenchmarkSnapshot(b *testing.B) {
	for _, name := range cases {
		b.Run(name, func(b *testing.B) {
			h := fixture(name)
			b.ReportAllocs()
			for b.Loop() {
				out := snapshot(h)
				runtime.KeepAlive(out)
			}
		})
	}
}

// TestSnapshotMemory reports incremental live heap after GC, with the input deliberately kept alive in both samples.
// It excludes the application's input allocations and is not a peak-memory/RSS measurement.
// Repeat in a quiet process.
func TestSnapshotMemory(t *testing.T) {
	t.Logf("toolchain=%s target=%s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			h := fixture(name)
			// Two collections discard both active and victim sync.Pool entries from previous cases, including the JSON encoder's large buffers.
			runtime.GC()
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			out := snapshot(h)
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&after)
			runtime.KeepAlive(h)
			runtime.KeepAlive(out)
			retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
			// Encoding is intentionally outside the snapshot memory sample.
			// This is ordinary JSON v2, not the production bounded response encoder.
			encoded, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("input_names=%d accepted=%t retained_delta_bytes=%d snapshot_json_bytes=%d", len(h), out != nil, retained, len(encoded))
		})
	}
}

func TestPreflightBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name string
		h    http.Header
		max  int
		want bool
	}{
		{name: "nil_slice_at_limit", h: http.Header{"A": nil}, max: 33, want: true},
		{name: "empty_slice_at_limit", h: http.Header{"A": {}}, max: 33, want: true},
		{name: "empty_value_at_limit", h: http.Header{"A": {""}}, max: 33, want: true},
		{name: "name_over_limit", h: http.Header{"AA": nil}, max: 33},
		{name: "value_over_limit", h: http.Header{"A": {"x"}}, max: 33},
		{name: "repeats_at_limit", h: http.Header{"A": {"", ""}}, max: 66, want: true},
		{name: "repeats_over_limit", h: http.Header{"A": {"", ""}}, max: 65},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := preflight(tt.h, tt.max); got != tt.want {
				t.Errorf("preflight(%v, %d) = %t, want %t", tt.h, tt.max, got, tt.want)
			}
		})
	}
}
