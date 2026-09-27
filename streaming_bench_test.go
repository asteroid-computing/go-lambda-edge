package edge_test

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/asteroid-computing/go-lambda-edge"
)

func BenchmarkStreamingPublic(b *testing.B) {
	for _, size := range []struct {
		name  string
		bytes int
	}{
		{name: "128B", bytes: 128},
		{name: "64KiB", bytes: 64 * 1024},
		{name: "5MiB", bytes: 5 * 1024 * 1024},
	} {
		b.Run(size.name, func(b *testing.B) {
			// Reuse one bounded application chunk; never allocate a whole body.
			chunk := bytes.Repeat([]byte("x"), min(size.bytes, 4096))
			adapter, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				controller := http.NewResponseController(w)
				for remaining := size.bytes; remaining > 0; {
					if r.Context().Err() != nil {
						return
					}
					n := min(remaining, len(chunk))
					if _, err := w.Write(chunk[:n]); err != nil {
						return
					}
					if err := controller.Flush(); err != nil {
						return
					}
					remaining -= n
				}
			}))
			if err != nil {
				b.Fatal(err)
			}
			event := events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: "/", RequestContext: events.APIGatewayProxyRequestContext{APIID: "synthetic"}}
			wire, err := json.Marshal(event)
			if err != nil {
				b.Fatal(err)
			}
			for _, format := range []string{"rawREST", "typedV1"} {
				b.Run(format, func(b *testing.B) {
					buffer := make([]byte, 8192)
					b.ReportAllocs()
					b.SetBytes(int64(size.bytes))
					for b.Loop() {
						var stream io.ReadCloser
						var err error
						if format == "rawREST" {
							stream, err = adapter.Handle(b.Context(), wire)
						} else {
							stream, err = adapter.HandleV1(b.Context(), event)
						}
						if err != nil {
							b.Fatal(err)
						}
						// Explicit incremental reads avoid io.ReadAll and io.Copy's
						// optional ReaderFrom/WriterTo shortcuts or buffer pooling.
						total := 0
						for {
							n, readErr := stream.Read(buffer)
							total += n
							if readErr == io.EOF {
								break
							}
							if readErr != nil {
								stream.Close()
								b.Fatal(readErr)
							}
						}
						if err := stream.Close(); err != nil {
							b.Fatal(err)
						}
						if total <= size.bytes {
							b.Fatal("stream body or metadata truncated")
						}
					}
				})
			}
		})
	}
}
