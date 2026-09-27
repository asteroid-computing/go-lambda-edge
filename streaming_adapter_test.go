package edge_test

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	"github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func restEvent() events.APIGatewayProxyRequest {
	return events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: "/orders", RequestContext: events.APIGatewayProxyRequestContext{APIID: "example"}}
}

func streamWire(t testing.TB, s io.ReadCloser) (int, http.Header, []byte, error) {
	t.Helper()
	if s == nil {
		t.Fatal("nil stream")
	}
	wire, err := io.ReadAll(s)
	closeErr := s.Close()
	if !errors.Is(closeErr, err) {
		t.Errorf("Close=%v read=%v", closeErr, err)
	}
	prefix, body, ok := bytes.Cut(wire, make([]byte, 8))
	if !ok {
		t.Fatalf("missing delimiter: %q", wire)
	}
	var meta struct {
		Status  int         `json:"statusCode"`
		Headers http.Header `json:"multiValueHeaders"`
	}
	if decodeErr := json.Unmarshal(prefix, &meta); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	return meta.Status, meta.Headers, body, err
}

func TestStreamingConstructorsAndValidation(t *testing.T) {
	var mux *http.ServeMux
	for _, h := range []http.Handler{nil, http.HandlerFunc(nil), mux} {
		if a, err := edge.NewStreaming(h); err == nil || a != nil {
			t.Errorf("nil handler accepted: %v %v", a, err)
		}
	}
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid invocation reached handler") })
	for _, opts := range [][]edge.Option{{nil}, {edge.WithResponseHeaderBudget(0)}, {edge.WithIdentityClaimsBudget(0)}, {edge.WithStreamErrorReporter(nil)}} {
		if a, err := edge.NewStreaming(h, opts...); err == nil || a != nil {
			t.Errorf("invalid options accepted: %v %v", a, err)
		}
	}
	report := edge.WithStreamErrorReporter(func(context.Context, error) { t.Error("invalid invocation reported as late failure") })
	if a, err := edge.New(h, report); err == nil || a != nil {
		t.Fatal("buffered constructor accepted stream reporter")
	}
	a, err := edge.NewStreaming(h, edge.WithStreamErrorReporter(nil), report)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(restEvent())
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []jsontext.Value{nil, []byte("null"), []byte(`{"httpMethod":"GET","httpMethod":"POST"}`), append([]byte(`{"version":"1.0",`), wire[1:]...), []byte(`{"version":"2.0","rawPath":"/","requestContext":{"apiId":"api","http":{"method":"GET"}}}`)} {
		if s, err := a.Handle(t.Context(), input); err == nil || s != nil {
			t.Errorf("invalid/unsupported event returned stream=%v err=%v", s, err)
		}
	}
	if s, err := a.HandleV1(nil, restEvent()); !errors.Is(err, edge.ErrInvalidInvocation) || s != nil {
		t.Errorf("nil context: %v %v", s, err)
	}
	if s, err := a.HandleV1(t.Context(), events.APIGatewayProxyRequest{}); !errors.Is(err, edge.ErrInvalidEvent) || s != nil {
		t.Errorf("invalid typed event: %v %v", s, err)
	}
	caller, err := identity.NewIAM("arn:aws:iam::123456789012:user/Alice", identity.SourceCustomAssertion)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := identity.WithCaller(t.Context(), caller)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := a.Handle(ctx, wire); !errors.Is(err, identity.ErrConflict) || s != nil {
		t.Errorf("inherited identity: %v %v", s, err)
	}
	for _, bad := range []*edge.StreamingAdapter{nil, new(edge.StreamingAdapter)} {
		if s, err := bad.HandleV1(t.Context(), restEvent()); !errors.Is(err, edge.ErrInvalidInvocation) || s != nil {
			t.Errorf("zero adapter: %v %v", s, err)
		}
	}
}

func TestStreamingSDKRegistrationAndBinary(t *testing.T) {
	a, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orders" {
			t.Error("wrong path")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(400)
		_, _ = w.Write([]byte{0, 255, 1})
	}))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(restEvent())
	if err != nil {
		t.Fatal(err)
	}
	// This collector verifies registration/serialization, not incremental delivery.
	// The actual Runtime API subprocess probe covers incremental transport.
	for _, handler := range []any{a.Handle, a.HandleV1} {
		out, err := lambda.NewHandler(handler).Invoke(t.Context(), wire)
		if err != nil {
			t.Fatal(err)
		}
		status, headers, body, err := streamWire(t, io.NopCloser(bytes.NewReader(out)))
		if err != nil || status != 400 || headers.Get("Content-Length") != "" || !bytes.Equal(body, []byte{0, 255, 1}) {
			t.Fatalf("stream response=%d %v %v %v", status, headers, body, err)
		}
	}
}

func TestStreamingLateErrorsAndReporting(t *testing.T) {
	for _, mode := range []string{"short", "trailer", "panic", "reporter_panic"} {
		t.Run(mode, func(t *testing.T) {
			var reports atomic.Int64
			a, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				if mode == "short" {
					w.Header().Set("Content-Length", "99")
				}
				if _, err := io.WriteString(w, "first"); err != nil {
					return
				}
				if mode == "panic" || mode == "reporter_panic" {
					panic("private application detail")
				}
				if mode == "trailer" {
					w.Header().Set("Trailer", "X-Private")
				}
			}), edge.WithStreamErrorReporter(func(_ context.Context, err error) {
				reports.Add(1)
				if err == nil || strings.Contains(err.Error(), "private") {
					t.Errorf("unsafe report: %v", err)
				}
				if mode == "reporter_panic" {
					panic("private reporter detail")
				}
			}))
			if err != nil {
				t.Fatal(err)
			}
			s, err := a.HandleV1(t.Context(), restEvent())
			if err != nil {
				t.Fatal(err)
			}
			status, _, body, readErr := streamWire(t, s)
			want := edge.ErrStream
			if mode == "short" {
				want = http.ErrContentLength
			}
			if mode == "trailer" {
				want = http.ErrNotSupported
			}
			if status != 200 || string(body) != "first" || !errors.Is(readErr, want) || reports.Load() != 1 {
				t.Fatalf("status=%d body=%q err=%v reports=%d", status, body, readErr, reports.Load())
			}
		})
	}
}

