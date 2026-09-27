package edge

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBufferedMatchesHTTP(t *testing.T) {
	for _, tt := range []struct {
		name    string
		method  string
		handler http.HandlerFunc
	}{
		{name: "untouched", handler: func(http.ResponseWriter, *http.Request) {}},
		{name: "empty_write", handler: func(w http.ResponseWriter, _ *http.Request) { w.Write(nil) }},
		{name: "commitment", handler: func(w http.ResponseWriter, _ *http.Request) {
			values := []string{"before"}
			w.Header()["Custom"] = values
			w.WriteHeader(http.StatusCreated)
			values[0] = "after"
			w.Header().Set("Content-Type", "changed/after-commit")
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, "body")
		}},
		{name: "split_sniff", handler: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, "<")
			io.WriteString(w, "html><body>hello</body></html>")
		}},
		{name: "suppressed_type", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header()["Content-Type"] = nil
			io.WriteString(w, "body")
		}},
		{name: "suppressed_length", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header()["Content-Length"] = nil
			io.WriteString(w, "body")
		}},
		{name: "declared_length", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "4")
			io.WriteString(w, "body")
		}},
		{name: "encoded_no_sniff", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "identity")
			io.WriteString(w, "body")
		}},
		{name: "application_error", handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "denied", http.StatusForbidden) }},
		{name: "redirect", handler: func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/next", http.StatusFound) }},
		{name: "head_empty", method: http.MethodHead, handler: func(http.ResponseWriter, *http.Request) {}},
		{name: "head_representation", method: http.MethodHead, handler: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "representation") }},
		{name: "head_declared", method: http.MethodHead, handler: func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Content-Length", "500") }},
		{name: "no_content", handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "99")
			w.WriteHeader(http.StatusNoContent)
		}},
		{name: "not_modified", handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "99")
			w.WriteHeader(http.StatusNotModified)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			r := httptest.NewRequestWithContext(t.Context(), method, "http://example.com/", nil)
			got, err := serveBuffered(tt.handler, r, defaultResponseHeaderBudget)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTestServer(t, tt.handler)
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			client := server.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			request, err := http.NewRequestWithContext(t.Context(), method, "http://example.com/", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Accept-Encoding", "identity")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if got.status != response.StatusCode || !bytes.Equal(got.body, body) {
				t.Errorf("buffered response = %d %q; HTTP = %d %q", got.status, got.body, response.StatusCode, body)
			}
			for _, name := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Custom", "Location", "X-Content-Type-Options"} {
				if !slices.Equal(got.headers.fields.Values(name), response.Header.Values(name)) {
					t.Errorf("buffered %s = %v; HTTP = %v", name, got.headers.fields.Values(name), response.Header.Values(name))
				}
			}
			if _, present := got.headers.fields["Date"]; present {
				t.Error("buffered writer generated Date")
			}
		})
	}
}

func TestBufferedBodylessStatuses(t *testing.T) {
	for _, status := range []int{204, 205, 304} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
			w.Header().Set("Content-Length", "100")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			if n, err := w.Write([]byte("forbidden")); n != 0 || !errors.Is(err, http.ErrBodyNotAllowed) {
				t.Errorf("bodyless Write = %d, %v; want ErrBodyNotAllowed", n, err)
			}
			if n, err := w.Write(nil); n != 0 || err != nil {
				t.Errorf("bodyless empty Write = %d, %v; want 0, nil", n, err)
			}
			result, err := w.finish()
			if err != nil || result == nil {
				t.Fatalf("finish = %v, %v; body rejection alone must not fail response", result, err)
			}
			if len(result.body) != 0 || result.headers.fields.Get("Content-Length") != "" {
				t.Errorf("bodyless response contains body or length: %+v", result)
			}
			wantType := "application/json"
			if status == 304 {
				wantType = ""
			}
			if got := result.headers.fields.Get("Content-Type"); got != wantType {
				t.Errorf("Content-Type = %q; want %q", got, wantType)
			}
		})
	}
}

