package edge

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func mustSnapshot(t testing.TB, fields http.Header, budget int) *responseHeaders {
	t.Helper()
	h, err := snapshotResponseHeaders(fields, budget)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestResponseHeaderSnapshotOwnership(t *testing.T) {
	fields := http.Header{
		"X-Mixed": {" \tfirst\t ", "second"}, "x-mixed": {"third"},
		"X-Empty": {""}, "X-Suppressed": nil,
		"Vary": nil, "vary": {"Origin"},
		"X-Unicode": {"café"}, "X-Interior": {"a\t b"},
	}
	h := mustSnapshot(t, fields, defaultResponseHeaderBudget)
	fields["X-Mixed"][0] = "changed"
	clear(fields)
	if !slices.Equal(h.fields.Values("X-Mixed"), []string{"first", "second", "third"}) {
		t.Errorf("snapshot values = %v; want owned, trimmed values in lexical alias order", h.fields["X-Mixed"])
	}
	if values, present := h.fields["X-Suppressed"]; !present || len(values) != 0 {
		t.Errorf("suppression marker = %v, %t; want present and empty", values, present)
	}
	if h.fields.Get("Vary") != "Origin" || h.fields.Get("X-Unicode") != "café" || h.fields.Get("X-Interior") != "a\t b" {
		t.Errorf("snapshot changed explicit content: %v", h.fields)
	}
	v1 := h.v1()
	if _, present := v1["X-Suppressed"]; present {
		t.Error("V1 emitted a suppression marker")
	}
	if !slices.Equal(v1["X-Empty"], []string{""}) {
		t.Errorf("V1 empty field = %v; want one empty value", v1["X-Empty"])
	}
	v1["X-Mixed"][0] = "changed again"
	delete(v1, "X-Empty")
	if h.fields.Get("X-Mixed") != "first" || len(h.fields["X-Empty"]) != 1 {
		t.Error("V1 projection mutation changed snapshot")
	}
}

func TestResponseHeaderValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		fields http.Header
		want   error
	}{
		{name: "empty_name", fields: http.Header{"": {"v"}}, want: errResponseHeaders},
		{name: "space_name", fields: http.Header{"Bad Name": {"v"}}, want: errResponseHeaders},
		{name: "unicode_name", fields: http.Header{"Ünicode": nil}, want: errResponseHeaders},
		{name: "bad_utf8", fields: http.Header{"A": {"\xff"}}, want: errResponseHeaders},
		{name: "carriage_return", fields: http.Header{"A": {"v\r"}}, want: errResponseHeaders},
		{name: "newline", fields: http.Header{"A": {"v\n"}}, want: errResponseHeaders},
		{name: "nul", fields: http.Header{"A": {"\x00"}}, want: errResponseHeaders},
		{name: "del", fields: http.Header{"A": {"\x7f"}}, want: errResponseHeaders},
		{name: "control", fields: http.Header{"A": {"\x1f"}}, want: errResponseHeaders},
		{name: "trailer_prefix", fields: http.Header{http.TrailerPrefix + "Digest": nil}, want: http.ErrNotSupported},
		{name: "trailer", fields: http.Header{"trailer": {"Digest"}}, want: http.ErrNotSupported},
		{name: "upgrade", fields: http.Header{"upgrade": {"websocket"}}, want: http.ErrNotSupported},
		{name: "hidden_trailer", fields: http.Header{"Connection": {"Trailer"}, "Trailer": {"Digest"}}, want: http.ErrNotSupported},
		{name: "hidden_upgrade", fields: http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}, want: http.ErrNotSupported},
		{name: "upgrade_nomination", fields: http.Header{"Connection": {"upgrade"}}, want: http.ErrNotSupported},
		{name: "invalid_connection_token", fields: http.Header{"Connection": {"good, bad token"}}, want: errResponseHeaders},
		{name: "quoted_connection_token", fields: http.Header{"Connection": {`"Custom"`}}, want: errResponseHeaders},
		{name: "invalid_removed_field", fields: http.Header{"Connection": {"Custom"}, "Custom": {"\n"}}, want: errResponseHeaders},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, err := snapshotResponseHeaders(tt.fields, defaultResponseHeaderBudget)
			if h != nil || !errors.Is(err, tt.want) {
				t.Errorf("snapshot(%v) = %v, %v; want nil, %v", tt.fields, h, err, tt.want)
			}
		})
	}
}

