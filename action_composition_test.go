package edge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/actionheader"
)

// These are consumer-owned middleware and metadata, not edge or actionheader API types.
type actionKey struct{}

func processActionHeaders(selector actionheader.Selector, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action, err := selector.Parse(r.Header)
		if err != nil {
			http.Error(w, "invalid action selection", http.StatusBadRequest)
			return
		}
		ctx := context.WithValue(r.Context(), actionKey{}, action)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestActionSelectorMiddlewareComposition(t *testing.T) {
	selector, err := actionheader.NewSelector("Action")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		values []string
		status int
		steps  []string
	}{
		{name: "allowed", values: []string{"read"}, status: http.StatusOK, steps: []string{"extra headers", "authorize read", "execute read"}},
		{name: "denied", values: []string{"delete"}, status: http.StatusForbidden, steps: []string{"extra headers", "authorize delete"}},
		{name: "ambiguous", values: []string{"read", "delete"}, status: http.StatusBadRequest, steps: []string{"extra headers"}},
		{name: "missing", status: http.StatusBadRequest, steps: []string{"extra headers"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var steps []string
			type traceKey struct{}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
			r.Header["Action"] = tt.values
			r.Header.Set("Custom-Trace", "trace-123")
			dispatcher := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				action, ok := r.Context().Value(actionKey{}).(string)
				if !ok {
					t.Error("dispatcher did not receive selected action")
					return
				}
				if r.Context().Value(traceKey{}) != "trace-123" {
					t.Error("action middleware lost earlier custom-header metadata")
				}
				steps = append(steps, "authorize "+action)
				if action != "read" {
					http.Error(w, "denied", http.StatusForbidden)
					return
				}
				// Later header changes cannot change the authorized selection.
				r.Header.Set("Action", "delete")
				cancel()
				if r.Context().Err() != context.Canceled {
					t.Error("middleware lost parent cancellation")
				}
				steps = append(steps, "execute "+action)
				w.WriteHeader(http.StatusOK)
			})
			next := processActionHeaders(selector, dispatcher)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				steps = append(steps, "extra headers")
				ctx := context.WithValue(r.Context(), traceKey{}, r.Header.Get("Custom-Trace"))
				next.ServeHTTP(w, r.WithContext(ctx))
			})
			if _, err := edge.New(handler); err != nil {
				t.Fatalf("edge.New rejected composed HTTP handler: %v", err)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tt.status || !slices.Equal(steps, tt.steps) {
				t.Errorf("composition = status %d, steps %v; want %d, %v", w.Code, steps, tt.status, tt.steps)
			}
			if r.Context().Value(actionKey{}) != nil || r.Context().Value(traceKey{}) != nil {
				t.Error("middleware modified the original request context")
			}
		})
	}
}
