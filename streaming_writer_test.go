package edge

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
)

type streamMetadata struct {
	StatusCode int         `json:"statusCode"`
	Headers    http.Header `json:"multiValueHeaders"`
}

func parseStream(t testing.TB, wire []byte) (streamMetadata, []byte) {
	t.Helper()
	prefix, body, ok := bytes.Cut(wire, make([]byte, 8))
	if !ok {
		t.Fatalf("stream has no delimiter: %q", wire)
	}
	var meta streamMetadata
	if err := json.Unmarshal(prefix, &meta); err != nil {
		t.Fatal(err)
	}
	return meta, body
}

func TestContentEncodingSniffingShared(t *testing.T) {
	for _, tc := range []struct {
		name     string
		encoding []string
		suppress bool
		want     string
	}{
		{"absent", nil, false, "text/html; charset=utf-8"},
		{"empty", []string{""}, false, "text/html; charset=utf-8"},
		{"blank", []string{" \t "}, false, "text/html; charset=utf-8"},
		{"encoded", []string{"gzip"}, false, ""},
		{"later_encoding", []string{"", "gzip"}, false, ""},
		{"suppressed_type", []string{""}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header()["Content-Encoding"] = tc.encoding
				if tc.suppress {
					w.Header()["Content-Type"] = nil
				}
				_, _ = io.WriteString(w, "<html><body>example</body></html>")
			})
			buffered, err := serveBuffered(h, &http.Request{Method: "GET"}, defaultResponseHeaderBudget)
			if err != nil {
				t.Fatal(err)
			}
			var wire bytes.Buffer
			out := &streamOutput{publish: func(p []byte) error { _, err := wire.Write(p); return err }, body: &wire}
			w := newStreamingWriter(t.Context(), "GET", defaultResponseHeaderBudget, out)
			h.ServeHTTP(w, nil)
			if err := w.finish(); err != nil {
				t.Fatal(err)
			}
			meta, _ := parseStream(t, wire.Bytes())
			if got := meta.Headers.Get("Content-Type"); got != tc.want || buffered.headers.fields.Get("Content-Type") != tc.want {
				t.Errorf("type: stream=%q buffered=%q; want %q", got, buffered.headers.fields.Get("Content-Type"), tc.want)
			}
		})
	}
}

func TestStreamingWriterCommitmentAndSniffing(t *testing.T) {
	var wire bytes.Buffer
	publications := 0
	out := &streamOutput{publish: func(p []byte) error { publications++; _, err := wire.Write(p); return err }, body: &wire}
	w := newStreamingWriter(t.Context(), "GET", defaultResponseHeaderBudget, out)
	values := []string{"before"}
	w.Header()["X-Value"] = values
	w.Header().Add("Set-Cookie", "a=1")
	w.Header().Add("Set-Cookie", "b=2")
	w.WriteHeader(201)
	values[0] = "after"
	w.Header().Set("Content-Type", "ignored/after-commit")
	w.WriteHeader(202)
	_, _ = w.Write(nil)
	_, _ = io.WriteString(w, "<")
	_, _ = io.WriteString(w, "html><body>example</body></html>")
	if publications != 0 {
		t.Fatal("small sniff prefix published before flush/completion")
	}
	if err := http.NewResponseController(w).Flush(); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "tail")
	w.Flush()
	if err := w.finish(); err != nil {
		t.Fatal(err)
	}
	meta, body := parseStream(t, wire.Bytes())
	if publications != 1 || meta.StatusCode != 201 || meta.Headers.Get("X-Value") != "before" || meta.Headers.Get("Content-Type") != "text/html; charset=utf-8" || len(meta.Headers.Values("Set-Cookie")) != 2 || string(body) != "<html><body>example</body></html>tail" {
		t.Fatalf("committed response: publications=%d meta=%+v body=%q", publications, meta, body)
	}
	if meta.Headers.Get("Content-Length") != "" || meta.Headers.Get("Transfer-Encoding") != "" {
		t.Fatal("stream inferred transport framing")
	}
}