func TestResponseHeaderConnectionFiltering(t *testing.T) {
	fields := http.Header{
		"Connection": {" custom , content-type, ,CONTENT-LENGTH, "},
		"connection": {"Other,Connection"},
		"Custom":     {"secret"}, "Other": {"hidden"},
		"Content-Type": {"application/json"}, "Content-Length": {"bad", "length"},
		"Proxy-Connection": {"close"}, "Keep-Alive": {"timeout=5"},
		"Proxy-Authenticate": {"Basic"}, "Proxy-Authorization": {"credentials"},
		"Te": {"trailers"}, "Transfer-Encoding": {"chunked"},
		"Trailer": nil, "Upgrade": {}, "Remain": {"visible"},
	}
	h := mustSnapshot(t, fields, defaultResponseHeaderBudget)
	if !maps.EqualFunc(h.fields, http.Header{"Remain": {"visible"}}, slices.Equal[[]string]) {
		t.Errorf("filtered headers = %v; want only Remain", h.fields)
	}
	for name, value := range map[string]string{"Content-Type": "text/plain", "Content-Length": "7"} {
		if err := h.automatic(name, value); err != nil {
			t.Fatal(err)
		}
		if _, present := h.fields[name]; present {
			t.Errorf("automatic field %s resurrected Connection nomination", name)
		}
	}
	if fields.Get("Custom") != "secret" || len(fields["Content-Length"]) != 2 {
		t.Error("filtering mutated original headers")
	}
}

func TestResponseHeaderBudgetAndAutomaticFields(t *testing.T) {
	for _, fields := range []http.Header{{"A": nil}, {"A": {}}, {"A": {""}}} {
		if h, err := snapshotResponseHeaders(fields, 32); h != nil || !errors.Is(err, errResponseHeaderBudget) {
			t.Errorf("snapshot(%v, 32) = %v, %v; want budget failure", fields, h, err)
		}
		h := mustSnapshot(t, fields, 33)
		if h.remaining != 0 {
			t.Errorf("snapshot(%v, 33) has %d uncharged bytes", fields, h.remaining)
		}
	}
	for _, fields := range []http.Header{{"A": {"", ""}}, {"A": {"  x "}}, {"Connection": {"close"}}, {"Set-Cookie": {"a=1"}}} {
		charge := 0
		for name, values := range fields {
			for _, value := range values {
				charge += len(name) + len(value) + 32
			}
		}
		if _, err := snapshotResponseHeaders(fields, charge-1); !errors.Is(err, errResponseHeaderBudget) {
			t.Errorf("snapshot(%v, %d) = %v; want rejection before trimming/removal", fields, charge-1, err)
		}
		h := mustSnapshot(t, fields, charge)
		if h.remaining != 0 {
			t.Errorf("snapshot refunded %d original bytes", h.remaining)
		}
	}
	const typeCost = len("Content-Type") + len("text/plain") + 32
	h := mustSnapshot(t, nil, typeCost)
	if err := h.automatic("Content-Type", "text/plain"); err != nil || h.remaining != 0 {
		t.Fatalf("automatic type = %v, remaining %d; want exact fit", err, h.remaining)
	}
	if err := h.automatic("Content-Length", "0"); !errors.Is(err, errResponseHeaderBudget) {
		t.Errorf("automatic length = %v; want budget failure", err)
	}
	if _, present := h.fields["Content-Length"]; present || h.remaining != 0 {
		t.Error("failed automatic addition mutated snapshot")
	}
	for _, values := range [][]string{nil, {}, {""}, {"application/json"}} {
		h := mustSnapshot(t, http.Header{"Content-Type": values}, defaultResponseHeaderBudget)
		before := h.remaining
		if err := h.automatic("Content-Type", "text/plain"); err != nil || h.remaining != before || !slices.Equal(h.fields["Content-Type"], values) {
			t.Errorf("automatic type replaced or charged existing value %v", values)
		}
	}
	tooMany := http.Header{"A": make([]string, defaultResponseHeaderBudget/33+1)}
	// Charging must not copy rejected input. The caller now separately allocates
	// a bounded InvocationError describing the configured limit.
	if allocations := testing.AllocsPerRun(10, func() {
		_, _ = chargeResponseHeaders(tooMany, defaultResponseHeaderBudget)
	}); allocations != 0 {
		t.Errorf("over-budget preflight allocated %g objects; want none", allocations)
	}
}

func TestResponseContentLength(t *testing.T) {
	for _, values := range [][]string{{""}, {"+1"}, {"-1"}, {"1,1"}, {"1", "1"}, {"1 2"}, {"9223372036854775808"}} {
		if h, err := snapshotResponseHeaders(http.Header{"Content-Length": values}, defaultResponseHeaderBudget); h != nil || !errors.Is(err, errResponseHeaders) {
			t.Errorf("Content-Length %v = %v, %v; want invalid response headers", values, h, err)
		}
	}
	for _, tt := range []struct {
		values []string
		want   int64
		set    bool
	}{
		{values: nil}, {values: []string{}},
		{values: []string{"0"}, set: true},
		{values: []string{" \t00012 "}, want: 12, set: true},
		{values: []string{"9223372036854775807"}, want: 9223372036854775807, set: true},
	} {
		h := mustSnapshot(t, http.Header{"Content-Length": tt.values}, defaultResponseHeaderBudget)
		n, set, err := responseContentLength(h.fields)
		if err != nil || n != tt.want || set != tt.set {
			t.Errorf("Content-Length %v = %d, %t, %v; want %d, %t", tt.values, n, set, err, tt.want, tt.set)
		}
	}
}

