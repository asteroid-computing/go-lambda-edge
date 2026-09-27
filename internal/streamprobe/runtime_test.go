// Package streamprobe tests the pinned SDK transport against a local Runtime API.
// It covers SDK behavior and the actual edge streaming writer without live AWS.
package streamprobe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	"github.com/asteroid-computing/go-lambda-edge"
)

const eventJSON = `{"httpMethod":"GET","path":"/","requestContext":{"apiId":"example"},"number":9007199254740993,"duplicate":1,"duplicate":2}`
const integrationContentType = "application/vnd.awslambda.http-integration-response"

// SDK reader dispatch must retain the concrete value's ContentType and Close
// methods even though the function's declared result is io.ReadCloser.
type probeStream struct {
	body        io.Reader
	url         string
	deferErrors bool
	pending     error
}

func (s *probeStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s.pending != nil {
		return 0, s.pending
	}
	n, err := s.body.Read(p)
	if s.deferErrors && n > 0 && err != nil {
		s.pending = err
		return n, nil
	}
	return n, err
}

func (s *probeStream) ContentType() string { return integrationContentType }
func (s *probeStream) MarshalJSON() ([]byte, error) {
	return nil, errors.New("stream is not a JSON response")
}
func (s *probeStream) Close() error {
	var closeErr error
	if body, ok := s.body.(io.Closer); ok {
		closeErr = body.Close()
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Get(s.url + "/closed")
	if err != nil {
		return errors.Join(closeErr, err)
	}
	return errors.Join(closeErr, response.Body.Close())
}

type gatedTail struct {
	ctx  context.Context // This reader belongs to exactly one invocation.
	url  string
	mode string
	tail *bytes.Reader
	once sync.Once
	err  error
}

func (r *gatedTail) Read(p []byte) (int, error) {
	r.once.Do(func() {
		request, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url+"/continue", nil)
		if err != nil {
			r.err = err
			return
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			r.err = err
			return
		}
		r.err = response.Body.Close()
	})
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.tail.Read(p)
	if r.mode == "data_and_error" && n > 0 || r.mode == "late_error" && err == io.EOF {
		return n, errors.New("synthetic stream failure")
	}
	return n, err
}

func TestRuntimeStreamingProbeProcess(t *testing.T) {
	mode := os.Getenv("EDGE_STREAM_PROBE")
	if mode == "" {
		return
	}
	if strings.HasPrefix(mode, "edge_") {
		runEdgeStream(t, mode)
		return
	}
	if mode == "sdk_gateway" {
		// Exercise the SDK's documented REST response type directly, without
		// edge's adapter or prefix encoder, to isolate Runtime API behavior.
		lambda.Start(func(ctx context.Context) (*events.APIGatewayProxyStreamingResponse, error) {
			url := "http://" + os.Getenv("AWS_LAMBDA_RUNTIME_API")
			body := io.MultiReader(strings.NewReader("first\n"), &gatedTail{ctx: ctx, url: url, tail: bytes.NewReader([]byte("last\n"))})
			return &events.APIGatewayProxyStreamingResponse{
				StatusCode: 200,
				Headers:    map[string]string{"Content-Type": "text/plain"},
				Body:       &probeStream{body: body, url: url},
			}, nil
		})
		return
	}
	makeStream := func(ctx context.Context) (io.ReadCloser, error) {
		prefix, err := json.Marshal(struct {
			StatusCode int                 `json:"statusCode"`
			Headers    map[string][]string `json:"multiValueHeaders"`
		}{StatusCode: 200, Headers: map[string][]string{"Content-Type": {"text/plain"}}})
		if err != nil {
			return nil, err
		}
		prefix = append(prefix, make([]byte, 8)...)
		prefix = append(prefix, "first\n"...)
		tail := "last\n"
		if mode == "late_error" {
			tail = ""
		}
		url := "http://" + os.Getenv("AWS_LAMBDA_RUNTIME_API")
		tailMode := mode
		if mode == "guarded_data_and_error" {
			tailMode = "data_and_error"
		}
		return &probeStream{body: io.MultiReader(bytes.NewReader(prefix), &gatedTail{ctx: ctx, url: url, mode: tailMode, tail: bytes.NewReader([]byte(tail))}), url: url, deferErrors: mode == "guarded_data_and_error"}, nil
	}
	if mode == "typed" {
		lambda.Start(func(ctx context.Context, event events.APIGatewayProxyRequest) (io.ReadCloser, error) {
			if event.HTTPMethod != "GET" || event.Path != "/" || event.RequestContext.APIID != "example" {
				return nil, errors.New("typed input was not decoded")
			}
			return makeStream(ctx)
		})
		return
	}
	lambda.Start(func(ctx context.Context, event jsontext.Value) (io.ReadCloser, error) {
		if string(event) != eventJSON {
			return nil, errors.New("raw event capture changed JSON")
		}
		return makeStream(ctx)
	})
}

