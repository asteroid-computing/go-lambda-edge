// Package claimprobe measures a candidate owned claim tree. Nothing in edge
// imports it. This is a storage experiment, not an identity implementation:
// provenance, full input-type support, JWT projection and public APIs are absent.
package claimprobe

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

const (
	defaultBudget = 256 * 1024
	maxDepth      = 64
	nodeCharge    = 64
)

var errRejected = errors.New("probe input rejected")

type node struct {
	kind   byte
	origin byte
	text   string
	number float64
	array  []node
	object map[string]node
}

func charge(remaining *int, n int) bool {
	if n > *remaining {
		return false
	}
	*remaining -= n
	return true
}

// preflight counts every occurrence of a shared subtree. The depth bound also
// terminates cycles without allocating a visited map. It calls no user methods.
func preflight(v any, remaining *int, depth int) bool {
	if !charge(remaining, nodeCharge) {
		return false
	}
	switch v := v.(type) {
	case nil, bool:
		return true
	case string:
		return charge(remaining, len(v)) && utf8.ValidString(v)
	case float64:
		return !math.IsNaN(v) && !math.IsInf(v, 0)
	case map[string]any:
		if depth >= maxDepth || len(v) > *remaining/nodeCharge {
			return false
		}
		for name, value := range v {
			if !charge(remaining, len(name)) || !utf8.ValidString(name) || !preflight(value, remaining, depth+1) {
				return false
			}
		}
		return true
	case []any:
		if depth >= maxDepth || len(v) > *remaining/nodeCharge {
			return false
		}
		for _, value := range v {
			if !preflight(value, remaining, depth+1) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// copyNode is only called after preflight. Inputs must remain unchanged until
// copying completes, as with ordinary Go maps and slices passed to a function.
func copyNode(v any) node {
	switch v := v.(type) {
	case nil:
		return node{kind: 'n'}
	case bool:
		if v {
			return node{kind: 't'}
		}
		return node{kind: 'f'}
	case string:
		return node{kind: '"', text: strings.Clone(v)}
	case float64:
		return node{kind: '0', number: v}
	case map[string]any:
		out := node{kind: '{', object: make(map[string]node, len(v))}
		for name, value := range v {
			out.object[strings.Clone(name)] = copyNode(value)
		}
		return out
	case []any:
		out := node{kind: '[', array: make([]node, len(v))}
		for i, value := range v {
			out.array[i] = copyNode(value)
		}
		return out
	default:
		panic("probe copy without successful preflight")
	}
}

func snapshot(v map[string]any, budget int) (node, bool) {
	remaining := budget
	if !preflight(v, &remaining, 0) {
		return node{}, false
	}
	return copyNode(v), true
}

func fixture(name string) (map[string]any, int) {
	budget := defaultBudget
	switch name {
	case "ordinary":
		return map[string]any{
			"iss": "https://issuer.example", "sub": "subject", "client_id": "client",
			"scope": "orders.read orders.write", "token_use": "access",
			"aud": []any{"orders", "inventory"}, "cognito:groups": []any{"staff", "billing"},
			"exp": float64(1900000000), "tenant": map[string]any{"id": "tenant", "active": true},
		}, budget
	case "distinct_names", "distinct_names_1m", "distinct_names_6m":
		if name == "distinct_names_1m" {
			budget = 1024 * 1024
		} else if name == "distinct_names_6m" {
			budget = 6 * 1024 * 1024
		}
		out := make(map[string]any)
		for i := range (budget - nodeCharge) / (nodeCharge + 8) {
			out[fmt.Sprintf("c%07d", i)] = ""
		}
		return out, budget
	case "array", "over_budget":
		n := (budget - 2*nodeCharge - len("items")) / nodeCharge
		if name == "over_budget" {
			n++
		}
		return map[string]any{"items": make([]any, n)}, budget
	case "singleton_objects":
		n := (budget - 2*nodeCharge - len("items")) / (2*nodeCharge + 1)
		values := make([]any, n)
		for i := range values {
			values[i] = map[string]any{"x": nil}
		}
		return map[string]any{"items": values}, budget
	case "large_string":
		return map[string]any{"text": strings.Repeat("a", budget-2*nodeCharge-len("text"))}, budget
	case "depth_64", "depth_65":
		depth := 64
		if name == "depth_65" {
			depth++
		}
		out := map[string]any{"leaf": "v"}
		for range depth - 1 {
			out = map[string]any{"next": out}
		}
		return out, budget
	case "cycle":
		out := make(map[string]any)
		out["self"] = out
		return out, budget
	case "shared_subtree":
		var value any = "leaf"
		for range 18 {
			value = []any{value, value}
		}
		return map[string]any{"tree": value}, budget
	default:
		panic("unknown probe fixture")
	}
}

var cases = []string{"ordinary", "distinct_names", "distinct_names_1m", "distinct_names_6m", "array", "singleton_objects", "large_string", "depth_64", "depth_65", "cycle", "shared_subtree", "over_budget"}

func BenchmarkSnapshot(b *testing.B) {
	for _, name := range cases {
		b.Run(name, func(b *testing.B) {
			input, budget := fixture(name)
			b.ReportAllocs()
			for b.Loop() {
				out, ok := snapshot(input, budget)
				runtime.KeepAlive(out)
				runtime.KeepAlive(ok)
			}
		})
	}
}

func TestSnapshotMemory(t *testing.T) {
	t.Logf("toolchain=%s target=%s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			input, budget := fixture(name)
			runtime.GC()
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			out, ok := snapshot(input, budget)
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&after)
			runtime.KeepAlive(input)
			runtime.KeepAlive(out)
			remaining := budget
			accepted := preflight(input, &remaining, 0)
			if accepted != ok {
				t.Fatal("snapshot and preflight acceptance disagree")
			}
			t.Logf("budget=%d accepted=%t charge=%d retained_delta_bytes=%d", budget, ok, budget-remaining, int64(after.HeapAlloc)-int64(before.HeapAlloc))
		})
	}
}

