package edge

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func v1RequestEvent() events.APIGatewayProxyRequest {
	return events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: "/", RequestContext: events.APIGatewayProxyRequestContext{APIID: "example"}}
}

func v2RequestEvent() events.APIGatewayV2HTTPRequest {
	return events.APIGatewayV2HTTPRequest{RawPath: "/", RequestContext: events.APIGatewayV2HTTPRequestContext{APIID: "example", HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "GET"}}}
}

func TestRequestPathsMatchHTTPParsing(t *testing.T) {
	for _, tt := range []struct {
		name   string
		v1Path string
		raw    string
		path   string
	}{
		{name: "delimiters", v1Path: "/a?b#c%", raw: "/a%3Fb%23c%25", path: "/a?b#c%"},
		{name: "encoded_slash", v1Path: "/a/b", raw: "/a%2fb", path: "/a/b"},
		{name: "double_escape", v1Path: "/a%2Fb", raw: "/a%252Fb", path: "/a%2Fb"},
		{name: "unicode", v1Path: "/café", raw: "/caf%C3%A9", path: "/café"},
		{name: "no_cleaning", v1Path: "/a//../b", raw: "/a//../b", path: "/a//../b"},
		{name: "network_path", v1Path: "//host/a", raw: "//host/a", path: "//host/a"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v1, v2 := v1RequestEvent(), v2RequestEvent()
			v1.Path, v2.RawPath = tt.v1Path, tt.raw
			v2.RawQueryString = "x=a&x=b&literal=%2C&space=+"
			v2.QueryStringParameters = map[string]string{"wrong": "ignored"}
			r1, err := requestV1(t.Context(), v1)
			if err != nil {
				t.Fatal(err)
			}
			r2, err := requestV2(t.Context(), v2)
			if err != nil {
				t.Fatal(err)
			}
			if r1.URL.Path != tt.v1Path || r1.URL.Fragment != "" || r1.URL.RawQuery != "" {
				t.Errorf("requestV1(%q) changed path into another URL component: %v", tt.v1Path, r1.URL)
			}
			if r2.RequestURI != tt.raw+"?"+v2.RawQueryString || r2.URL.Path != tt.path || r2.URL.RawQuery != v2.RawQueryString || r2.URL.Scheme != "" || r2.URL.Host != "" {
				t.Errorf("requestV2(%q) = URI %q, URL %v; want original target and decoded path %q", tt.raw, r2.RequestURI, r2.URL, tt.path)
			}
			for _, r := range []*http.Request{r1, r2} {
				parsed, err := http.ReadRequest(bufio.NewReader(strings.NewReader("GET " + r.RequestURI + " HTTP/1.1\r\nHost: example.com\r\n\r\n")))
				if err != nil {
					t.Fatalf("ReadRequest(%q) = %v", r.RequestURI, err)
				}
				if parsed.URL.Path != r.URL.Path || parsed.URL.RawQuery != r.URL.RawQuery || parsed.URL.Host != r.URL.Host {
					t.Errorf("HTTP parser URL %v differs from adapter URL %v", parsed.URL, r.URL)
				}
				if err := parsed.Body.Close(); err != nil {
					t.Error(err)
				}
			}
		})
	}
}

