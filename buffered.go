package edge

import (
	"math"
	"net/http"
	"strconv"

	"github.com/aws/aws-lambda-go/events"
)

var errInformationalResponse = invocationError("response", ErrResponse, "informational response is unsupported", http.ErrNotSupported)

// bufferedWriter belongs to one handler invocation.
// Only Header, WriteHeader and Write form its HTTP surface;
// it deliberately exposes no network or streaming capabilities.
// The owning call must finish it after ServeHTTP returns.
type bufferedWriter struct {
	header         http.Header
	headers        *responseHeaders
	headerBudget   int
	method         string
	status         int
	body           responseBuffer
	written        int64
	declaredLength int64
	hasLength      bool
	err            error
}

func newBufferedWriter(method string, headerBudget int) *bufferedWriter {
	w := &bufferedWriter{header: make(http.Header), method: method, headerBudget: headerBudget}
	if method == http.MethodHead {
		w.body.limit = 512
	}
	return w
}

func (w *bufferedWriter) Header() http.Header { return w.header }

func (w *bufferedWriter) WriteHeader(status int) {
	// Like net/http, ignore every subsequent status after final commitment.
	// A fault on the initial attempt cannot be repaired by a later status.
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

func (w *bufferedWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.err != nil {
		return 0, w.err
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
		w.err = limitError("response", "buffered_body", math.MaxInt64, errResponseTooLarge)
		return 0, w.err
	}
	data := p
	if w.method == http.MethodHead {
		data = p[:min(len(p), 512-len(w.body.data))]
	}
	if _, err := w.body.Write(data); err != nil {
		w.err = limitError("response", "buffered_body", maxResponseBytes, errResponseTooLarge)
		return 0, w.err
	}
	w.written += int64(len(p))
	return len(p), nil
}

func responseBodyAllowed(status int) bool {
	return status != http.StatusNoContent && status != http.StatusResetContent && status != http.StatusNotModified
}

// finish freezes transport output without exposing the mutable application header map.
// Errors discard the response, even if the handler ignored Write's failure.
// Header-map changes after commitment are ignored except for trailers, which net/http would otherwise send after the committed response.
func (w *bufferedWriter) finish() (*bufferedResponse, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.err != nil {
		return nil, w.err
	}
	if err := responseTrailers(w.header); err != nil {
		return nil, err
	}
	if w.method != http.MethodHead && w.hasLength && w.written != w.declaredLength {
		return nil, invocationError("response", ErrResponse, "response body length mismatch", http.ErrContentLength)
	}
	if responseBodyAllowed(w.status) {
		if len(w.body.data) > 0 && w.headers.canSniffType() {
			kind := http.DetectContentType(w.body.data[:min(len(w.body.data), 512)])
			if err := w.headers.automatic("Content-Type", kind); err != nil {
				return nil, err
			}
		}
		if w.method != http.MethodHead || w.written > 0 {
			if err := w.headers.automatic("Content-Length", strconv.FormatInt(w.written, 10)); err != nil {
				return nil, err
			}
		}
	}
	body := w.body.data
	if w.method == http.MethodHead {
		body = nil
	}
	return &bufferedResponse{status: w.status, headers: w.headers, body: body}, nil
}

// bufferedResponse owns the finalized response.
// The writer is no longer usable.
// Projections retain the JSON-free typed boundary;
// marshalResponse is separate.
type bufferedResponse struct {
	status  int
	headers *responseHeaders
	body    []byte
}

func (r *bufferedResponse) v1() (events.APIGatewayProxyResponse, error) {
	body, binary, err := encodeResponseBody(r.body, r.headers.fields)
	if err != nil {
		return events.APIGatewayProxyResponse{}, err
	}
	return events.APIGatewayProxyResponse{StatusCode: r.status, MultiValueHeaders: r.headers.v1(), Body: body, IsBase64Encoded: binary}, nil
}

func (r *bufferedResponse) v2() (events.APIGatewayV2HTTPResponse, error) {
	headers, cookies, err := r.headers.v2()
	if err != nil {
		return events.APIGatewayV2HTTPResponse{}, err
	}
	body, binary, err := encodeResponseBody(r.body, r.headers.fields)
	if err != nil {
		return events.APIGatewayV2HTTPResponse{}, err
	}
	return events.APIGatewayV2HTTPResponse{StatusCode: r.status, Headers: headers, Cookies: cookies, Body: body, IsBase64Encoded: binary}, nil
}

// serveBuffered runs inside the caller's invocation scope.
// Capturing the method first prevents a handler's Request mutation from changing HEAD semantics.
// Panics propagate to that scope so cleanup precedes runtime panic handling.
func serveBuffered(handler http.Handler, r *http.Request, headerBudget int) (*bufferedResponse, error) {
	w := newBufferedWriter(r.Method, headerBudget)
	handler.ServeHTTP(w, r)
	return w.finish()
}
