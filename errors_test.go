package edge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Exercise real transport fault sites while the public invocation methods await identity producers.
// Assertions use the exported contract, not private causes.
func TestInvocationErrorCategories(t *testing.T) {
	for _, tt := range []struct {
		name      string
		fail      func(*testing.T) error
		category  error
		operation Operation
		standard  error
	}{
		{name: "nil_context", category: ErrInvalidInvocation, operation: "validate", fail: func(t *testing.T) error {
			return withInvocation(nil, func(context.Context, *invocation) error {
				t.Error("invalid invocation ran application work")
				return nil
			})
		}},
		{name: "malformed_json", category: ErrInvalidEvent, operation: "decode", fail: func(t *testing.T) error {
			_, err := decodeEvent([]byte(`{"credential-secret":`))
			return err
		}},
		{name: "unsupported_version", category: ErrUnsupportedEvent, operation: "decode", fail: func(t *testing.T) error {
			_, err := decodeEvent([]byte(`{"version":"credential-secret"}`))
			return err
		}},
		{name: "invalid_envelope", category: ErrInvalidEvent, operation: "validate", fail: func(t *testing.T) error {
			_, err := decodeEvent([]byte(`{}`))
			return err
		}},
		{name: "typed_request", category: ErrInvalidEvent, operation: "request", fail: func(t *testing.T) error {
			event := v2RequestEvent()
			event.RawPath = "/credential-secret%bad%"
			_, err := requestV2(t.Context(), event)
			return err
		}},
		{name: "response_header", category: ErrResponse, operation: "response", fail: func(t *testing.T) error {
			_, err := snapshotResponseHeaders(http.Header{"Credential-secret": {"private\nvalue"}}, defaultResponseHeaderBudget)
			return err
		}},
		{name: "content_length", category: ErrResponse, operation: "response", standard: http.ErrContentLength, fail: func(t *testing.T) error {
			w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
			w.Header().Set("Content-Length", "0")
			_, err := w.Write([]byte("credential-secret"))
			return err
		}},
		{name: "short_content_length", category: ErrResponse, operation: "response", standard: http.ErrContentLength, fail: func(t *testing.T) error {
			w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
			w.Header().Set("Content-Length", "1")
			_, err := w.finish()
			return err
		}},
		{name: "trailers", category: ErrResponse, operation: "response", standard: http.ErrNotSupported, fail: func(t *testing.T) error {
			_, err := snapshotResponseHeaders(http.Header{"Trailer": {"Credential-secret"}}, defaultResponseHeaderBudget)
			return err
		}},
		{name: "response_codec", category: ErrResponse, operation: "encode", fail: func(t *testing.T) error {
			_, err := marshalResponse(map[string]string{"credential-secret": "private\xff"})
			return err
		}},
		{name: "cleanup", category: ErrCleanup, operation: "cleanup", fail: func(t *testing.T) error {
			return withInvocation(t.Context(), func(ctx context.Context, inv *invocation) error {
				inv.ownRequest(&http.Request{Body: &failingCloseBody{Reader: http.NoBody}})
				return nil
			})
		}},
		{name: "stream_unpublished", category: ErrStream, operation: "stream", fail: func(t *testing.T) error {
			s, err := startStream(t.Context(), func(context.Context, *invocation) error { return nil }, func(context.Context, *streamOutput) error { return nil }, nil)
			if s != nil {
				if closeErr := s.Close(); closeErr != nil {
					t.Error(closeErr)
				}
				t.Error("unpublished stream returned a reader")
			}
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fail(t)
			if err == nil {
				t.Fatal("transport fault returned nil error")
			}
			wrapped := fmt.Errorf("caller: %w", err)
			if !errors.Is(wrapped, tt.category) || tt.standard != nil && !errors.Is(wrapped, tt.standard) {
				t.Errorf("wrapped error = %v, want category %v and standard cause %v", wrapped, tt.category, tt.standard)
			}
			diagnostic, ok := errors.AsType[*InvocationError](wrapped)
			if !ok {
				t.Fatalf("error %v has no InvocationError", wrapped)
			}
			if diagnostic.Operation() != tt.operation {
				t.Errorf("Operation() = %q, want %q", diagnostic.Operation(), tt.operation)
			}
			if name, maximum, ok := diagnostic.Limit(); name != "" || maximum != 0 || ok {
				t.Errorf("Limit() = %q, %d, %v for a non-limit failure", name, maximum, ok)
			}
			// Inspect every exposed cause, including joined siblings.
			// An error's own sanitized text must not conceal a credential-bearing child.
			pending := []error{wrapped}
			for len(pending) != 0 {
				node := pending[len(pending)-1]
				pending = pending[:len(pending)-1]
				if strings.Contains(strings.ToLower(node.Error()), "credential-secret") || strings.Contains(node.Error(), "private") {
					t.Errorf("error tree exposes input in %T: %v", node, node)
				}
				switch node := node.(type) {
				case interface{ Unwrap() []error }:
					pending = append(pending, node.Unwrap()...)
				case interface{ Unwrap() error }:
					if inner := node.Unwrap(); inner != nil {
						pending = append(pending, inner)
					}
				}
			}
		})
	}
}