func TestRequestMergingAndOwnership(t *testing.T) {
	event := v1RequestEvent()
	event.Headers = map[string]string{"host": "example.com:443", "X-Mixed": "echo", "x-extra": "keep", "x-case": "from-single", "Content-Length": "999", "Transfer-Encoding": "chunked", "Trailer": "Digest"}
	event.MultiValueHeaders = map[string][]string{"x-mixed": {"echo", "repeat", "repeat"}, "X-Case": {"upper"}, "x-case": {"lower"}, "Content-Length": {"3"}}
	event.QueryStringParameters = map[string]string{"a": "echo", "b": "extra", "Case": "upper", "case": "lower"}
	event.MultiValueQueryStringParameters = map[string][]string{"a": {"echo", "repeat", "repeat"}, "b": {"multi"}}
	event.Body = "hello"
	r, err := requestV1(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	if r.Host != "example.com:443" || r.Header.Get("Host") != "" || r.Header.Get("X-Extra") != "keep" {
		t.Errorf("host/header promotion lost values: host=%q, headers=%v", r.Host, r.Header)
	}
	if !slices.Equal(r.Header.Values("X-Mixed"), []string{"echo", "repeat", "repeat"}) || !slices.Equal(r.Header.Values("X-Case"), []string{"upper", "lower", "from-single"}) {
		t.Errorf("header merge = %v, want multivalue order and distinct single value", r.Header)
	}
	query := r.URL.Query()
	if !slices.Equal(query["a"], []string{"echo", "repeat", "repeat"}) || !slices.Equal(query["b"], []string{"multi", "extra"}) || query.Get("Case") != "upper" || query.Get("case") != "lower" {
		t.Errorf("query merge = %v, want repeated values and case-sensitive keys", query)
	}
	if r.ContentLength != 5 || r.Header.Get("Content-Length") != "5" || r.Header.Get("Transfer-Encoding") != "" || r.Header.Get("Trailer") != "" || r.TransferEncoding != nil || r.Trailer != nil {
		t.Errorf("body framing = length %d, headers %v; want decoded length without transfer framing", r.ContentLength, r.Header)
	}
	r.Header["X-Mixed"][0] = "changed"
	r.Header.Set("X-Extra", "changed")
	query["a"][0] = "changed"
	if event.MultiValueHeaders["x-mixed"][0] != "echo" || event.Headers["x-extra"] != "keep" || event.MultiValueQueryStringParameters["a"][0] != "echo" || event.Headers["Content-Length"] != "999" {
		t.Error("request mutation changed caller-owned event")
	}
}

func TestRequestV2CookiesBodyAndMetadata(t *testing.T) {
	event := v2RequestEvent()
	event.Headers = map[string]string{"cookie": "ignored=1", "x-list": "a,b", "x-forwarded-for": "untrusted", "x-forwarded-host": "untrusted"}
	event.Cookies = []string{"a=1", "b=2"}
	event.Body, event.IsBase64Encoded = "AP8=", true
	event.RequestContext.DomainName = "example.com"
	event.RequestContext.HTTP.SourceIP = "2001:db8::1"
	event.RequestContext.HTTP.Protocol = "HTTP/2.0"
	r, err := requestV2(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, []byte{0, 255}) || r.ContentLength != 2 {
		t.Errorf("body = %x, length %d; want 00ff, length 2", body, r.ContentLength)
	}
	if r.Header.Get("Cookie") != "a=1; b=2" || len(r.Cookies()) != 2 || !slices.Equal(r.Header.Values("X-List"), []string{"a,b"}) {
		t.Errorf("cookies/flattened header = %v, want cookies joined and comma value intact", r.Header)
	}
	if r.Host != "example.com" || r.RemoteAddr != "2001:db8::1" || r.Proto != "HTTP/2.0" || r.ProtoMajor != 2 || r.ProtoMinor != 0 || r.TLS != nil || r.Pattern != "" || r.GetBody != nil {
		t.Error("gateway metadata did not produce the specified server request fields")
	}
	r.Header.Set("Cookie", "changed")
	if event.Cookies[0] != "a=1" || event.Headers["cookie"] != "ignored=1" {
		t.Error("cookie conversion aliased caller data")
	}
	event.Cookies = nil
	r, err = requestV2(t.Context(), event)
	if err != nil || r.Header.Get("Cookie") != "ignored=1" {
		t.Fatalf("empty cookies fallback = (%v, %v), want existing Cookie header", r, err)
	}
}

func TestRequestRejectsInvalidTransportValues(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*events.APIGatewayV2HTTPRequest)
	}{
		{name: "missing_api", change: func(e *events.APIGatewayV2HTTPRequest) { e.RequestContext.APIID = "" }},
		{name: "relative_path", change: func(e *events.APIGatewayV2HTTPRequest) { e.RawPath = "relative" }},
		{name: "bad_escape", change: func(e *events.APIGatewayV2HTTPRequest) { e.RawPath = "/%xy" }},
		{name: "query_in_path", change: func(e *events.APIGatewayV2HTTPRequest) { e.RawPath = "/a?b" }},
		{name: "fragment_in_path", change: func(e *events.APIGatewayV2HTTPRequest) { e.RawPath = "/a#b" }},
		{name: "space_in_raw_path", change: func(e *events.APIGatewayV2HTTPRequest) { e.RawPath = "/a b" }},
		{name: "bad_method", change: func(e *events.APIGatewayV2HTTPRequest) { e.RequestContext.HTTP.Method = "GET secret" }},
		{name: "bad_protocol", change: func(e *events.APIGatewayV2HTTPRequest) { e.RequestContext.HTTP.Protocol = "secret" }},
		{name: "bad_base64", change: func(e *events.APIGatewayV2HTTPRequest) { e.Body, e.IsBase64Encoded = "aGVsbG8=secret", true }},
		{name: "bad_header_name", change: func(e *events.APIGatewayV2HTTPRequest) { e.Headers = map[string]string{"secret:name": "value"} }},
		{name: "bad_header_value", change: func(e *events.APIGatewayV2HTTPRequest) { e.Headers = map[string]string{"x": "secret\r\nx: bad"} }},
		{name: "bad_cookie", change: func(e *events.APIGatewayV2HTTPRequest) { e.Cookies = []string{"secret\x00"} }},
		{name: "multiple_hosts", change: func(e *events.APIGatewayV2HTTPRequest) { e.Headers = map[string]string{"Host": "a", "host": "b"} }},
		{name: "empty_host", change: func(e *events.APIGatewayV2HTTPRequest) { e.Headers = map[string]string{"host": ""} }},
		{name: "host_path", change: func(e *events.APIGatewayV2HTTPRequest) { e.RequestContext.DomainName = "example.com/secret" }},
		{name: "host_credentials", change: func(e *events.APIGatewayV2HTTPRequest) { e.RequestContext.DomainName = "secret@example.com" }},
		{name: "unbracketed_ipv6", change: func(e *events.APIGatewayV2HTTPRequest) { e.RequestContext.DomainName = "2001:db8::1" }},
		{name: "bad_ipv6", change: func(e *events.APIGatewayV2HTTPRequest) { e.RequestContext.DomainName = "[secret]" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := v2RequestEvent()
			tt.change(&event)
			r, err := requestV2(t.Context(), event)
			if err == nil || r != nil {
				t.Fatalf("requestV2(invalid transport) = (%v, %v), want (nil, error)", r, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("conversion error exposed input: %v", err)
			}
		})
	}
}

