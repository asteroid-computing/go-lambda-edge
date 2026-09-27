package edge_test

import (
	"encoding/base64"
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	edge "github.com/asteroid-computing/go-lambda-edge"
)

func BenchmarkAdapterPublic(b *testing.B) {
	for _, fixture := range []struct {
		name   string
		size   int
		binary bool
	}{{name: "small", size: 128}, {name: "binary64KiB", size: 64 * 1024, binary: true}, {name: "text5MiB", size: 5 * 1024 * 1024}} {
		b.Run(fixture.name, func(b *testing.B) {
			body := strings.Repeat("x", fixture.size)
			mediaType := "text/plain"
			if fixture.binary {
				body = base64.StdEncoding.EncodeToString([]byte(body))
				mediaType = "application/octet-stream"
			}
			a, err := edge.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", mediaType)
				_, _ = io.Copy(w, r.Body)
			}))
			if err != nil {
				b.Fatal(err)
			}
			v1 := events.APIGatewayProxyRequest{HTTPMethod: "POST", Path: "/", Body: body, IsBase64Encoded: fixture.binary, RequestContext: events.APIGatewayProxyRequestContext{APIID: "api"}}
			v2 := events.APIGatewayV2HTTPRequest{Version: "2.0", RawPath: "/", Body: body, IsBase64Encoded: fixture.binary, RequestContext: events.APIGatewayV2HTTPRequestContext{APIID: "api", HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "POST"}}}
			raw1, err := json.Marshal(v1)
			if err != nil {
				b.Fatal(err)
			}
			raw2, err := json.Marshal(v2)
			if err != nil {
				b.Fatal(err)
			}
			for _, path := range []struct {
				name string
				run  func() error
			}{
				{"rawV1", func() error { _, err := a.Invoke(b.Context(), raw1); return err }},
				{"rawV2", func() error { _, err := a.Invoke(b.Context(), raw2); return err }},
				{"typedV1", func() error { _, err := a.HandleV1(b.Context(), v1); return err }},
				{"typedV2", func() error { _, err := a.HandleV2(b.Context(), v2); return err }},
			} {
				b.Run(path.name, func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(fixture.size))
					for b.Loop() {
						if err := path.run(); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
