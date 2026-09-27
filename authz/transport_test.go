package authz_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/authz"
)

func TestAuthorizationCancellationAcrossTransports(t *testing.T) {
	for _, format := range []string{"native", "typed_v1", "typed_v2", "raw_rest", "raw_http_v1", "raw_http_v2", "stream_raw", "stream_typed"} {
		t.Run(format, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			calls := 0
			policy := checked(t, func(context.Context, authz.Request) (bool, error) {
				calls++
				cancel(errors.New("private cancellation cause"))
				return true, errors.New("private grant outage")
			})
			facts := request(t)
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := policy.Authorize(r.Context(), facts); err != nil {
					if err != context.Canceled {
						t.Errorf("policy error=%v; want context.Canceled", err)
					}
					writePolicyError(w, 503, "unavailable")
					return
				}
				t.Error("canceled authorization executed the action")
			})
			if format == "native" {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequestWithContext(ctx, "GET", "/", nil))
				if w.Code != 503 || calls != 1 {
					t.Fatalf("native response=%d checks=%d", w.Code, calls)
				}
				return
			}
			adapter, err := edge.New(h)
			if err != nil {
				t.Fatal(err)
			}
			v1 := events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: "/", RequestContext: events.APIGatewayProxyRequestContext{APIID: "example"}}
			v2 := events.APIGatewayV2HTTPRequest{Version: "2.0", RawPath: "/", RequestContext: events.APIGatewayV2HTTPRequestContext{APIID: "example", HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "GET"}}}
			switch format {
			case "stream_raw", "stream_typed":
				streaming, configErr := edge.NewStreaming(h)
				if configErr != nil {
					t.Fatal(configErr)
				}
				if format == "stream_raw" {
					wire, marshalErr := json.Marshal(v1)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					stream, invokeErr := streaming.Handle(ctx, wire)
					err = invokeErr
					if stream != nil {
						_ = stream.Close()
						t.Error("canceled authorization returned stream")
					}
				} else {
					stream, invokeErr := streaming.HandleV1(ctx, v1)
					err = invokeErr
					if stream != nil {
						_ = stream.Close()
						t.Error("canceled authorization returned stream")
					}
				}
			case "typed_v1":
				response, invokeErr := adapter.HandleV1(ctx, v1)
				err = invokeErr
				if response.StatusCode != 0 {
					t.Error("canceled invocation returned a usable response")
				}
			case "typed_v2":
				response, invokeErr := adapter.HandleV2(ctx, v2)
				err = invokeErr
				if response.StatusCode != 0 {
					t.Error("canceled invocation returned a usable response")
				}
			default:
				var event any = v1
				if format == "raw_http_v2" {
					event = v2
				}
				wire, marshalErr := json.Marshal(event)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if format == "raw_http_v1" {
					wire = append([]byte(`{"version":"1.0",`), wire[1:]...)
				}
				response, invokeErr := adapter.Invoke(ctx, wire)
				err = invokeErr
				if len(response) != 0 {
					t.Error("canceled invocation returned a usable response")
				}
			}
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Errorf("invocation=%v checks=%d; want canceled after one check", err, calls)
			}
		})
	}
}