func TestRequestDefaultsAndContext(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	event := v1RequestEvent()
	event.HTTPMethod = "custom"
	r, err := requestV1(parent, event)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != "custom" || r.Host != "" || r.RemoteAddr != "" || r.Proto != "HTTP/1.1" || r.ProtoMajor != 1 || r.ProtoMinor != 1 || r.Body != http.NoBody || r.ContentLength != 0 {
		t.Error("minimal typed event did not get the documented defaults")
	}
	cancel()
	if !errors.Is(r.Context().Err(), context.Canceled) {
		t.Errorf("request context error = %v, want canceled", r.Context().Err())
	}
	if r, err := requestV1(nil, event); err == nil || r != nil {
		t.Errorf("nil context = (%v, %v), want (nil, error)", r, err)
	}
	event.Path = "relative"
	if r, err := requestV1(t.Context(), event); err == nil || r != nil {
		t.Errorf("relative v1 path = (%v, %v), want (nil, error)", r, err)
	}
}

func TestRequestMatchesLocalHTTPServer(t *testing.T) {
	type observed struct {
		method, path, escapedPath, query, target, host, contentType, cookie, body string
		length                                                                    int64
	}
	observations := make(chan observed, 2)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		observations <- observed{method: r.Method, path: r.URL.Path, escapedPath: r.URL.EscapedPath(), query: r.URL.RawQuery, target: r.RequestURI, host: r.Host, contentType: r.Header.Get("Content-Type"), cookie: r.Header.Get("Cookie"), body: string(data), length: r.ContentLength}
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	clientRequest, err := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/a%2Fb?x=1&x=2", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	clientRequest.Header.Set("Content-Type", "text/plain")
	clientRequest.Header.Set("Cookie", "a=1; b=2")
	response, err := server.Client().Do(clientRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("local HTTP server returned %d", response.StatusCode)
	}
	want := <-observations
	event := v2RequestEvent()
	event.RawPath, event.RawQueryString = "/a%2Fb", "x=1&x=2"
	event.Body = "hello"
	event.RequestContext.HTTP.Method = "POST"
	event.Headers = map[string]string{"host": clientRequest.URL.Host, "content-type": "text/plain"}
	event.Cookies = []string{"a=1", "b=2"}
	r, err := requestV2(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(handler)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.serveHTTP(httptest.NewRecorder(), r); err != nil {
		t.Fatal(err)
	}
	if got := <-observations; got != want {
		t.Errorf("adapter request = %+v, local HTTP request = %+v", got, want)
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestServeHTTPCleansUp(t *testing.T) {
	for _, panicHandler := range []bool{false, true} {
		var data bytes.Buffer
		writer := multipart.NewWriter(&data)
		part, err := writer.CreateFormFile("file", "example.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, "force this file onto disk"); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/", &data).WithContext(t.Context())
		r.Header.Set("Content-Type", writer.FormDataContentType())
		body := &trackedBody{Reader: r.Body}
		r.Body = body
		var handlerContext context.Context
		var temporaryPath string
		adapter, err := New(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			handlerContext = r.Context()
			if err := r.ParseMultipartForm(0); err != nil {
				t.Fatal(err)
			}
			file, err := r.MultipartForm.File["file"][0].Open()
			if err != nil {
				t.Fatal(err)
			}
			diskFile, ok := file.(*os.File)
			if !ok {
				t.Fatal("multipart test did not create a file on disk")
			}
			temporaryPath = diskFile.Name()
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if panicHandler {
				panic("test panic")
			}
		}))
		if err != nil {
			t.Fatal(err)
		}
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			if err := adapter.serveHTTP(httptest.NewRecorder(), r); err != nil {
				t.Error(err)
			}
		}()
		if (recovered != nil) != panicHandler {
			t.Errorf("serveHTTP panic = %v, want panic %v", recovered, panicHandler)
		}
		if !body.closed || handlerContext.Err() != context.Canceled || t.Context().Err() != nil {
			t.Error("serveHTTP did not close body/cancel child, or canceled invocation parent")
		}
		if _, err := os.Stat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("multipart file remains after serveHTTP: %v", err)
		}
	}
}

