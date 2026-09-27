package streamprobe

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Record the Go 1.27 reference behavior separately from the proposed edge writer.
func TestHTTPStreamingHeaderReference(t *testing.T) {
	for _, tc := range []struct {
		name         string
		encoding     []string
		suppressType bool
		flushFirst   bool
		wantType     string
	}{
		{name: "nil_encoding", wantType: "text/html; charset=utf-8"},
		{name: "empty_encoding", encoding: []string{""}, wantType: "text/html; charset=utf-8"},
		{name: "encoded", encoding: []string{"gzip"}},
		{name: "suppressed_type", suppressType: true},
		{name: "empty_flush", flushFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header()["Content-Encoding"] = tc.encoding
				if tc.suppressType {
					w.Header()["Content-Type"] = nil
				}
				if tc.flushFirst {
					if err := http.NewResponseController(w).Flush(); err != nil {
						t.Error(err)
						return
					}
				}
				if _, err := io.WriteString(w, "<html><body>example</body></html>"); err != nil {
					t.Error(err)
				}
			}))
			r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/", nil)
			if err != nil {
				t.Fatal(err)
			}
			// Only header behavior is under test;
			// disable automatic decompression for the synthetic encoding fixture.
			r.Header.Set("Accept-Encoding", "identity")
			response, err := server.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if _, err := io.Copy(io.Discard, response.Body); err != nil {
				t.Fatal(err)
			}
			if got := response.Header.Get("Content-Type"); got != tc.wantType {
				t.Errorf("Content-Type=%q; want %q", got, tc.wantType)
			}
			t.Logf("content-type=%q content-length=%q transfer-encoding=%v", response.Header.Get("Content-Type"), response.Header.Get("Content-Length"), response.TransferEncoding)
		})
	}
}
