// Command streaming registers the orders application for REST response streaming.
// Deployed interoperability remains subject to docs/streaming.md's qualification.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/examples/dispatcher/internal/orders"
)

func main() {
	h, err := orders.FromEnvironment(true)
	if err != nil {
		slog.Error("configure orders", "error", err)
		os.Exit(1)
	}
	adapter, err := edge.NewStreaming(h, edge.WithStreamErrorReporter(func(_ context.Context, err error) {
		// Edge supplies sanitized invocation diagnostics.
		// Never log a request's Authorization header, token, proof or raw provider error here.
		slog.Error("orders stream terminated", "error", err)
	}))
	if err != nil {
		slog.Error("configure streaming adapter", "error", err)
		os.Exit(1)
	}
	// Return the reader directly;
	// do not collect or JSON-encode the stream.
	// For already typed REST events, register adapter.HandleV1 instead.
	lambda.Start(adapter.Handle)
}
