package edge_test

import (
	"context"
	"fmt"
	"net/http"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	edge "github.com/asteroid-computing/go-lambda-edge"
)

func ExampleAdapter_Invoke() {
	adapter, err := edge.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		panic(err)
	}
	// Production main calls lambda.Start(adapter). Passing adapter.Invoke as a
	// reflected function would give []byte ordinary JSON/base64 semantics.
	handler := lambda.NewHandler(adapter)
	response, err := handler.Invoke(context.Background(), []byte(`{"httpMethod":"GET","path":"/","requestContext":{"apiId":"example"}}`))
	fmt.Println(len(response) > 0, err)
	// Output: true <nil>
}

func ExampleAdapter_HandleV2() {
	adapter, err := edge.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	if err != nil {
		panic(err)
	}
	// Production main may call lambda.Start(adapter.HandleV2). Its runtime then
	// owns envelope JSON. This direct typed call performs no envelope JSON work.
	response, err := adapter.HandleV2(context.Background(), events.APIGatewayV2HTTPRequest{
		Version: "2.0", RawPath: "/",
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			APIID: "example", HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "POST"},
		},
	})
	fmt.Println(response.StatusCode, err)
	// Output: 202 <nil>
}
