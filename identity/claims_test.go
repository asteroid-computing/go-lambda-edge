package identity_test

import (
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func parseClaims(t *testing.T, input string) identity.Claims {
	t.Helper()
	c, err := identity.ParseClaims(jsontext.Value(input))
	if err != nil {
		t.Fatalf("ParseClaims(%q) = %v", input, err)
	}
	return c
}

func lookup(t *testing.T, c identity.Claims, name string) identity.Claim {
	t.Helper()
	v, ok := c.Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%q) missing from fixture", name)
	}
	return v
}

func TestClaimsPresenceAndReads(t *testing.T) {
	c := parseClaims(t, `{"null":null,"false":false,"true":true,"text":"","number":1,"array":[],"object":{}}`)
	for name, kind := range map[string]identity.ClaimKind{
		"null":   identity.ClaimNull,
		"false":  identity.ClaimBoolean,
		"true":   identity.ClaimBoolean,
		"text":   identity.ClaimString,
		"number": identity.ClaimNumber,
		"array":  identity.ClaimArray,
		"object": identity.ClaimObject,
	} {
		v := lookup(t, c, name)
		if v.Kind() != kind || v.Representation() != identity.RepresentationJSON {
			t.Errorf("Lookup(%q): kind=%v representation=%v, want %v / JSON", name, v.Kind(), v.Representation(), kind)
		}
		if _, ok := v.Text(); ok != (kind == identity.ClaimString) {
			t.Errorf("%s.Text() ok=%t, kind=%v", name, ok, kind)
		}
		if b, ok := v.Bool(); ok != (kind == identity.ClaimBoolean) || b != (name == "true") {
			t.Errorf("%s.Bool() = %t, %t", name, b, ok)
		}
		if a, ok := v.Array(); ok != (kind == identity.ClaimArray) || len(a) != 0 {
			t.Errorf("%s.Array() = %v, %t", name, a, ok)
		}
		if o, ok := v.Object(); ok != (kind == identity.ClaimObject) || o.Len() != 0 {
			t.Errorf("%s.Object() length=%d, ok=%t", name, o.Len(), ok)
		}
		if _, ok := v.NumberText(); ok != (kind == identity.ClaimNumber) {
			t.Errorf("%s.NumberText() ok=%t, kind=%v", name, ok, kind)
		}
		if _, ok := v.Float64(); ok != (kind == identity.ClaimNumber) {
			t.Errorf("%s.Float64() ok=%t, kind=%v", name, ok, kind)
		}
	}
	missing, ok := c.Lookup("missing")
	if ok || missing.Kind() != identity.ClaimInvalid || missing.Representation() != identity.RepresentationUnknown {
		t.Errorf("Lookup(missing) kind=%v representation=%v ok=%t", missing.Kind(), missing.Representation(), ok)
	}
	var empty identity.Claims
	if empty.Len() != 0 || len(empty.Names()) != 0 {
		t.Error("zero Claims is not empty")
	}
	if _, ok := empty.Lookup("anything"); ok {
		t.Error("zero Claims contains a member")
	}
}

func TestClaimsNumberFidelity(t *testing.T) {
	for _, literal := range []string{"9007199254740993", "18446744073709551615", "-0", "1.2300e+40", "1e9999"} {
		t.Run(literal, func(t *testing.T) {
			c := parseClaims(t, `{"n":`+literal+`}`)
			n := lookup(t, c, "n")
			if got, ok := n.NumberText(); !ok || got != literal {
				t.Errorf("NumberText(%s) = %q, %t", literal, got, ok)
			}
			approx, ok := n.Float64()
			if ok != (literal != "1e9999") {
				t.Errorf("Float64(%s) = %v, %t", literal, approx, ok)
			}
			if literal == "-0" && !math.Signbit(approx) {
				t.Error("Float64(-0) lost its sign")
			}
		})
	}
	for _, tc := range []struct {
		name  string
		input any
		text  string
	}{
		{name: "int", input: int(-12), text: "-12"},
		{name: "int8", input: int8(-128), text: "-128"},
		{name: "int16", input: int16(-32768), text: "-32768"},
		{name: "int32", input: int32(-2147483648), text: "-2147483648"},
		{name: "int64", input: int64(math.MinInt64), text: "-9223372036854775808"},
		{name: "uint", input: uint(12), text: "12"},
		{name: "uint8", input: uint8(255), text: "255"},
		{name: "uint16", input: uint16(65535), text: "65535"},
		{name: "uint32", input: uint32(math.MaxUint32), text: "4294967295"},
		{name: "uint64", input: uint64(math.MaxUint64), text: "18446744073709551615"},
		{name: "json_number", input: json.Number("9007199254740993"), text: "9007199254740993"},
		{name: "float32", input: float32(42)},
		{name: "float64", input: float64(9007199254740992)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := identity.NewClaims(map[string]any{"n": tc.input})
			if err != nil {
				t.Fatal(err)
			}
			n := lookup(t, c, "n")
			if text, ok := n.NumberText(); text != tc.text || ok != (tc.text != "") {
				t.Errorf("NumberText(%T) = %q, %t; want %q", tc.input, text, ok, tc.text)
			}
			if _, ok := n.Float64(); !ok || n.Representation() != identity.RepresentationDecoded {
				t.Errorf("numeric decoded input %T lost its value or representation", tc.input)
			}
		})
	}
}

