package edge

import (
	"context"
	"io"
	"math"
	"net/http"
)

// streamingWriter has one handler owner and retains at most 512 body bytes.
// Its context belongs to that invocation, not the reusable adapter. Logical
// HTTP commitment freezes headers; publish transfers ownership to the reader.
type streamingWriter struct {
	ctx            context.Context
	out            *streamOutput
	header         http.Header
	headers        *responseHeaders
	headerBudget   int
	method         string
	status         int
	written        int64
	declaredLength int64
	hasLength      bool
	published      bool
	sniff          [512]byte
	used           int
	err            error
}

func newStreamingWriter(ctx context.Context, method string, headerBudget int, out *streamOutput) *streamingWriter {
	return &streamingWriter{ctx: ctx, out: out, header: make(http.Header), method: method, headerBudget: headerBudget}
}

func (w *streamingWriter) Header() http.Header { return w.header }

func (w *streamingWriter) WriteHeader(status int) {
	if w.status != 0 || w.err != nil {
		return
	}
	if status >= 100 && status <= 199 {
		w.err = errInformationalResponse
		return
	}
	if status < 200 || status > 599 {
		w.err = invocationError("response", ErrResponse, "invalid final response status")
		return
	}
	w.status = status
	w.headers, w.err = snapshotResponseHeaders(w.header, w.headerBudget)
	if w.err != nil {
		return
	}
	if !responseBodyAllowed(status) {
		w.headers.remove("Content-Length")
		if status == http.StatusNotModified {
			w.headers.remove("Content-Type")
		}
	}
	w.declaredLength, w.hasLength, w.err = responseContentLength(w.headers.fields)
}

func (w *streamingWriter) ready() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.err == nil {
		w.err = w.ctx.Err()
	}
	return w.err
}

func (w *streamingWriter) Write(p []byte) (int, error) {
	if err := w.ready(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if !responseBodyAllowed(w.status) {
		return 0, http.ErrBodyNotAllowed
	}
	if w.hasLength && int64(len(p)) > w.declaredLength-w.written {
		w.err = invocationError("response", ErrResponse, "response exceeds declared content length", http.ErrContentLength)
		return 0, w.err
	}
	if int64(len(p)) > math.MaxInt64-w.written {
		w.err = invocationError("response", ErrResponse, "response byte count overflow")
		return 0, w.err
	}
	if w.method == http.MethodHead {
		if !w.published && w.headers.canSniffType() {
			w.used += copy(w.sniff[w.used:], p)
		}
		w.written += int64(len(p))
		if !w.published && (!w.headers.canSniffType() || w.used == len(w.sniff)) {
			return len(p), w.publish()
		}
		return len(p), nil
	}
	accepted := 0
	if !w.published && w.headers.canSniffType() {
		accepted = copy(w.sniff[w.used:], p)
		w.used += accepted
		w.written += int64(accepted)
		p = p[accepted:]
		if w.used < len(w.sniff) {
			return accepted, nil
		}
	}
	if err := w.publish(); err != nil {
		return accepted, err
	}
	if len(p) == 0 {
		return accepted, nil
	}
	n, err := w.writeBody(p)
	w.written += int64(n)
	return accepted + n, err
}

// publish validates all metadata before acknowledging handoff, then drains any
// previously accepted sniff bytes. A pipe error cannot undo accepted bytes.
func (w *streamingWriter) publish() error {
	if err := w.ready(); err != nil {
		return err
	}
	if w.err = responseTrailers(w.header); w.err != nil {
		return w.err
	}
	if w.published {
		return nil
	}
	if w.used != 0 && w.headers.canSniffType() {
		w.err = w.headers.automatic("Content-Type", http.DetectContentType(w.sniff[:w.used]))
		if w.err != nil {
			return w.err
		}
	}
	prefix, err := encodeStreamPrefix(w.status, w.headers)
	if err != nil {
		w.err = err
		return err
	}
	if w.err = w.out.publish(prefix); w.err != nil {
		return w.err
	}
	w.published = true
	if w.used != 0 && w.method != http.MethodHead {
		_, err = w.writeBody(w.sniff[:w.used])
	}
	w.used = 0
	return err
}

func (w *streamingWriter) writeBody(p []byte) (int, error) {
	n, err := w.out.body.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		switch {
		case w.ctx.Err() != nil:
			w.err = w.ctx.Err()
		case err == io.ErrClosedPipe:
			w.err = err // The bridge translates its own interruption safely.
		default:
			w.err = invocationError("stream", ErrStream, "response body write failed")
		}
	}
	return n, w.err
}

func (w *streamingWriter) Flush() { _ = w.FlushError() }

func (w *streamingWriter) FlushError() error { return w.publish() }

func (w *streamingWriter) finish() error {
	if err := w.ready(); err != nil {
		return err
	}
	if w.err = responseTrailers(w.header); w.err != nil {
		return w.err
	}
	if w.method != http.MethodHead && w.hasLength && w.written != w.declaredLength {
		w.err = invocationError("response", ErrResponse, "response body length mismatch", http.ErrContentLength)
		return w.err
	}
	return w.publish()
}
