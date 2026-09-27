package identity_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func claimsFixture(name string) (map[string]any, int) {
	budget := 256 * 1024
	switch name {
	case "ordinary":
		return map[string]any{
			"iss": "https://issuer.example", "sub": "subject", "client_id": "client",
			"scope": "orders.read orders.write", "token_use": "access",
			"aud": []any{"orders", "inventory"}, "cognito:groups": []any{"staff", "billing"},
			"exp": float64(1900000000), "tenant": map[string]any{"id": "tenant", "active": true},
		}, budget
	case "distinct_names", "distinct_names_6m":
		if name == "distinct_names_6m" {
			budget = 6 * 1024 * 1024
		}
		out := make(map[string]any)
		for i := range (budget - 64) / 72 {
			out[fmt.Sprintf("c%07d", i)] = ""
		}
		return out, budget
	case "singleton_objects":
		values := make([]any, (budget-128-5)/129)
		for i := range values {
			values[i] = map[string]any{"x": nil}
		}
		return map[string]any{"items": values}, budget
	case "large_string":
		return map[string]any{"text": strings.Repeat("a", budget-128-4)}, budget
	default:
		panic("unknown benchmark fixture")
	}
}

func BenchmarkClaims(b *testing.B) {
	for _, name := range []string{"ordinary", "distinct_names", "distinct_names_6m", "singleton_objects", "large_string"} {
		input, budget := claimsFixture(name)
		wire, err := json.Marshal(input)
		if err != nil {
			b.Fatal(err)
		}
		for _, raw := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/raw=%t", name, raw), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					var c identity.Claims
					var err error
					if raw {
						c, err = identity.ParseClaims(wire, identity.WithClaimsBudget(budget))
					} else {
						c, err = identity.NewClaims(input, identity.WithClaimsBudget(budget))
					}
					if err != nil {
						b.Fatal(err)
					}
					runtime.KeepAlive(c)
				}
			})
		}
	}
}

func TestClaimsRetainedMemory(t *testing.T) {
	for _, name := range []string{"ordinary", "distinct_names", "distinct_names_6m", "singleton_objects", "large_string"} {
		input, budget := claimsFixture(name)
		wire, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/raw=%t", name, raw), func(t *testing.T) {
				runtime.GC()
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				var c identity.Claims
				var err error
				if raw {
					c, err = identity.ParseClaims(jsontext.Value(wire), identity.WithClaimsBudget(budget))
				} else {
					c, err = identity.NewClaims(input, identity.WithClaimsBudget(budget))
				}
				if err != nil {
					t.Fatal(err)
				}
				runtime.GC()
				runtime.GC()
				runtime.ReadMemStats(&after)
				runtime.KeepAlive(input)
				runtime.KeepAlive(wire)
				runtime.KeepAlive(c)
				t.Logf("budget=%d retained_heap_delta=%d (excludes input; not peak memory)", budget, int64(after.HeapAlloc)-int64(before.HeapAlloc))
			})
		}
	}
}
