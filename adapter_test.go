package edge_test

import (
	"net/http"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge"
)

func TestNewRejectsNilHandlers(t *testing.T) {
	var mux *http.ServeMux
	for _, tt := range []struct {
		name    string
		handler http.Handler
	}{
		{name: "nil_interface"},
		{name: "nil_function", handler: http.HandlerFunc(nil)},
		{name: "nil_pointer", handler: mux},
	} {
		t.Run(tt.name, func(t *testing.T) {
			adapter, err := edge.New(tt.handler)
			if err == nil || adapter != nil {
				t.Errorf("New(%T) = (%v, %v), want (nil, error)", tt.handler, adapter, err)
			}
		})
	}
}

func TestNewRejectsNilOption(t *testing.T) {
	adapter, err := edge.New(http.NewServeMux(), edge.WithGatewayIdentity(true), nil)
	if err == nil || adapter != nil {
		t.Errorf("New(mux, option, nil) = (%v, %v), want (nil, error)", adapter, err)
	}
}

func TestNewDoesNotInvokeHandler(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("New invoked the HTTP handler")
	})
	adapter, err := edge.New(handler, edge.WithGatewayIdentity(true))
	if err != nil || adapter == nil {
		t.Errorf("New(handler, option) = (%v, %v), want (non-nil, nil)", adapter, err)
	}
}
