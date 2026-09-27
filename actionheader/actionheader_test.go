package actionheader_test

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/actionheader"
)

func TestSelectorParse(t *testing.T) {
	selector, err := actionheader.NewSelector("aCtIoN")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		headers http.Header
		want    string
		wantErr error
	}{
		{name: "nil", wantErr: actionheader.ErrMissing},
		{name: "unrelated", headers: http.Header{"Else": {"orders.create"}}, wantErr: actionheader.ErrMissing},
		{name: "nil_slice", headers: http.Header{"Action": nil}, wantErr: actionheader.ErrMissing},
		{name: "empty_slice", headers: http.Header{"Action": {}}, wantErr: actionheader.ErrMissing},
		{name: "empty", headers: http.Header{"Action": {""}}, wantErr: actionheader.ErrInvalid},
		{name: "whitespace", headers: http.Header{"Action": {" \t "}}, wantErr: actionheader.ErrInvalid},
		{name: "trim_and_preserve_case", headers: http.Header{"Action": {" \tOrders.Create\t "}}, want: "Orders.Create"},
		{name: "direct_assignment", headers: http.Header{"aCtIoN": {"orders.create"}}, want: "orders.create"},
		{name: "empty_alias", headers: http.Header{"ACTION": {}, "action": {"orders.create"}}, want: "orders.create"},
		{name: "conflict", headers: http.Header{"Action": {"read", "delete"}}, wantErr: actionheader.ErrAmbiguous},
		{name: "identical", headers: http.Header{"Action": {"read", "read"}}, wantErr: actionheader.ErrAmbiguous},
		{name: "case_aliases", headers: http.Header{"Action": {"read"}, "ACTION": {"delete"}}, wantErr: actionheader.ErrAmbiguous},
		{name: "identical_aliases", headers: http.Header{"Action": {"read"}, "action": {"read"}}, wantErr: actionheader.ErrAmbiguous},
		{name: "invalid_plus_alias", headers: http.Header{"Action": {""}, "action": {"read"}}, wantErr: actionheader.ErrAmbiguous},
		{name: "multiple_empty", headers: http.Header{"Action": {"", ""}}, wantErr: actionheader.ErrAmbiguous},
		{name: "joined", headers: http.Header{"Action": {"read,delete"}}, wantErr: actionheader.ErrAmbiguous},
		{name: "identical_joined", headers: http.Header{"Action": {"read,read"}}, wantErr: actionheader.ErrAmbiguous},
		{name: "comma_before_invalid", headers: http.Header{"Action": {",\r\n"}}, wantErr: actionheader.ErrAmbiguous},
		{name: "space_in_token", headers: http.Header{"Action": {"orders create"}}, wantErr: actionheader.ErrInvalid},
		{name: "slash", headers: http.Header{"Action": {"orders/create"}}, wantErr: actionheader.ErrInvalid},
		{name: "colon", headers: http.Header{"Action": {"orders:create"}}, wantErr: actionheader.ErrInvalid},
		{name: "quoted", headers: http.Header{"Action": {`"read"`}}, wantErr: actionheader.ErrInvalid},
		{name: "unicode_space", headers: http.Header{"Action": {"\u00a0read"}}, wantErr: actionheader.ErrInvalid},
		{name: "unicode", headers: http.Header{"Action": {"créer"}}, wantErr: actionheader.ErrInvalid},
		{name: "invalid_utf8", headers: http.Header{"Action": {"\xff"}}, wantErr: actionheader.ErrInvalid},
		{name: "newline", headers: http.Header{"Action": {"read\n"}}, wantErr: actionheader.ErrInvalid},
		{name: "nul", headers: http.Header{"Action": {"read\x00"}}, wantErr: actionheader.ErrInvalid},
		{name: "del", headers: http.Header{"Action": {"read\x7f"}}, wantErr: actionheader.ErrInvalid},
		{name: "all_token_punctuation", headers: http.Header{"Action": {"!#$%&'*+-.^_`|~AZaz09"}}, want: "!#$%&'*+-.^_`|~AZaz09"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before := tt.headers.Clone()
			got, err := selector.Parse(tt.headers)
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Errorf("Parse(%v) = %q, %v; want %q, %v", tt.headers, got, err, tt.want, tt.wantErr)
			}
			if !reflect.DeepEqual(tt.headers, before) {
				t.Errorf("Parse mutated headers: got %v, want %v", tt.headers, before)
			}
			if err != nil && !errors.Is(fmt.Errorf("select action: %w", err), tt.wantErr) {
				t.Error("wrapped selection error lost its category")
			}
		})
	}
}

func TestSelectorConfiguration(t *testing.T) {
	for _, name := range []string{"", " Action", "Action ", "Action:Name", "Action\r\n", "Àction", "Action\xff"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			h, err := actionheader.NewSelector(name)
			if err == nil || h != (actionheader.Selector{}) {
				t.Fatalf("NewSelector(%q) = %v, %v; want zero selector and error", name, h, err)
			}
			_, parseErr := h.Parse(http.Header{"Action": {"read"}})
			if parseErr == nil {
				t.Fatal("zero selector Parse succeeded")
			}
			for _, category := range []error{actionheader.ErrMissing, actionheader.ErrAmbiguous, actionheader.ErrInvalid} {
				if errors.Is(err, category) || errors.Is(parseErr, category) {
					t.Errorf("configuration error matched input category %v", category)
				}
			}
		})
	}
	selector, err := actionheader.NewSelector("Task")
	if err != nil {
		t.Fatal(err)
	}
	copy := selector
	got, err := copy.Parse(http.Header{"taſk": {"wrong"}, "task": {"right"}, "Action": {"ignored"}})
	if got != "right" || err != nil {
		t.Errorf("copied custom selector Parse = %q, %v; want right", got, err)
	}
}

func TestSelectorErrorsDoNotDiscloseValues(t *testing.T) {
	selector, err := actionheader.NewSelector("Action")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"secret/action", "secret,action"} {
		action, err := selector.Parse(http.Header{"Action": {value}, "Authorization": {"secret-credential"}})
		if action != "" || err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("Parse returned %q, %v; want empty action and sanitized error", action, err)
		}
	}
}
