package edge_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-lambda-go/events"

	"github.com/asteroid-computing/go-lambda-edge"
)

func ExampleStreamingAdapter_Handle() {
	adapter, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Protected applications authenticate and authorize before this point.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		controller := http.NewResponseController(w)
		for _, message := range []string{"ready", "done"} {
			if r.Context().Err() != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", message); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}))
	if err != nil {
		panic(err)
	}
	// A Lambda main registers lambda.Start(adapter.Handle).
	// This direct call demonstrates reader ownership locally;
	// it makes no AWS calls.
	stream, err := adapter.Handle(context.Background(), jsontext.Value(`{"httpMethod":"GET","path":"/","requestContext":{"apiId":"example"}}`))
	if err != nil {
		panic(err)
	}
	fmt.Print(string(readExampleStream(stream)))
	// Output:
	// data: ready
	//
	// data: done
}

func ExampleStreamingAdapter_HandleV1() {
	adapter, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, value := range []string{"ready", "done"} {
			if r.Context().Err() != nil {
				return
			}
			if err := json.MarshalWrite(w, struct {
				State string `json:"state"`
			}{value}); err != nil {
				return
			}
			if _, err := io.WriteString(w, "\n"); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
		}
	}))
	if err != nil {
		panic(err)
	}
	// A Lambda main may register lambda.Start(adapter.HandleV1).
	// Input envelope decoding then belongs to the SDK;
	// output metadata still uses JSON v2 in edge.
	stream, err := adapter.HandleV1(context.Background(), exampleRESTEvent())
	if err != nil {
		panic(err)
	}
	fmt.Print(string(readExampleStream(stream)))
	// Output:
	// {"state":"ready"}
	// {"state":"done"}
}

func ExampleNewStreaming_binary() {
	adapter, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0, 255, 1})
	}))
	if err != nil {
		panic(err)
	}
	stream, err := adapter.HandleV1(context.Background(), exampleRESTEvent())
	if err != nil {
		panic(err)
	}
	fmt.Printf("%v\n", readExampleStream(stream))
	// Output: [0 255 1]
}

func ExampleNewStreaming_gzip() {
	adapter, err := edge.NewStreaming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "gzip")
		// The example always emits gzip;
		// a real route negotiates Accept-Encoding.
		compressed := gzip.NewWriter(w)
		defer compressed.Close()
		for _, chunk := range []string{"first\n", "last\n"} {
			if r.Context().Err() != nil {
				return
			}
			if _, err := io.WriteString(compressed, chunk); err != nil {
				return
			}
			// Flush the encoder first, then the underlying HTTP response.
			if err := compressed.Flush(); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
		}
	}))
	if err != nil {
		panic(err)
	}
	stream, err := adapter.HandleV1(context.Background(), exampleRESTEvent())
	if err != nil {
		panic(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(readExampleStream(stream)))
	if err != nil {
		panic(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		panic(err)
	}
	fmt.Print(string(data))
	// Output:
	// first
	// last
}

func exampleRESTEvent() events.APIGatewayProxyRequest {
	return events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: "/", RequestContext: events.APIGatewayProxyRequestContext{APIID: "example"}}
}

// This collector is only for the small local examples.
// Lambda's runtime consumes the reader incrementally;
// do not collect the stream in a production wrapper.
func readExampleStream(stream io.ReadCloser) []byte {
	defer stream.Close()
	wire, err := io.ReadAll(stream)
	if err != nil {
		panic(err)
	}
	_, body, ok := bytes.Cut(wire, make([]byte, 8))
	if !ok {
		panic("missing stream delimiter")
	}
	return body
}