func TestStreamingWriterLengthAndBodyPolicies(t *testing.T) {
	for _, tc := range []struct {
		name, method, length string
		status               int
		payload              string
		flush                bool
		wantErr              error
		wantBody, wantLength string
	}{
		{name: "empty"},
		{name: "small", payload: "body", wantBody: "body"},
		{name: "explicit", length: "4", payload: "body", wantBody: "body", wantLength: "4"},
		{name: "zero", length: "0", wantLength: "0"},
		{name: "overflow", length: "2", payload: "body", wantErr: http.ErrContentLength},
		{name: "short_before", length: "9", payload: "body", wantErr: http.ErrContentLength},
		{name: "short_after", length: "9", payload: "body", flush: true, wantErr: http.ErrContentLength, wantBody: "body", wantLength: "9"},
		{name: "head", method: "HEAD", payload: "representation"},
		{name: "head_explicit", method: "HEAD", length: "99", payload: "representation", wantLength: "99"},
		{name: "head_omitted", method: "HEAD", length: "99", wantLength: "99"},
		{name: "head_overflow", method: "HEAD", length: "2", payload: "representation", wantErr: http.ErrContentLength},
		{name: "no_content", status: 204, payload: "forbidden"},
		{name: "reset", status: 205, payload: "forbidden"},
		{name: "not_modified", status: 304, payload: "forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire bytes.Buffer
			out := &streamOutput{publish: func(p []byte) error { _, err := wire.Write(p); return err }, body: &wire}
			w := newStreamingWriter(t.Context(), tc.method, defaultResponseHeaderBudget, out)
			if tc.length != "" {
				w.Header().Set("Content-Length", tc.length)
			}
			if tc.status != 0 {
				w.WriteHeader(tc.status)
			}
			n, writeErr := io.WriteString(w, tc.payload)
			if tc.status != 0 && (n != 0 || !errors.Is(writeErr, http.ErrBodyNotAllowed)) {
				t.Errorf("bodyless write=%d %v", n, writeErr)
			}
			if tc.flush {
				_ = w.FlushError()
			}
			err := w.finish()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("finish=%v; want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil && !tc.flush {
				if wire.Len() != 0 {
					t.Fatal("invalid unpublished response leaked data")
				}
				return
			}
			meta, body := parseStream(t, wire.Bytes())
			if string(body) != tc.wantBody || meta.Headers.Get("Content-Length") != tc.wantLength {
				t.Errorf("body=%q length=%q; want %q %q", body, meta.Headers.Get("Content-Length"), tc.wantBody, tc.wantLength)
			}
		})
	}
}

func TestStreamingWriterStickyFaults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*streamingWriter)
		want  error
	}{
		{"informational", func(w *streamingWriter) { w.WriteHeader(103) }, http.ErrNotSupported},
		{"invalid_status", func(w *streamingWriter) { w.WriteHeader(600) }, ErrResponse},
		{"bad_header", func(w *streamingWriter) { w.Header().Set("Bad Header", "x"); w.WriteHeader(200) }, ErrResponse},
		{"bad_length", func(w *streamingWriter) { w.Header().Set("Content-Length", "-1"); w.WriteHeader(200) }, ErrResponse},
		{"header_budget", func(w *streamingWriter) { w.headerBudget = 1; w.Header().Set("X-Test", "x"); w.WriteHeader(200) }, ErrLimitExceeded},
		{"generated_type_budget", func(w *streamingWriter) { w.headerBudget = 1; _, _ = w.Write([]byte("body")); w.Flush() }, ErrLimitExceeded},
		{"prefix_size", func(w *streamingWriter) { w.Header().Set("Large", strings.Repeat("x", 16000)); w.Flush() }, ErrLimitExceeded},
		{"trailer", func(w *streamingWriter) { w.WriteHeader(200); w.Header().Set("Trailer", "X-Late"); w.Flush() }, http.ErrNotSupported},
		{"overflow_counter", func(w *streamingWriter) { w.written = math.MaxInt64; _, _ = w.Write([]byte("x")) }, ErrResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire bytes.Buffer
			w := newStreamingWriter(t.Context(), "GET", defaultResponseHeaderBudget, &streamOutput{publish: func(p []byte) error { _, err := wire.Write(p); return err }, body: &wire})
			tc.setup(w)
			w.WriteHeader(200)
			if _, err := w.Write([]byte("ignored failure")); !errors.Is(err, tc.want) {
				t.Errorf("Write=%v; want %v", err, tc.want)
			}
			if err := w.finish(); !errors.Is(err, tc.want) || wire.Len() != 0 {
				t.Errorf("finish=%v published=%d", err, wire.Len())
			}
		})
	}
}