func TestGatewayTextIsNotReparsed(t *testing.T) {
	input := map[string]string{"n": "12", "groups": `["admin"]`, "bool": "true", "null": "null"}
	c, err := identity.NewTextClaims(input)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range input {
		v := lookup(t, c, name)
		if got, ok := v.Text(); !ok || got != want || v.Representation() != identity.RepresentationGatewayText {
			t.Errorf("gateway %q = %q, %t, representation=%v", name, got, ok, v.Representation())
		}
	}
}

func TestClaimsOwnership(t *testing.T) {
	array := []any{"first", map[string]string{"inner": "before"}}
	nested := map[string]any{"list": array, "strings": []string{"before"}}
	input := map[string]any{"nested": nested, "text": "before"}
	c, err := identity.NewClaims(input)
	if err != nil {
		t.Fatal(err)
	}
	clear(input)
	array[0] = "after"
	array[1].(map[string]string)["inner"] = "after"
	nested["strings"].([]string)[0] = "after"
	clear(nested)
	names := c.Names()
	if !slices.Equal(names, []string{"nested", "text"}) {
		t.Errorf("Names() = %v, want sorted names", names)
	}
	names[0] = "changed"
	obj, ok := lookup(t, c, "nested").Object()
	if !ok {
		t.Fatal("owned nested object missing")
	}
	list, ok := lookup(t, obj, "list").Array()
	if !ok || len(list) != 2 {
		t.Fatalf("owned list length=%d, ok=%t", len(list), ok)
	}
	if text, ok := list[0].Text(); !ok || text != "first" {
		t.Errorf("owned list[0] = %q, %t", text, ok)
	}
	inner, ok := list[1].Object()
	if !ok {
		t.Fatal("owned inner object missing")
	}
	if text, ok := lookup(t, inner, "inner").Text(); !ok || text != "before" {
		t.Errorf("owned inner text = %q, %t", text, ok)
	}
	list[0] = identity.Claim{}
	list, _ = lookup(t, obj, "list").Array()
	if text, ok := list[0].Text(); !ok || text != "first" {
		t.Errorf("Array result mutation changed original to %q, %t", text, ok)
	}
	texts, _ := lookup(t, obj, "strings").Array()
	if text, ok := texts[0].Text(); !ok || text != "before" {
		t.Errorf("owned []string element = %q, %t", text, ok)
	}
	wire := jsontext.Value(`{"key":"secret","n":9007199254740993}`)
	parsed, err := identity.ParseClaims(wire)
	if err != nil {
		t.Fatal(err)
	}
	clear(wire)
	if text, ok := lookup(t, parsed, "key").Text(); !ok || text != "secret" {
		t.Errorf("input mutation changed parsed string to %q, %t", text, ok)
	}
	if text, ok := lookup(t, parsed, "n").NumberText(); !ok || text != "9007199254740993" {
		t.Errorf("input mutation changed parsed number to %q, %t", text, ok)
	}
}

