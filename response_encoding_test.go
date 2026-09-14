package edge

import (
	"bytes"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func TestEncodeResponseBody(t *testing.T) {
	for _, tt := range []struct {
		name     string
		body     []byte
		header   http.Header
		wantBase bool
	}{
		{name: "empty"},
		{name: "empty_binary", header: http.Header{"Content-Type": {"application/octet-stream"}}},
		{name: "text", body: []byte("café"), header: http.Header{"Content-Type": {"text/plain; charset=utf-8"}}},
		{name: "json", body: []byte(`{"ok":true}`), header: http.Header{"Content-Type": {"Application/JSON"}}},
		{name: "json_suffix", body: []byte(`{}`), header: http.Header{"Content-Type": {"application/problem+json"}}},
		{name: "xml", body: []byte("<x/>"), header: http.Header{"Content-Type": {"application/xml"}}},
		{name: "xml_suffix", body: []byte("<svg/>"), header: http.Header{"Content-Type": {"image/svg+xml"}}},
		{name: "javascript", body: []byte("true"), header: http.Header{"Content-Type": {"application/javascript"}}},
		{name: "form", body: []byte("a=b"), header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}},
		{name: "identity", body: []byte("hello"), header: http.Header{"Content-Type": {"text/plain"}, "Content-Encoding": {"Identity"}}},
		{name: "suppressed_encoding", body: []byte("hello"), header: http.Header{"Content-Type": {"text/plain"}, "Content-Encoding": nil}},
		{name: "binary", body: []byte{0, 255}, header: http.Header{"Content-Type": {"application/octet-stream"}}, wantBase: true},
		{name: "utf8_binary", body: []byte("hello"), header: http.Header{"Content-Type": {"application/octet-stream"}}, wantBase: true},
		{name: "invalid_utf8", body: []byte{255}, header: http.Header{"Content-Type": {"text/plain"}}, wantBase: true},
		{name: "compressed", body: []byte("hello"), header: http.Header{"Content-Type": {"text/plain"}, "Content-Encoding": {"gzip"}}, wantBase: true},
		{name: "multiple_encodings", body: []byte("hello"), header: http.Header{"Content-Type": {"text/plain"}, "Content-Encoding": {"identity", "gzip"}}, wantBase: true},
		{name: "missing_type", body: []byte("hello"), wantBase: true},
		{name: "suppressed_type", body: []byte("hello"), header: http.Header{"Content-Type": nil}, wantBase: true},
		{name: "invalid_type", body: []byte("hello"), header: http.Header{"Content-Type": {"text/plain; broken"}}, wantBase: true},
		{name: "bare_suffix", body: []byte("hello"), header: http.Header{"Content-Type": {"problem+json"}}, wantBase: true},
		{name: "ambiguous_type", body: []byte("hello"), header: http.Header{"Content-Type": {"text/plain", "application/octet-stream"}}, wantBase: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := slices.Clone(tt.body)
			got, binary, err := encodeResponseBody(tt.body, tt.header)
			if err != nil {
				t.Fatalf("encodeResponseBody() = %v", err)
			}
			if binary != tt.wantBase {
				t.Errorf("IsBase64Encoded = %v, want %v", binary, tt.wantBase)
			}
			decoded := []byte(got)
			if binary {
				decoded, err = base64.StdEncoding.DecodeString(got)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(decoded, original) || !bytes.Equal(tt.body, original) {
				t.Errorf("body round trip = %q, input after encoding = %q, want %q", decoded, tt.body, original)
			}
		})
	}
}

func TestResponseEncodingLimits(t *testing.T) {
	for _, tt := range []struct {
		name      string
		length    int
		mediaType string
		wantErr   bool
	}{
		{name: "text_at_limit", length: maxResponseBytes, mediaType: "text/plain"},
		{name: "text_over_limit", length: maxResponseBytes + 1, mediaType: "text/plain", wantErr: true},
		{name: "binary_at_limit", length: maxResponseBytes / 4 * 3, mediaType: "application/octet-stream"},
		{name: "binary_expansion_over_limit", length: maxResponseBytes/4*3 + 1, mediaType: "application/octet-stream", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), tt.length)
			got, binary, err := encodeResponseBody(body, http.Header{"Content-Type": {tt.mediaType}})
			if tt.wantErr {
				if !errors.Is(err, errResponseTooLarge) || got != "" || binary {
					t.Fatalf("over-limit encoding = (%d bytes, %v, %v), want no result and size error", len(got), binary, err)
				}
				return
			}
			if err != nil || len(got) != maxResponseBytes {
				t.Errorf("at-limit encoding = (%d bytes, %v), want %d bytes", len(got), err, maxResponseBytes)
			}
		})
	}
}

