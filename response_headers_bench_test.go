package edge

import (
	"fmt"
	"net/http"
	"runtime"
	"testing"
)

func responseHeaderFixture(name string) http.Header {
	if name == "ordinary" {
		return http.Header{
			"Content-Type":  {"application/json"},
			"Cache-Control": {"private", "no-store"},
			"Set-Cookie":    {"a=1; Secure; HttpOnly; SameSite=Lax", "b=2; Secure; HttpOnly"},
			"Vary":          {"Origin", "Accept-Encoding"},
		}
	}
	budget := defaultResponseHeaderBudget
	if name == "distinct_names_6m" {
		budget = maxResponseBytes
	}
	fields := make(http.Header)
	for i := range budget / 40 {
		fields[fmt.Sprintf("H-%06d", i)] = []string{""}
	}
	return fields
}

func BenchmarkResponseHeaders(b *testing.B) {
	for _, name := range []string{"ordinary", "distinct_names_default", "distinct_names_6m"} {
		for _, operation := range []string{"snapshot", "v1", "v2"} {
			b.Run(name+"/"+operation, func(b *testing.B) {
				fields := responseHeaderFixture(name)
				budget := defaultResponseHeaderBudget
				if name == "distinct_names_6m" {
					budget = maxResponseBytes
				}
				b.ReportAllocs()
				for b.Loop() {
					h, err := snapshotResponseHeaders(fields, budget)
					if err != nil {
						b.Fatal(err)
					}
					switch operation {
					case "v1":
						runtime.KeepAlive(h.v1())
					case "v2":
						out, cookies, err := h.v2()
						if err != nil {
							b.Fatal(err)
						}
						runtime.KeepAlive(out)
						runtime.KeepAlive(cookies)
					}
					runtime.KeepAlive(h)
				}
			})
		}
	}
}

func TestResponseHeaderRetainedMemory(t *testing.T) {
	for _, name := range []string{"ordinary", "distinct_names_default", "distinct_names_6m"} {
		t.Run(name, func(t *testing.T) {
			fields := responseHeaderFixture(name)
			budget := defaultResponseHeaderBudget
			if name == "distinct_names_6m" {
				budget = maxResponseBytes
			}
			runtime.GC()
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			h := mustSnapshot(t, fields, budget)
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&after)
			runtime.KeepAlive(fields)
			runtime.KeepAlive(h)
			t.Logf("additional retained heap = %d bytes (excludes input and projection; not peak memory)", int64(after.HeapAlloc)-int64(before.HeapAlloc))
		})
	}
}