func TestStreamingEarlyFailuresAndPanic(t *testing.T) {
	for _, mode := range []string{"panic", "prefix", "short"} {
		t.Run(mode, func(t *testing.T) {
			a, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "panic":
					panic("early panic")
				case "prefix":
					w.Header().Set("Large", strings.Repeat("x", 16000))
					w.(http.Flusher).Flush()
				case "short":
					w.Header().Set("Content-Length", "10")
					_, _ = io.WriteString(w, "short")
				}
			}), edge.WithStreamErrorReporter(func(context.Context, error) { t.Error("early failure was reported") }))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				got := recover()
				if mode == "panic" && got != "early panic" || mode != "panic" && got != nil {
					t.Errorf("panic=%v", got)
				}
			}()
			s, err := a.HandleV1(t.Context(), restEvent())
			if s != nil || err == nil {
				t.Errorf("early failure returned %v %v", s, err)
			}
		})
	}
}

func TestStreamingCloseAndParentCancellation(t *testing.T) {
	for _, parentCancel := range []bool{false, true} {
		ctx, cancel := context.WithCancelCause(t.Context())
		var finished, reports atomic.Int64
		a, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer finished.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			if _, err := w.Write(bytes.Repeat([]byte("x"), 1024*1024)); !errors.Is(err, context.Canceled) {
				t.Errorf("interrupted write=%v", err)
			}
		}), edge.WithStreamErrorReporter(func(_ context.Context, err error) {
			reports.Add(1)
			if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "secret") {
				t.Errorf("report=%v", err)
			}
		}))
		if err != nil {
			t.Fatal(err)
		}
		s, err := a.HandleV1(ctx, restEvent())
		if err != nil {
			t.Fatal(err)
		}
		if parentCancel {
			cancel(errors.New("secret cause"))
		}
		if err := s.Close(); !errors.Is(err, context.Canceled) {
			t.Errorf("Close=%v", err)
		}
		if err := s.Close(); !errors.Is(err, context.Canceled) {
			t.Error("Close not idempotent")
		}
		cancel(nil)
		if finished.Load() != 1 || reports.Load() != 1 {
			t.Fatalf("handler/reporter incomplete: %d %d", finished.Load(), reports.Load())
		}
	}
}

func TestStreamingGatewayIdentityAndIsolation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		a, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			j, ok := identity.FromContext(r.Context()).JWT()
			value := "anonymous"
			if ok {
				value, _ = j.Subject()
			}
			_, _ = io.WriteString(w, value)
		}), edge.WithGatewayIdentity(enabled))
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 10 {
			for _, subject := range []string{"Alice", "Bob"} {
				wg.Go(func() {
					for _, raw := range []bool{true, false} {
						event := restEvent()
						event.RequestContext.Authorizer = map[string]any{"claims": map[string]any{"iss": "issuer", "sub": subject}}
						var s io.ReadCloser
						var err error
						if raw {
							wire, marshalErr := json.Marshal(event)
							if marshalErr != nil {
								t.Error(marshalErr)
								return
							}
							s, err = a.Handle(t.Context(), wire)
						} else {
							s, err = a.HandleV1(t.Context(), event)
						}
						if err != nil {
							t.Error(err)
							return
						}
						_, _, body, err := streamWire(t, s)
						want := "anonymous"
						if enabled {
							want = subject
						}
						if err != nil || string(body) != want {
							t.Errorf("identity body=%q err=%v; want %q", body, err, want)
						}
					}
				})
			}
		}
		wg.Wait()
	}
}

func TestStreamingMultipartCleanupBeforeEOF(t *testing.T) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("upload", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, "temporary file content")
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	var path string
	var served context.Context
	a, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = r.Context()
		if err := r.ParseMultipartForm(0); err != nil {
			t.Error(err)
			return
		}
		file, err := r.MultipartForm.File["upload"][0].Open()
		if err != nil {
			t.Error(err)
			return
		}
		if disk, ok := file.(*os.File); ok {
			path = disk.Name()
		}
		_ = file.Close()
		_, _ = io.WriteString(w, "done")
	}))
	if err != nil {
		t.Fatal(err)
	}
	event := restEvent()
	event.HTTPMethod, event.Body = "POST", body.String()
	event.Headers = map[string]string{"Content-Type": form.FormDataContentType()}
	s, err := a.HandleV1(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	_, _, output, err := streamWire(t, s)
	if err != nil || string(output) != "done" || path == "" {
		t.Fatalf("body=%q path=%q err=%v", output, path, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("multipart file remains: %v", err)
	}
	if served.Err() != context.Canceled {
		t.Error("invocation context survived consumption")
	}
}

func TestStreamingDoesNotUseBufferedEnvelopeLimit(t *testing.T) {
	chunk := bytes.Repeat([]byte("x"), 1024*1024)
	a, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		for range 8 {
			if _, err := w.Write(chunk); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	s, err := a.HandleV1(t.Context(), restEvent())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n, err := io.Copy(io.Discard, s)
	if err != nil || n <= 8*1024*1024 || n > 8*1024*1024+16000 {
		t.Fatalf("stream bytes=%d err=%v; want 8 MiB body plus bounded metadata", n, err)
	}
}