func TestNilContainerKinds(t *testing.T) {
	c, err := identity.NewClaims(map[string]any{
		"map":         map[string]any(nil),
		"strings_map": map[string]string(nil),
		"array":       []any(nil),
		"strings":     []string(nil),
		"null":        nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]identity.ClaimKind{
		"map":         identity.ClaimObject,
		"strings_map": identity.ClaimObject,
		"array":       identity.ClaimArray,
		"strings":     identity.ClaimArray,
		"null":        identity.ClaimNull,
	} {
		if got := lookup(t, c, name).Kind(); got != want {
			t.Errorf("Kind(%s) = %v, want %v", name, got, want)
		}
	}
}

type hostileClaim struct{}

func (hostileClaim) String() string { panic("must not invoke arbitrary String methods") }
func (hostileClaim) MarshalJSON() ([]byte, error) {
	panic("must not invoke arbitrary JSON methods")
}

func TestInvalidTypedClaims(t *testing.T) {
	type customString string
	type customMap map[string]any
	for name, input := range map[string]any{
		"nan":              math.NaN(),
		"infinity":         math.Inf(1),
		"float32_infinity": float32(math.Inf(-1)),
		"custom":           customString("text"),
		"custom_map":       customMap{},
		"pointer":          new(1),
		"uintptr":          uintptr(1),
		"bytes":            []byte("text"),
		"struct":           hostileClaim{},
		"invalid_utf8":     "\xff",
		"invalid_key":      map[string]any{"\xff": nil},
		"invalid_number":   json.Number("secret"),
		"spaced_number":    json.Number(" 1"),
		"trailing_space":   json.Number("1 "),
		"leading_zero":     json.Number("01"),
		"multiple_numbers": json.Number("1 2"),
		"null_number":      json.Number("null"),
		"empty_number":     json.Number(""),
		"hex_number":       json.Number("0x1p2"),
	} {
		t.Run(name, func(t *testing.T) {
			c, err := identity.NewClaims(map[string]any{"secret": input})
			if !errors.Is(err, identity.ErrInvalidClaims) || c.Len() != 0 {
				t.Errorf("NewClaims(%s) length=%d error=%v; want invalid claims", name, c.Len(), err)
			}
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				if strings.Contains(cause.Error(), "secret") {
					t.Errorf("error tree exposed claims: %v", cause)
				}
			}
		})
	}
}

func TestInvalidJSONClaims(t *testing.T) {
	for _, input := range []string{
		``,
		`null`,
		`[]`,
		`42`,
		`{"secret":`,
		`{"secret":1,"secret":2}`,
		`{"x":1,"\u0078":2}`,
		`{} {}`,
		`{} secret`,
		"{\"secret\":\"\xff\"}",
		`{"x":[1,]}`,
		`{"x":{"a":1,"a":2}}`,
		`{"x":"\ud800"}`,
	} {
		c, err := identity.ParseClaims(jsontext.Value(input))
		if !errors.Is(err, identity.ErrInvalidClaims) || c.Len() != 0 {
			t.Errorf("ParseClaims(%q) length=%d error=%v; want invalid claims", input, c.Len(), err)
		}
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			if strings.Contains(cause.Error(), "secret") {
				t.Errorf("error tree exposed JSON: %v", cause)
			}
		}
	}
}

func TestClaimsBudgets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  map[string]any
		wire   string
		charge int
	}{
		{name: "empty", wire: `{}`, charge: 64},
		{name: "string", input: map[string]any{"x": "a"}, wire: `{"x":"a"}`, charge: 130},
		{name: "unicode", input: map[string]any{"é": "é"}, wire: `{"é":"é"}`, charge: 132},
		{name: "number", input: map[string]any{"x": json.Number("1e2")}, wire: `{"x":1e2}`, charge: 132},
		{name: "nested", input: map[string]any{"x": []any{nil}}, wire: `{"x":[null]}`, charge: 193},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, delta := range []int{-1, 0} {
				maximum := tc.charge + delta
				for name, construct := range map[string]func() (identity.Claims, error){
					"typed": func() (identity.Claims, error) {
						return identity.NewClaims(tc.input, identity.WithClaimsBudget(maximum))
					},
					"raw": func() (identity.Claims, error) {
						return identity.ParseClaims(jsontext.Value(tc.wire), identity.WithClaimsBudget(maximum))
					},
				} {
					_, err := construct()
					if delta == 0 {
						if err != nil {
							t.Errorf("%s exact budget %d rejected: %v", name, maximum, err)
						}
						continue
					}
					limit, ok := errors.AsType[*identity.ClaimsLimitError](err)
					if !ok || limit.Maximum() != int64(maximum) || !errors.Is(err, identity.ErrClaimsLimit) {
						t.Errorf("%s budget %d error=%v, want ClaimsLimitError with that maximum", name, maximum, err)
					}
				}
			}
		})
	}
	_, err := identity.ParseClaims(jsontext.Value(`{}`+strings.Repeat(" ", 63)), identity.WithClaimsBudget(64))
	if !errors.Is(err, identity.ErrClaimsLimit) {
		t.Errorf("wire length greater than 64 accepted: %v", err)
	}
	for _, opts := range [][]identity.ClaimsOption{{nil}, {identity.WithClaimsBudget(0)}, {identity.WithClaimsBudget(-1)}, {identity.WithClaimsBudget(6*1024*1024 + 1)}} {
		if _, err := identity.NewClaims(nil, opts...); err == nil {
			t.Error("invalid constructor configuration accepted")
		}
	}
	if _, err := identity.NewClaims(nil, identity.WithClaimsBudget(0), identity.WithClaimsBudget(64)); err != nil {
		t.Errorf("last budget assignment did not win: %v", err)
	}
	if _, err := identity.NewTextClaims(nil, identity.WithClaimsBudget(64)); err != nil {
		t.Errorf("nil text map not an empty object: %v", err)
	}
	_, err = identity.NewTextClaims(map[string]string{"x": "a"}, identity.WithClaimsBudget(129))
	if !errors.Is(err, identity.ErrClaimsLimit) {
		t.Errorf("text map did not enforce budget: %v", err)
	}
}

