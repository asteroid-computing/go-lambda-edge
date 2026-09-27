package edge

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/actionheader"
)

func TestActionSelectorAcrossRequestFormats(t *testing.T) {
	selector, err := actionheader.NewSelector("Action")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Custom-Trace") != "keep" {
			http.Error(w, "lost custom header", http.StatusInternalServerError)
			return
		}
		action, err := selector.Parse(r.Header)
		if err != nil {
			http.Error(w, "invalid action selection", http.StatusBadRequest)
			return
		}
		io.WriteString(w, action)
	}))
	for _, tt := range []struct {
		name    string
		values  []string
		want    string
		wantErr error
	}{
		{name: "single", values: []string{"Orders.Create"}, want: "Orders.Create"},
		{name: "outer_whitespace", values: []string{" \tOrders.Create\t "}, want: "Orders.Create"},
		{name: "missing", wantErr: actionheader.ErrActionMissing},
		{name: "empty", values: []string{""}, wantErr: actionheader.ErrActionInvalid},
		{name: "invalid", values: []string{"orders/create"}, wantErr: actionheader.ErrActionInvalid},
		{name: "duplicates", values: []string{"read", "delete"}, wantErr: actionheader.ErrActionAmbiguous},
		{name: "identical_duplicates", values: []string{"read", "read"}, wantErr: actionheader.ErrActionAmbiguous},
		{name: "literal_comma", values: []string{"read,delete"}, wantErr: actionheader.ErrActionAmbiguous},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, format := range []string{"typed_v1", "raw_rest", "raw_http_v1", "typed_v2", "raw_http_v2", "native_http"} {
				t.Run(format, func(t *testing.T) {
					if format == "native_http" {
						r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/", nil)
						if err != nil {
							t.Fatal(err)
						}
						r.Header.Set("Custom-Trace", "keep")
						for _, value := range tt.values {
							r.Header.Add("Action", value)
						}
						response, err := server.Client().Do(r)
						if err != nil {
							t.Fatal(err)
						}
						defer response.Body.Close()
						body, err := io.ReadAll(response.Body)
						if err != nil {
							t.Fatal(err)
						}
						wantStatus := http.StatusOK
						if tt.wantErr != nil {
							wantStatus = http.StatusBadRequest
						}
						if response.StatusCode != wantStatus || tt.wantErr == nil && string(body) != tt.want {
							t.Errorf("HTTP response = %d %q; want status %d, action %q", response.StatusCode, body, wantStatus, tt.want)
						}
						return
					}
					r := actionGatewayRequest(t, format, tt.values)
					defer r.Body.Close()
					got, err := selector.Parse(r.Header)
					if got != tt.want || !errors.Is(err, tt.wantErr) {
						t.Errorf("Parse translated headers = %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
					}
					if r.Header.Get("Custom-Trace") != "keep" {
						t.Error("request conversion lost unrelated custom header")
					}
				})
			}
		})
	}
}

func actionGatewayRequest(t *testing.T, format string, values []string) *http.Request {
	t.Helper()
	v1, v2 := v1RequestEvent(), v2RequestEvent()
	v2.Version = "2.0"
	v1.Headers = map[string]string{"Custom-Trace": "keep"}
	v2.Headers = map[string]string{"custom-trace": "keep"}
	if len(values) != 0 {
		// A value mirrored by V1's single and multivalue maps is not an extra field line.
		// Actual repeats in the multivalue list must survive.
		v1.Headers["action"] = values[0]
		v1.MultiValueHeaders = map[string][]string{"ACTION": values}
		v2.Headers["action"] = strings.Join(values, ",")
	}
	var r *http.Request
	var err error
	switch format {
	case "typed_v1":
		r, err = requestV1(t.Context(), v1)
	case "typed_v2":
		r, err = requestV2(t.Context(), v2)
	default:
		var event any = v1
		if format == "raw_http_v2" {
			event = v2
		}
		payload, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if format == "raw_http_v1" {
			payload = append([]byte(`{"version":"1.0",`), payload[1:]...)
		}
		decoded, decodeErr := decodeEvent(payload)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if decoded.v2 != nil {
			r, err = requestV2(t.Context(), *decoded.v2)
		} else {
			r, err = requestV1(t.Context(), *decoded.v1)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return r
}