func FuzzRequestV2Target(f *testing.F) {
	for _, path := range []string{"/", "/a%2Fb", "/a%252Fb", "/a?b", "//host/a", "/%ff", "/café", "/a//../b"} {
		f.Add(path, "x=1&x=2")
	}
	f.Add("/", "x=%zz")
	f.Add("/", "x=secret\r\ny=bad")
	f.Fuzz(func(t *testing.T, path, query string) {
		event := v2RequestEvent()
		event.RawPath = path
		event.RawQueryString = query
		r, err := requestV2(t.Context(), event)
		if err != nil {
			if r != nil {
				t.Fatal("invalid path returned a partial request")
			}
			return
		}
		target := path
		if query != "" {
			target += "?" + query
		}
		if r.RequestURI != target || r.URL.EscapedPath() != path || r.URL.RawQuery != query || r.URL.Host != "" || r.URL.Fragment != "" {
			t.Fatalf("accepted path %q changed target: %v", path, r.URL)
		}
		parsed, err := http.ReadRequest(bufio.NewReader(strings.NewReader("GET " + target + " HTTP/1.1\r\nHost: example.com\r\n\r\n")))
		if err != nil {
			t.Fatalf("accepted path %q cannot be parsed by net/http: %v", path, err)
		}
		if parsed.URL.Path != r.URL.Path || parsed.URL.RawQuery != query {
			t.Fatalf("path %q decoded differently by HTTP parser: %q vs %q", path, parsed.URL.Path, r.URL.Path)
		}
		if err := parsed.Body.Close(); err != nil {
			t.Error(err)
		}
	})
}

func FuzzRequestV1Query(f *testing.F) {
	f.Add("x", "a,b", "a b")
	f.Add("", "%", "&=")
	f.Fuzz(func(t *testing.T, key, single, multi string) {
		event := v1RequestEvent()
		event.QueryStringParameters = map[string]string{key: single}
		event.MultiValueQueryStringParameters = map[string][]string{key: {multi, multi}}
		r, err := requestV1(t.Context(), event)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{multi, multi}
		if single != multi {
			want = append(want, single)
		}
		if got := r.URL.Query()[key]; !slices.Equal(got, want) {
			t.Fatalf("query values = %q, want %q", got, want)
		}
	})
}