func TestBufferedStickyFaults(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(*bufferedWriter)
		want  error
	}{
		{name: "length_overflow", want: http.ErrContentLength, setup: func(w *bufferedWriter) {
			w.Header().Set("Content-Length", "2")
			w.Write([]byte("too long"))
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(200)
		}},
		{name: "header_budget", want: errResponseHeaderBudget, setup: func(w *bufferedWriter) { w.Header().Set("Huge", strings.Repeat("x", defaultResponseHeaderBudget)) }},
		{name: "invalid_header", want: errResponseHeaders, setup: func(w *bufferedWriter) { w.Header().Set("Custom", "bad\nvalue") }},
		{name: "information", want: http.ErrNotSupported, setup: func(w *bufferedWriter) { w.WriteHeader(103); w.WriteHeader(200) }},
		{name: "upgrade", want: http.ErrNotSupported, setup: func(w *bufferedWriter) { w.WriteHeader(101) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
			tt.setup(w)
			if n, err := w.Write([]byte("later")); n != 0 || !errors.Is(err, tt.want) {
				t.Errorf("Write after fault = %d, %v; want 0, %v", n, err, tt.want)
			}
			if result, err := w.finish(); result != nil || !errors.Is(err, tt.want) {
				t.Errorf("finish after ignored error = %v, %v; want nil, %v", result, err, tt.want)
			}
		})
	}
	for _, status := range []int{-1, 0, 99, 600, 999} {
		w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
		w.WriteHeader(status)
		w.WriteHeader(200)
		if result, err := w.finish(); result != nil || err == nil {
			t.Errorf("invalid initial status %d = %v, %v; want rejection", status, result, err)
		}
	}
	w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
	w.WriteHeader(201)
	for _, status := range []int{202, 103, 101, 0, 999} {
		w.WriteHeader(status)
	}
	if result, err := w.finish(); err != nil || result.status != 201 {
		t.Errorf("superfluous statuses changed commitment: %v, %v", result, err)
	}
}

func TestBufferedLengthMismatchAndHead(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := newBufferedWriter(method, defaultResponseHeaderBudget)
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("small"))
		result, err := w.finish()
		if method == http.MethodGet {
			if result != nil || !errors.Is(err, http.ErrContentLength) {
				t.Errorf("GET short body = %v, %v; want mismatch error", result, err)
			}
		} else if err != nil || result == nil || len(result.body) != 0 || result.headers.fields.Get("Content-Length") != "100" {
			t.Errorf("HEAD short representation = %v, %v; want declared length without body", result, err)
		}
	}
	w := newBufferedWriter(http.MethodHead, defaultResponseHeaderBudget)
	data := bytes.Repeat([]byte("x"), maxResponseBytes+1)
	if n, err := w.Write(data); err != nil || n != len(data) {
		t.Fatalf("HEAD large Write = %d, %v; want count without storing body", n, err)
	}
	if len(w.body.data) != 512 || cap(w.body.data) != 512 {
		t.Errorf("HEAD retained length/capacity = %d/%d; want 512/512", len(w.body.data), cap(w.body.data))
	}
	result, err := w.finish()
	if err != nil || len(result.body) != 0 || result.headers.fields.Get("Content-Length") != strconv.Itoa(len(data)) {
		t.Errorf("HEAD large finish = %v, %v; want counted length and no body", result, err)
	}
	r := httptest.NewRequest(http.MethodHead, "/", nil)
	result, err = serveBuffered(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Method = http.MethodGet
		io.WriteString(w, "representation")
	}), r, defaultResponseHeaderBudget)
	if err != nil || len(result.body) != 0 {
		t.Errorf("request mutation changed HEAD behavior: %v, %v", result, err)
	}
}