func TestMarshalResponseSDKTypes(t *testing.T) {
	t.Run("v1", func(t *testing.T) {
		want := events.APIGatewayProxyResponse{StatusCode: 201, Body: "hello", MultiValueHeaders: map[string][]string{"Set-Cookie": {"a=1", "b=2"}, "X-Trace": {"one", "two"}}}
		data, err := marshalResponse(want)
		if err != nil {
			t.Fatal(err)
		}
		var got events.APIGatewayProxyResponse
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.StatusCode != want.StatusCode || got.Body != want.Body || got.IsBase64Encoded || !slices.Equal(got.MultiValueHeaders["Set-Cookie"], want.MultiValueHeaders["Set-Cookie"]) || !slices.Equal(got.MultiValueHeaders["X-Trace"], want.MultiValueHeaders["X-Trace"]) {
			t.Errorf("V1 response round trip = %+v, want %+v", got, want)
		}
	})
	t.Run("v2", func(t *testing.T) {
		want := events.APIGatewayV2HTTPResponse{StatusCode: 200, Body: "AP8=", IsBase64Encoded: true, Headers: map[string]string{"Content-Type": "application/octet-stream"}, Cookies: []string{"a=1", "b=2"}}
		data, err := marshalResponse(want)
		if err != nil {
			t.Fatal(err)
		}
		var got events.APIGatewayV2HTTPResponse
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.StatusCode != want.StatusCode || got.Body != want.Body || !got.IsBase64Encoded || got.Headers["Content-Type"] != want.Headers["Content-Type"] || !slices.Equal(got.Cookies, want.Cookies) {
			t.Errorf("V2 response round trip = %+v, want %+v", got, want)
		}
	})
}

func TestMarshalResponseEnvelopeLimit(t *testing.T) {
	// Measure the actual codec overhead rather than coupling the test to field
	// order, optional-field tags, or whitespace choices in the pinned SDK.
	response := events.APIGatewayV2HTTPResponse{StatusCode: 200}
	empty, err := marshalResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []int{-1, 0, 1} {
		response.Body = strings.Repeat("x", maxResponseBytes-len(empty)+extra)
		got, err := marshalResponse(response)
		if extra > 0 {
			if !errors.Is(err, errResponseTooLarge) || got != nil {
				t.Errorf("oversized envelope = (%d bytes, %v), want nil and size error", len(got), err)
			}
			continue
		}
		if err != nil || len(got) != maxResponseBytes+extra {
			t.Errorf("envelope boundary %d = (%d bytes, %v)", extra, len(got), err)
		}
	}
	// The body itself is small enough, but JSON escape expansion is not.
	response.Body = strings.Repeat("\x00", maxResponseBytes/6)
	if got, err := marshalResponse(response); !errors.Is(err, errResponseTooLarge) || got != nil {
		t.Errorf("escaped envelope = (%d bytes, %v), want nil and size error", len(got), err)
	}
	response.Body = ""
	response.Cookies = []string{strings.Repeat("x", maxResponseBytes)}
	if got, err := marshalResponse(response); !errors.Is(err, errResponseTooLarge) || got != nil {
		t.Errorf("cookie envelope = (%d bytes, %v), want nil and size error", len(got), err)
	}
}

func TestMarshalResponseSanitizesCodecErrors(t *testing.T) {
	response := events.APIGatewayV2HTTPResponse{StatusCode: 200, Headers: map[string]string{"private-secret": "invalid\xff"}}
	got, err := marshalResponse(response)
	if err == nil || got != nil {
		t.Fatalf("invalid header UTF-8 = (%d bytes, %v), want nil and encoding error", len(got), err)
	}
	if strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "invalid") {
		t.Errorf("encoding error leaked application data: %v", err)
	}
}

func TestResponseBufferRejectsWholeWrite(t *testing.T) {
	var out responseBuffer
	first := bytes.Repeat([]byte("x"), maxResponseBytes-1)
	if n, err := out.Write(first); n != len(first) || err != nil {
		t.Fatalf("initial Write = (%d, %v)", n, err)
	}
	if n, err := out.Write([]byte("yz")); n != 0 || !errors.Is(err, errResponseTooLarge) {
		t.Errorf("crossing Write = (%d, %v), want zero and size error", n, err)
	}
	if !bytes.Equal(out.data, first) || cap(out.data) > maxResponseBytes {
		t.Errorf("failed Write retained data or excessive capacity: len=%d cap=%d", len(out.data), cap(out.data))
	}
	if n, err := out.Write([]byte("y")); n != 1 || err != nil || len(out.data) != maxResponseBytes || cap(out.data) > maxResponseBytes {
		t.Errorf("exact-limit Write = (%d, %v), len=%d cap=%d", n, err, len(out.data), cap(out.data))
	}
}

func BenchmarkMarshalResponse(b *testing.B) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "small_json", body: `{"ok":true}`},
		{name: "near_limit", body: strings.Repeat("x", maxResponseBytes-1024)},
		{name: "escaped_over_limit", body: strings.Repeat("\x00", maxResponseBytes/6)},
	} {
		b.Run(tt.name, func(b *testing.B) {
			response := events.APIGatewayV2HTTPResponse{StatusCode: 200, Body: tt.body}
			b.ReportAllocs()
			b.SetBytes(int64(len(tt.body)))
			for b.Loop() {
				_, err := marshalResponse(response)
				if err != nil && !errors.Is(err, errResponseTooLarge) {
					b.Fatal(err)
				}
			}
		})
	}
}
