package actionheader_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/asteroid-computing/go-lambda-edge/actionheader"
)

// These are consumer-owned middleware and metadata, not actionheader API types.
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

func ExampleSelector() {
	selector, err := actionheader.NewSelector("Action")
	if err != nil {
		fmt.Println(err)
		return
	}
	dispatcher := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action, ok := r.Context().Value(actionKey{}).(string)
		if !ok {
			http.Error(w, "missing action metadata", http.StatusInternalServerError)
			return
		}
		// This example operation is public.
		// Protected operations must authorize this same selection before executing or publishing streaming output.
		switch action {
		case "service.ping":
			fmt.Fprintln(w, "pong")
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	})
	// The composed http.Handler serves native HTTP directly or goes to an edge adapter unchanged.
	handler := processActionHeaders(selector, dispatcher)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Action", "service.ping")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	fmt.Println(w.Code)
	fmt.Print(w.Body.String())
	// Output:
	// 200
	// pong
}