func TestCandidateBoundaries(t *testing.T) {
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			input, budget := fixture(name)
			_, ok := snapshot(input, budget)
			want := name != "depth_65" && name != "cycle" && name != "shared_subtree" && name != "over_budget"
			if ok != want {
				t.Errorf("snapshot(%s) accepted=%t, want %t", name, ok, want)
			}
		})
	}
	input := map[string]any{"nested": map[string]any{"value": "before"}, "list": []any{"before"}}
	out, ok := snapshot(input, defaultBudget)
	if !ok {
		t.Fatal("valid ownership fixture rejected")
	}
	input["nested"].(map[string]any)["value"] = "after"
	input["list"].([]any)[0] = "after"
	if out.object["nested"].object["value"].text != "before" || out.object["list"].array[0].text != "before" {
		t.Error("snapshot aliases caller-owned containers")
	}
}

func TestSDKNumberFidelity(t *testing.T) {
	const payload = `{"httpMethod":"GET","path":"/","requestContext":{"apiId":"example","authorizer":{"claims":{"counter":9007199254740993}}}}`
	for _, useNumber := range []bool{false, true} {
		var captured any
		h := lambda.NewHandlerWithOptions(func(ctx context.Context, event events.APIGatewayProxyRequest) (bool, error) {
			captured = event.RequestContext.Authorizer["claims"].(map[string]any)["counter"]
			return true, nil
		}, lambda.WithUseNumber(useNumber))
		if _, err := h.Invoke(t.Context(), []byte(payload)); err != nil {
			t.Fatal(err)
		}
		t.Logf("UseNumber=%t type=%T value=%v", useNumber, captured, captured)
		if !useNumber && captured != float64(9007199254740992) {
			t.Errorf("default SDK number = %v, want rounded float64", captured)
		}
		if useNumber && fmt.Sprint(captured) != "9007199254740993" {
			t.Errorf("UseNumber SDK number = %v, want exact literal", captured)
		}
	}
}

// scanJSON measures bounded lexical validation separately from the tree. It
// uses jsontext directly; it does not decode through map[string]any and float64.
func scanJSON(input []byte, budget int) error {
	if len(input) > budget {
		return errRejected
	}
	d := jsontext.NewDecoder(bytes.NewReader(input))
	started, completed := false, false
	for {
		token, err := d.ReadToken()
		if err == io.EOF && completed {
			return nil
		}
		if err != nil {
			return err
		}
		if completed || !started && token.Kind() != '{' {
			return errRejected
		}
		started = true
		if d.StackDepth() > maxDepth {
			return errRejected
		}
		completed = d.StackDepth() == 0
	}
}

func TestJSONScanBoundaries(t *testing.T) {
	for _, input := range []string{``, `null`, `[]`, `{"a":`, `{"a":1,"a":2}`, `{} {}`, "{\"a\":\"\xff\"}"} {
		if err := scanJSON([]byte(input), defaultBudget); err == nil {
			t.Errorf("scanJSON(%q) accepted invalid claim object", input)
		}
	}
	if err := scanJSON([]byte(`{"a":9007199254740993,"b":[null,true]}`), defaultBudget); err != nil {
		t.Errorf("scanJSON(valid object) = %v", err)
	}
}

func BenchmarkJSONScan(b *testing.B) {
	for _, name := range []string{"ordinary", "distinct_names", "large_string", "depth_64"} {
		b.Run(name, func(b *testing.B) {
			input, budget := fixture(name)
			wire, err := json.Marshal(input)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := scanJSON(wire, budget); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
