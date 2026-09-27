package edge

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBufferedRequestIntegration(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /content", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "content.txt", time.Unix(1000, 0), strings.NewReader("0123456789"))
	})
	mux.HandleFunc("GET /compressed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "gzip")
		z := gzip.NewWriter(w)
		io.WriteString(z, "compressed response")
		z.Close()
	})
	mux.HandleFunc("POST /echo/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Custom", r.Header.Get("Custom"))
		w.Header().Set("Selected-Id", r.PathValue("id"))
		io.Copy(w, r.Body)
	})
	for _, format := range []string{"v1", "v2"} {
		for _, path := range []string{"/content", "/compressed", "/echo/example"} {
			t.Run(format+"_"+strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
				t.Parallel()
				var response *bufferedResponse
				var scope context.Context
				var body *trackedBody
				err := withInvocation(t.Context(), func(ctx context.Context, inv *invocation) error {
					scope = ctx
					v1, v2 := v1RequestEvent(), v2RequestEvent()
					v1.Path, v2.RawPath = path, path
					v1.Headers = map[string]string{"Custom": "request-specific", "Range": "bytes=2-5"}
					v2.Headers = v1.Headers
					if path == "/echo/example" {
						v1.HTTPMethod = http.MethodPost
						v2.RequestContext.HTTP.Method = http.MethodPost
						v1.Body, v2.Body = "echo body", "echo body"
					}
					var r *http.Request
					var err error
					if format == "v1" {
						r, err = requestV1(ctx, v1)
					} else {
						r, err = requestV2(ctx, v2)
					}
					if err != nil {
						return err
					}
					body = &trackedBody{Reader: r.Body}
					r.Body = body
					inv.ownRequest(r)
					response, err = serveBuffered(mux, r, defaultResponseHeaderBudget)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				if !body.closed || scope.Err() != context.Canceled {
					t.Errorf("buffered invocation did not clean up: closed=%t context=%v", body.closed, scope.Err())
				}
				var wireBody string
				var binary bool
				if format == "v1" {
					out, err := response.v1()
					if err != nil {
						t.Fatal(err)
					}
					wireBody, binary = out.Body, out.IsBase64Encoded
				} else {
					out, err := response.v2()
					if err != nil {
						t.Fatal(err)
					}
					wireBody, binary = out.Body, out.IsBase64Encoded
				}
				decoded := []byte(wireBody)
				if binary {
					decoded, err = base64.StdEncoding.DecodeString(wireBody)
					if err != nil {
						t.Fatal(err)
					}
				}
				switch path {
				case "/content":
					if response.status != 206 || string(decoded) != "2345" || response.headers.fields.Get("Content-Range") != "bytes 2-5/10" {
						t.Errorf("range response = %d %q, headers %v", response.status, decoded, response.headers.fields)
					}
				case "/compressed":
					if !binary || response.headers.fields.Get("Content-Encoding") != "gzip" {
						t.Fatal("compressed response lost binary/encoding metadata")
					}
					z, err := gzip.NewReader(bytes.NewReader(decoded))
					if err != nil {
						t.Fatal(err)
					}
					plain, err := io.ReadAll(z)
					z.Close()
					if err != nil || string(plain) != "compressed response" {
						t.Errorf("decompressed response = %q, %v", plain, err)
					}
				case "/echo/example":
					if string(decoded) != "echo body" || response.headers.fields.Get("Custom") != "request-specific" || response.headers.fields.Get("Selected-Id") != "example" {
						t.Errorf("mux/echo response = %q, headers %v", decoded, response.headers.fields)
					}
				}
			})
		}
	}
}

func TestBufferedInvocationFailureCleanup(t *testing.T) {
	for _, scenario := range []string{"write_failure", "panic", "parent_cancel", "cleanup_failure"} {
		t.Run(scenario, func(t *testing.T) {
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			body := &trackedBody{Reader: http.NoBody}
			failedBody := &failingCloseBody{Reader: http.NoBody}
			var scope context.Context
			var recovered any
			var err error
			func() {
				defer func() { recovered = recover() }()
				err = withInvocation(parent, func(ctx context.Context, inv *invocation) error {
					scope = ctx
					r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
					r.Body = body
					if scenario == "cleanup_failure" {
						r.Body = failedBody
					}
					inv.ownRequest(r)
					_, err := serveBuffered(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch scenario {
						case "write_failure":
							w.Header().Set("Content-Length", "1")
							io.WriteString(w, "overflow")
						case "panic":
							panic("handler panic")
						case "parent_cancel":
							cancel()
						default:
							io.WriteString(w, "success before failed cleanup")
						}
					}), r, defaultResponseHeaderBudget)
					return err
				})
			}()
			if !body.closed && !failedBody.closed || scope.Err() != context.Canceled {
				t.Errorf("failure did not clean up body/context: %v, %v", body.closed || failedBody.closed, scope.Err())
			}
			switch scenario {
			case "panic":
				if recovered != "handler panic" {
					t.Errorf("recovered %v; want original handler panic", recovered)
				}
			case "write_failure":
				if !errors.Is(err, http.ErrContentLength) {
					t.Errorf("ignored Write failure returned %v", err)
				}
			case "parent_cancel":
				if !errors.Is(err, context.Canceled) {
					t.Errorf("canceled invocation returned %v", err)
				}
			case "cleanup_failure":
				if err == nil || strings.Contains(err.Error(), "credential") {
					t.Errorf("cleanup failure returned %v; want sanitized error", err)
				}
			}
		})
	}
}

func TestBufferedConfiguredBudgetAndSuppression(t *testing.T) {
	for _, budget := range []int{1, defaultResponseHeaderBudget} {
		t.Run(strconv.Itoa(budget), func(t *testing.T) {
			a, err := New(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), WithResponseHeaderBudget(budget))
			if err != nil {
				t.Fatal(err)
			}
			result, err := serveBuffered(a.handler, httptest.NewRequest(http.MethodGet, "/", nil), a.config.responseHeaderBudget)
			if budget == 1 {
				if result != nil || !errors.Is(err, errResponseHeaderBudget) {
					t.Errorf("tiny budget = %v, %v; want generated-length failure", result, err)
				}
			} else if err != nil || result == nil {
				t.Errorf("default budget = %v, %v; want success", result, err)
			}
		})
	}
	for _, field := range []string{"Content-Type", "Content-Length"} {
		w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
		w.Header().Set("Connection", field)
		w.Write([]byte("body"))
		result, err := w.finish()
		if err != nil {
			t.Fatal(err)
		}
		if _, present := result.headers.fields[field]; present {
			t.Errorf("writer resurrected nominated field %s", field)
		}
	}
}