func TestStreamingEmptyFlushAndCapabilities(t *testing.T) {
	var wire bytes.Buffer
	w := newStreamingWriter(t.Context(), "GET", defaultResponseHeaderBudget, &streamOutput{publish: func(p []byte) error { _, err := wire.Write(p); return err }, body: &wire})
	controller := http.NewResponseController(w)
	if err := controller.Flush(); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "<html>after flush</html>")
	if err := w.finish(); err != nil {
		t.Fatal(err)
	}
	meta, body := parseStream(t, wire.Bytes())
	if meta.Headers.Get("Content-Type") != "" || meta.Headers.Get("Content-Length") != "" || len(body) == 0 {
		t.Fatalf("empty flush invented metadata: %+v", meta)
	}
	if err := controller.EnableFullDuplex(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatal(err)
	}
	if _, _, err := controller.Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatal(err)
	}
	if _, ok := any(w).(http.Pusher); ok {
		t.Fatal("unexpected push capability")
	}
}

func TestStreamingWriterBackpressureAndSniffThreshold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var completed bool
		s, err := startStream(t.Context(), prepareEmptyStream, func(ctx context.Context, out *streamOutput) error {
			w := newStreamingWriter(ctx, "GET", defaultResponseHeaderBudget, out)
			if n, err := w.Write(bytes.Repeat([]byte("a"), 511)); n != 511 || err != nil {
				return errors.New("initial write failed")
			}
			// Filling byte 512 must publish, then block until the reader drains it.
			if _, err := w.Write([]byte("bc")); err != nil {
				return err
			}
			completed = true
			return w.finish()
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		synctest.Wait()
		if completed {
			t.Fatal("writer completed without a body consumer")
		}
		wire, err := io.ReadAll(s)
		if err != nil {
			t.Fatal(err)
		}
		_, body := parseStream(t, wire)
		if len(body) != 513 || !completed || body[511] != 'b' || body[512] != 'c' {
			t.Fatalf("body length=%d completed=%v", len(body), completed)
		}
	})
}

type partialStreamWriter struct{}

func (partialStreamWriter) Write(p []byte) (int, error) {
	return min(2, len(p)), errors.New("private write detail")
}

func TestStreamingPartialWriteCountsAndSanitization(t *testing.T) {
	for _, sniff := range []bool{false, true} {
		w := newStreamingWriter(t.Context(), "GET", defaultResponseHeaderBudget, &streamOutput{publish: func([]byte) error { return nil }, body: partialStreamWriter{}})
		if sniff {
			if n, err := w.Write(bytes.Repeat([]byte("x"), 511)); n != 511 || err != nil {
				t.Fatalf("buffered Write=%d %v", n, err)
			}
		} else {
			w.Header().Set("Content-Type", "text/plain")
		}
		n, err := w.Write([]byte("tail"))
		want := 2
		if sniff {
			want = 1
		} // The accepted sniff byte belongs to this call; the tail does not.
		if n != want || !errors.Is(err, ErrStream) || strings.Contains(err.Error(), "private") {
			t.Fatalf("Write=%d %v; want %d sanitized stream error", n, err, want)
		}
		if n, err := w.Write([]byte("again")); n != 0 || !errors.Is(err, ErrStream) {
			t.Fatalf("sticky Write=%d %v", n, err)
		}
		if err := w.finish(); !errors.Is(err, ErrStream) {
			t.Fatal(err)
		}
	}
}