func runEdgeStream(t *testing.T, mode string) {
	t.Helper()
	url := "http://" + os.Getenv("AWS_LAMBDA_RUNTIME_API")
	a, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if mode == "edge_late_error" {
			w.Header().Set("Content-Length", "99")
		}
		if _, err := io.WriteString(w, "first\n"); err != nil {
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url+"/continue", nil)
		if err != nil {
			panic(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			panic(err)
		}
		_ = response.Body.Close()
		if mode == "edge_late_error" {
			return
		}
		if mode == "edge_late_panic" {
			panic("synthetic private panic")
		}
		_, _ = io.WriteString(w, "last\n")
	}))
	if err != nil {
		t.Fatal(err)
	}
	observe := func(s io.ReadCloser, err error) (io.ReadCloser, error) {
		if err != nil {
			return nil, err
		}
		content, ok := s.(interface{ ContentType() string })
		if !ok || content.ContentType() != integrationContentType {
			_ = s.Close()
			return nil, errors.New("edge stream lost integration content type")
		}
		// Forward the production reader unchanged; the wrapper only records that
		// SDK Close joined edge cleanup before the next invocation is requested.
		return &probeStream{body: s, url: url}, nil
	}
	if mode == "edge_typed" {
		lambda.Start(func(ctx context.Context, event events.APIGatewayProxyRequest) (io.ReadCloser, error) {
			return observe(a.HandleV1(ctx, event))
		})
		return
	}
	lambda.Start(func(ctx context.Context, event jsontext.Value) (io.ReadCloser, error) {
		return observe(a.Handle(ctx, event))
	})
}