func TestInvocationErrorLimits(t *testing.T) {
	for _, tt := range []struct {
		name      string
		fail      func(*testing.T) error
		operation Operation
		limit     Resource
		maximum   int64
	}{
		{name: "original_headers", operation: "response", limit: "response_headers", maximum: 32, fail: func(t *testing.T) error {
			_, err := snapshotResponseHeaders(http.Header{"A": nil}, 32)
			return err
		}},
		{name: "generated_headers", operation: "response", limit: "response_headers", maximum: 33, fail: func(t *testing.T) error {
			h := mustSnapshot(t, http.Header{"A": nil}, 33)
			return h.automatic("Content-Length", "0")
		}},
		{name: "body_write", operation: "response", limit: "buffered_body", maximum: maxResponseBytes, fail: func(t *testing.T) error {
			w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
			_, err := w.Write(make([]byte, maxResponseBytes+1))
			return err
		}},
		{name: "binary_expansion", operation: "response", limit: "buffered_body", maximum: maxResponseBytes, fail: func(t *testing.T) error {
			_, _, err := encodeResponseBody(make([]byte, maxResponseBytes/4*3+1), nil)
			return err
		}},
		{name: "envelope", operation: "encode", limit: "buffered_envelope", maximum: maxResponseBytes, fail: func(t *testing.T) error {
			_, err := marshalResponse(map[string]string{"body": strings.Repeat("x", maxResponseBytes)})
			return err
		}},
		{name: "stream_metadata", operation: "encode", limit: "stream_metadata", maximum: maxStreamPrefixBytes, fail: func(t *testing.T) error {
			h := mustSnapshot(t, http.Header{"Large": {strings.Repeat("x", maxStreamPrefixBytes)}}, defaultResponseHeaderBudget)
			_, err := encodeStreamPrefix(http.StatusOK, h)
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fail(t)
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("limit failure = %v, want ErrLimitExceeded", err)
			}
			diagnostic, ok := errors.AsType[*InvocationError](err)
			if !ok {
				t.Fatalf("error %v has no InvocationError", err)
			}
			name, maximum, ok := diagnostic.Limit()
			if !ok || name != tt.limit || maximum != tt.maximum || diagnostic.Operation() != tt.operation {
				t.Errorf("limit diagnostic = %q, %d, %v, %q; want %q, %d, true, %q", name, maximum, ok, diagnostic.Operation(), tt.limit, tt.maximum, tt.operation)
			}
		})
	}
}

func TestInvocationErrorJoinedCleanup(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := withInvocation(parent, func(ctx context.Context, inv *invocation) error {
		inv.ownRequest(&http.Request{Body: &failingCloseBody{Reader: http.NoBody}})
		cancel()
		w := newBufferedWriter(http.MethodGet, defaultResponseHeaderBudget)
		w.Header().Set("Content-Length", "0")
		_, err := w.Write([]byte("x"))
		return err
	})
	wrapped := fmt.Errorf("caller: %w", err)
	if !errors.Is(wrapped, ErrResponse) || !errors.Is(wrapped, http.ErrContentLength) || !errors.Is(wrapped, ErrCleanup) || errors.Is(wrapped, context.Canceled) {
		t.Errorf("joined failure = %v, want response/length and cleanup without cancellation", wrapped)
	}
	if diagnostic, ok := errors.AsType[*InvocationError](wrapped); !ok || diagnostic.Operation() != "response" {
		t.Errorf("primary diagnostic = %v, %v; want response before cleanup", diagnostic, ok)
	}
}

func TestInvocationCancellationDoesNotExposeCause(t *testing.T) {
	parent, cancel := context.WithCancelCause(t.Context())
	private := errors.New("credential-secret")
	cancel(private)
	err := withInvocation(parent, func(context.Context, *invocation) error {
		t.Error("canceled invocation ran application work")
		return nil
	})
	if !errors.Is(err, context.Canceled) || errors.Is(err, private) || strings.Contains(err.Error(), "credential-secret") {
		t.Errorf("cancellation = %v, want only the standard cancellation cause", err)
	}
}

func TestHTTPFailureResponsesRemainSuccessfulInvocations(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		err := withInvocation(t.Context(), func(ctx context.Context, inv *invocation) error {
			r, err := requestV2(ctx, v2RequestEvent())
			if err != nil {
				return err
			}
			inv.ownRequest(r)
			response, err := serveBuffered(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "application failure", status)
			}), r, defaultResponseHeaderBudget)
			if err != nil {
				return err
			}
			typed, err := response.v2()
			if err != nil {
				return err
			}
			if typed.StatusCode != status {
				t.Errorf("HTTP status = %d, want %d", typed.StatusCode, status)
			}
			_, err = marshalResponse(typed)
			return err
		})
		if err != nil {
			t.Errorf("HTTP %d response produced invocation error: %v", status, err)
		}
	}
}