func TestResponseHeaderV2Projection(t *testing.T) {
	t.Run("suppressed_only", func(t *testing.T) {
		h := mustSnapshot(t, http.Header{"Content-Type": nil, "Content-Length": {}}, defaultResponseHeaderBudget)
		v2, cookies, err := h.v2()
		if len(h.v1()) != 0 || len(v2) != 0 || len(cookies) != 0 || err != nil {
			t.Errorf("suppressed fields produced V1/V2 output: %v, %v, %v", v2, cookies, err)
		}
	})
	// Each audited name has a fixture containing complete values. This tests
	// conversion and ordering, not a second implementation of each field grammar.
	for _, name := range []string{
		"Accept-Ranges", "Allow", "Content-Encoding", "Content-Language", "Vary", "WWW-Authenticate", "Cache-Control",
		"Access-Control-Allow-Headers", "Access-Control-Allow-Methods", "Access-Control-Expose-Headers",
		"Link", "Content-Security-Policy", "Content-Security-Policy-Report-Only", "Referrer-Policy", "Server-Timing", "Accept-Patch", "Accept-CH",
		"Cache-Status", "Proxy-Status", "Content-Digest", "Repr-Digest", "Want-Content-Digest", "Want-Repr-Digest", "Signature", "Signature-Input",
	} {
		t.Run(name, func(t *testing.T) {
			h := mustSnapshot(t, http.Header{name: {"first", "second"}}, defaultResponseHeaderBudget)
			fields, _, err := h.v2()
			if err != nil || fields[http.CanonicalHeaderKey(name)] != "first, second" {
				t.Errorf("V2 %s = %v, %v; want ordered combination", name, fields, err)
			}
		})
	}
	for _, name := range []string{"Content-Type", "Location", "ETag", "Date", "Access-Control-Allow-Origin", "Custom"} {
		t.Run("reject_"+name, func(t *testing.T) {
			h := mustSnapshot(t, http.Header{name: {"same", "same"}}, defaultResponseHeaderBudget)
			if fields, cookies, err := h.v2(); fields != nil || cookies != nil || !errors.Is(err, errResponseHeaderRepeat) {
				t.Errorf("V2 repeated %s = %v, %v, %v; want no partial response", name, fields, cookies, err)
			}
			if values := h.v1()[http.CanonicalHeaderKey(name)]; !slices.Equal(values, []string{"same", "same"}) {
				t.Errorf("V1 lost repeated %s: %v", name, values)
			}
		})
	}
	h := mustSnapshot(t, http.Header{
		"Set-Cookie":       {"a=1; Expires=Wed, 21 Oct 2030 07:28:00 GMT", "b=2"},
		"WWW-Authenticate": {`Digest realm="a,b", nonce="x"`, `Basic realm="other"`},
		"Signature-Input":  {`sig1=("@method" "@path");created=1`, `sig2=("content-digest");created=2`},
		"Custom":           {"literal, comma"}, "Empty": {""}, "Suppressed": nil,
	}, defaultResponseHeaderBudget)
	fields, cookies, err := h.v2()
	if err != nil {
		t.Fatal(err)
	}
	if fields["Www-Authenticate"] != `Digest realm="a,b", nonce="x", Basic realm="other"` || fields["Signature-Input"] != `sig1=("@method" "@path");created=1, sig2=("content-digest");created=2` {
		t.Errorf("V2 altered challenge/dictionary values: %v", fields)
	}
	if _, present := fields["Set-Cookie"]; present || !slices.Equal(cookies, h.fields["Set-Cookie"]) {
		t.Errorf("V2 cookies = %v, headers %v; want separate values", cookies, fields)
	}
	if value, present := fields["Empty"]; !present || value != "" || fields["Custom"] != "literal, comma" {
		t.Errorf("V2 changed single or empty values: %v", fields)
	}
	if _, present := fields["Suppressed"]; present {
		t.Error("V2 emitted suppression marker")
	}
	cookies[0] = "changed"
	if strings.HasPrefix(h.fields["Set-Cookie"][0], "changed") {
		t.Error("V2 cookies alias snapshot slice")
	}
}

func TestResponseHeaderBudgetOption(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts []Option
		want int
	}{
		{name: "default", want: defaultResponseHeaderBudget},
		{name: "minimum", opts: []Option{WithResponseHeaderBudget(1)}, want: 1},
		{name: "maximum", opts: []Option{WithResponseHeaderBudget(maxResponseBytes)}, want: maxResponseBytes},
		{name: "last_assignment", opts: []Option{WithResponseHeaderBudget(0), WithResponseHeaderBudget(100)}, want: 100},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, err := New(http.NewServeMux(), tt.opts...)
			if err != nil || a == nil {
				t.Fatalf("New = %v, %v; want valid adapter", a, err)
			}
			if a.config.responseHeaderBudget != tt.want {
				t.Errorf("configured budget = %d; want %d", a.config.responseHeaderBudget, tt.want)
			}
		})
	}
	for _, budget := range []int{-1, 0, maxResponseBytes + 1, int(^uint(0) >> 1)} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			a, err := New(http.NewServeMux(), WithResponseHeaderBudget(100), WithResponseHeaderBudget(budget))
			if a != nil || err == nil {
				t.Errorf("New with budget %d = %v, %v; want nil and configuration error", budget, a, err)
			}
		})
	}
}