func TestClaimsBoundDepthAndExpansion(t *testing.T) {
	for _, depth := range []int{64, 65} {
		input := map[string]any{}
		for range depth - 1 {
			input = map[string]any{"x": input}
		}
		wire := strings.Repeat(`{"x":`, depth-1) + `{}` + strings.Repeat(`}`, depth-1)
		_, typedErr := identity.NewClaims(input)
		_, rawErr := identity.ParseClaims(jsontext.Value(wire))
		for name, err := range map[string]error{"typed": typedErr, "raw": rawErr} {
			if depth == 64 && err != nil || depth == 65 && !errors.Is(err, identity.ErrInvalidClaims) {
				t.Errorf("%s depth %d: %v", name, depth, err)
			}
			if errors.Is(err, identity.ErrClaimsLimit) {
				t.Errorf("%s depth error misclassified as bytes: %v", name, err)
			}
		}
	}
	cycle := make(map[string]any)
	cycle["self"] = cycle
	if _, err := identity.NewClaims(cycle); !errors.Is(err, identity.ErrInvalidClaims) {
		t.Errorf("cyclic map error=%v, want invalid claims", err)
	}
	var shared any = "leaf"
	for range 18 {
		shared = []any{shared, shared}
	}
	if _, err := identity.NewClaims(map[string]any{"shared": shared}); !errors.Is(err, identity.ErrClaimsLimit) {
		t.Errorf("expanded shared subtree error=%v, want budget exceeded", err)
	}
}

func TestClaimsConcurrentReadsAndDiagnostics(t *testing.T) {
	c := parseClaims(t, `{"secret_identifier":"secret_value","array":["secret_value"]}`)
	v := lookup(t, c, "secret_identifier")
	for _, value := range []any{c, v} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if got := fmt.Sprintf(format, value); strings.Contains(got, "secret") {
				t.Errorf("diagnostic %s exposed claims: %s", format, got)
			}
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				v, _ := c.Lookup("secret_identifier")
				if text, ok := v.Text(); !ok || text != "secret_value" {
					t.Errorf("concurrent Text() = %q, %t", text, ok)
				}
				names := c.Names()
				names[0] = "changed"
			}
		})
	}
	wg.Wait()
}

func FuzzParseClaims(f *testing.F) {
	for _, input := range []string{`{}`, `{"n":9007199254740993}`, `{"nested":[null,true,{"x":"y"}]}`, `{"x":1,"x":2}`, `null`} {
		f.Add([]byte(input))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		c, err := identity.ParseClaims(input, identity.WithClaimsBudget(4096))
		if err != nil {
			if !errors.Is(err, identity.ErrInvalidClaims) && !errors.Is(err, identity.ErrClaimsLimit) {
				t.Errorf("ParseClaims returned unclassified input error: %v", err)
			}
			if c.Len() != 0 {
				t.Error("ParseClaims returned a partial object on failure")
			}
			return
		}
		names := c.Names()
		clear(input)
		for _, name := range names {
			v, ok := c.Lookup(name)
			if !ok || v.Kind() == identity.ClaimInvalid || v.Representation() != identity.RepresentationJSON {
				t.Error("accepted JSON lost its shape or ownership")
			}
		}
	})
}
