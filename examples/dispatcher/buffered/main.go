// Command buffered registers the orders application for REST or HTTP API events.
package main

import (
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/examples/dispatcher/internal/orders"
)

func main() {
	h, err := orders.FromEnvironment(false)
	if err != nil {
		slog.Error("configure orders", "error", err)
		os.Exit(1)
	}
	adapter, err := edge.New(h)
	if err != nil {
		slog.Error("configure buffered adapter", "error", err)
		os.Exit(1)
	}
	// Register the object to retain JSON v2 envelope ownership. For a fixed
	// typed input, register adapter.HandleV1 or adapter.HandleV2 instead.
	lambda.Start(adapter)
}
