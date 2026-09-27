// Command native serves the buffered orders application on loopback.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/asteroid-computing/go-lambda-edge/examples/dispatcher/internal/orders"
)

func main() {
	h, err := orders.FromEnvironment(false)
	if err != nil {
		slog.Error("configure orders", "error", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: "127.0.0.1:8080", Handler: h, ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve orders", "error", err)
		os.Exit(1)
	}
}