func TestBufferedLimitsAndEncoding(t *testing.T) {
	w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
	w.Header().Set("Content-Type", "text/plain")
	data := bytes.Repeat([]byte("x"), maxResponseBytes)
	if n, err := w.Write(data); n != len(data) || err != nil {
		t.Fatalf("exact-limit Write = %d, %v", n, err)
	}
	result, err := w.finish()
	if err != nil {
		t.Fatal(err)
	}
	typed, err := result.v1()
	if err != nil || len(typed.Body) != maxResponseBytes {
		t.Fatalf("typed response = body length %d, %v; want exact body ceiling", len(typed.Body), err)
	}
	if raw, err := marshalResponse(typed); raw != nil || !errors.Is(err, errResponseTooLarge) {
		t.Errorf("raw envelope at body ceiling = %d bytes, %v; want overhead rejection", len(raw), err)
	}
	w = newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("x")); n != 0 || !errors.Is(err, errResponseTooLarge) || len(w.body.data) != maxResponseBytes || cap(w.body.data) > maxResponseBytes {
		t.Errorf("over-limit Write = %d, %v; retained length/cap %d/%d", n, err, len(w.body.data), cap(w.body.data))
	}
	if result, err := w.finish(); result != nil || !errors.Is(err, errResponseTooLarge) {
		t.Errorf("ignored body overflow = %v, %v; want no response", result, err)
	}
	for _, data := range [][]byte{[]byte("hello"), {0, 255, 1}} {
		w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.Header().Add("Vary", "Origin")
		w.Header().Add("Vary", "Accept-Encoding")
		w.Write(data)
		result, err := w.finish()
		if err != nil {
			t.Fatal(err)
		}
		v1, err := result.v1()
		if err != nil {
			t.Fatal(err)
		}
		v2, err := result.v2()
		if err != nil {
			t.Fatal(err)
		}
		if v1.Headers != nil || v2.MultiValueHeaders != nil || !slices.Equal(v1.MultiValueHeaders["Set-Cookie"], v2.Cookies) || v2.Headers["Vary"] != "Origin, Accept-Encoding" {
			t.Errorf("gateway header projection differs: V1=%v V2=%v", v1, v2)
		}
		decoded := []byte(v2.Body)
		if v2.IsBase64Encoded {
			decoded, err = base64.StdEncoding.DecodeString(v2.Body)
		}
		if err != nil || !bytes.Equal(decoded, data) || v1.Body != v2.Body || v1.IsBase64Encoded != v2.IsBase64Encoded {
			t.Errorf("gateway body changed: V1=%q V2=%q binary=%t error=%v", v1.Body, v2.Body, v2.IsBase64Encoded, err)
		}
	}
}

func TestBufferedCapabilitiesAndLateTrailers(t *testing.T) {
	w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
	var handlerWriter http.ResponseWriter = w
	if _, ok := handlerWriter.(http.Flusher); ok {
		t.Error("buffered writer advertises flushing")
	}
	if _, ok := handlerWriter.(http.Hijacker); ok {
		t.Error("buffered writer advertises hijacking")
	}
	if _, ok := handlerWriter.(http.Pusher); ok {
		t.Error("buffered writer advertises pushing")
	}
	controller := http.NewResponseController(handlerWriter)
	for _, err := range []error{controller.Flush(), controller.EnableFullDuplex(), controller.SetReadDeadline(time.Now()), controller.SetWriteDeadline(time.Now())} {
		if !errors.Is(err, http.ErrNotSupported) {
			t.Errorf("ResponseController capability returned %v; want ErrNotSupported", err)
		}
	}
	for _, name := range []string{http.TrailerPrefix + "Digest", "Trailer", "trailer"} {
		w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
		io.WriteString(w, "committed")
		w.Header()[name] = []string{"Digest"}
		if result, err := w.finish(); result != nil || !errors.Is(err, http.ErrNotSupported) {
			t.Errorf("late %s = %v, %v; want unsupported trailer fault", name, result, err)
		}
	}
}