func TestSDKStreamsThroughRuntimeAPI(t *testing.T) {
	for _, mode := range []string{"raw", "typed", "late_error", "data_and_error", "guarded_data_and_error", "edge_raw", "edge_typed", "edge_late_error", "edge_late_panic", "sdk_gateway"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			firstRead := make(chan struct{}, 1)
			release := make(chan struct{})
			closed := make(chan struct{}, 1)
			next := make(chan struct{}, 1)
			type result struct {
				body            []byte
				header          http.Header
				trailer         http.Header
				chunked         bool
				closeConnection bool
				connection      string
				err             error
			}
			results := make(chan result, 1)
			var invokes atomic.Int32
			var nextConnection atomic.Value
			var streamClosed atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/continue":
					select {
					case <-release:
						w.WriteHeader(http.StatusNoContent)
					case <-r.Context().Done():
					}
				case "/closed":
					streamClosed.Store(true)
					closed <- struct{}{}
					w.WriteHeader(http.StatusNoContent)
				case "/2018-06-01/runtime/invocation/next":
					if invokes.Add(1) > 1 {
						if !streamClosed.Load() {
							t.Error("next invocation requested before stream Close completed")
						}
						nextConnection.Store(r.RemoteAddr)
						next <- struct{}{}
						<-r.Context().Done()
						return
					}
					w.Header().Set("Lambda-Runtime-Aws-Request-Id", "probe")
					w.Header().Set("Lambda-Runtime-Deadline-Ms", strconv.FormatInt(time.Now().Add(time.Minute).UnixMilli(), 10))
					input := eventJSON
					if strings.HasPrefix(mode, "edge_") {
						input = strings.Replace(input, `,"duplicate":1,"duplicate":2`, "", 1)
					}
					if _, err := io.WriteString(w, input); err != nil {
						t.Error(err)
					}
				case "/2018-06-01/runtime/invocation/probe/response":
					// Require the first application body segment, not just metadata,
					// before releasing the producer's second segment.
					reader := bufio.NewReader(r.Body)
					var initial []byte
					var err error
					for len(initial) < 16000 && !bytes.HasSuffix(initial, make([]byte, 8)) {
						var b byte
						b, err = reader.ReadByte()
						if err != nil {
							break
						}
						initial = append(initial, b)
					}
					if err == nil {
						first := make([]byte, len("first\n"))
						_, err = io.ReadFull(reader, first)
						initial = append(initial, first...)
					}
					if err != nil {
						results <- result{err: err}
						return
					}
					firstRead <- struct{}{}
					rest, err := io.ReadAll(reader)
					results <- result{body: append(initial, rest...), header: r.Header.Clone(), trailer: r.Trailer.Clone(), chunked: slices.Contains(r.TransferEncoding, "chunked"), closeConnection: r.Close, connection: r.RemoteAddr, err: err}
					w.WriteHeader(http.StatusAccepted)
				default:
					data, err := io.ReadAll(r.Body)
					results <- result{err: errors.New("unexpected Runtime API path " + r.URL.Path + ": " + string(data))}
					if err != nil {
						t.Error(err)
					}
					w.WriteHeader(http.StatusAccepted)
				}
			}))
			defer server.Close()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestRuntimeStreamingProbeProcess$")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "AWS_LAMBDA_RUNTIME_API=") && !strings.HasPrefix(entry, "EDGE_STREAM_PROBE=") && !strings.HasPrefix(entry, "_LAMBDA_SERVER_PORT=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "EDGE_STREAM_PROBE="+mode, "AWS_LAMBDA_RUNTIME_API="+server.Listener.Addr().String())
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cancel()
				// The runtime normally loops forever; this test deliberately
				// terminates its subprocess after observing the next invocation.
				if err := cmd.Wait(); err != nil && ctx.Err() == nil {
					t.Errorf("runtime exited: %v; output: %s", err, output.String())
				}
			}()
			select {
			case <-firstRead:
			case got := <-results:
				t.Fatalf("runtime failed before streaming: %v", got.err)
			case <-ctx.Done():
				t.Fatal("no incremental response before deadline")
			}
			close(release)
			var got result
			select {
			case got = <-results:
			case <-ctx.Done():
				t.Fatal("stream did not finish")
			}
			if got.err != nil {
				t.Fatal(got.err)
			}
			if !got.chunked || got.header.Get("Content-Type") != integrationContentType {
				t.Errorf("stream chunked=%v content-type=%q", got.chunked, got.header.Get("Content-Type"))
			}
			prefix, body, ok := bytes.Cut(got.body, make([]byte, 8))
			if !ok || !jsontext.Value(prefix).IsValid() {
				t.Fatalf("invalid integration framing: %q", got.body)
			}
			wantBody := "first\nlast\n"
			wantError := mode == "late_error" || mode == "data_and_error" || mode == "guarded_data_and_error" || mode == "edge_late_error" || mode == "edge_late_panic"
			if mode == "late_error" || mode == "data_and_error" || mode == "edge_late_error" || mode == "edge_late_panic" {
				wantBody = "first\n"
			}
			if string(body) != wantBody || (got.trailer.Get("Lambda-Runtime-Function-Error-Type") != "") != wantError {
				t.Errorf("body=%q trailers=%v, want body=%q error=%v", body, got.trailer, wantBody, wantError)
			}
			t.Logf("mode=%s incremental=true response-mode=%q runtime-error-trailer=%v", mode, got.header.Get("Lambda-Runtime-Function-Response-Mode"), wantError)
			for _, notification := range []<-chan struct{}{closed, next} {
				select {
				case <-notification:
				case <-ctx.Done():
					t.Fatal("runtime did not close stream before requesting next invocation")
				}
			}
			// Observe compatibility details without asserting that a future SDK
			// must retain today's omissions or connection reuse behavior.
			t.Logf("runtime-request-close=%v response-connection-reused-for-next=%v", got.closeConnection, nextConnection.Load() == got.connection)
		})
	}
}
