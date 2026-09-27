package edge

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

// Decoder tests exercise the accepted wire contract directly while the public Invoke and typed HTTP entry points await their translation contract.
const minimalV1 = `{"httpMethod":"GET","path":"/","requestContext":{"apiId":"example"}}`
const minimalV2 = `{"version":"2.0","rawPath":"/","requestContext":{"apiId":"example","http":{"method":"GET"}}}`

func TestDecodeDocumentedFixtures(t *testing.T) {
	for _, tt := range []struct {
		file    string
		version string
		path    string
		method  string
		body    string
	}{
		{file: "rest", path: "/a?b#c", method: "POST", body: "{application-invalid-json"},
		{file: "http-v1", version: "1.0", path: "/my/path", method: "GET"},
		{file: "http-v2", version: "2.0", path: "/my/%2Fpath", method: "POST", body: "AP8="},
	} {
		t.Run(tt.file, func(t *testing.T) {
			payload, err := os.ReadFile("testdata/events/" + tt.file + ".json")
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeEvent(payload)
			if err != nil {
				t.Fatalf("decodeEvent(%s) = %v", tt.file, err)
			}
			if got.version != tt.version {
				t.Errorf("version = %q, want %q", got.version, tt.version)
			}
			var path, method, body string
			if tt.version == "2.0" {
				if got.v2 == nil || got.v1 != nil {
					t.Fatal("decodeEvent(v2) did not select exactly the v2 event")
				}
				path, method, body = got.v2.RawPath, got.v2.RequestContext.HTTP.Method, got.v2.Body
				if got.v2.RawQueryString != "parameter1=value1&parameter1=value2&literal=%2C" || !slices.Equal(got.v2.Cookies, []string{"a=1", "b=2"}) || !got.v2.IsBase64Encoded {
					t.Error("decodeEvent(v2) changed raw query, cookies, or binary flag")
				}
				if got.v2.RequestContext.Authorizer != nil {
					t.Error("decodeEvent(v2) prematurely interpreted authorizer JSON")
				}
			} else {
				if got.v1 == nil || got.v2 != nil {
					t.Fatal("decodeEvent(v1) did not select exactly the v1 event")
				}
				path, method, body = got.v1.Path, got.v1.HTTPMethod, got.v1.Body
				if got.v1.RequestContext.Authorizer != nil {
					t.Error("decodeEvent(v1) prematurely interpreted authorizer JSON")
				}
			}
			if path != tt.path || method != tt.method || body != tt.body {
				t.Errorf("path/method/body = (%q, %q, %q), want (%q, %q, %q)", path, method, body, tt.path, tt.method, tt.body)
			}
		})
	}
}

func TestDecodeRejectsInvalidEnvelopes(t *testing.T) {
	for name, payload := range map[string]string{
		"empty":                "",
		"null":                 "null",
		"array":                "[]",
		"string":               `"event"`,
		"number":               "12",
		"empty_object":         "{}",
		"trailing_value":       minimalV1 + " {}",
		"trailing_garbage":     minimalV1 + " broken",
		"sqs":                  `{"Records":[{"eventSource":"aws:sqs"}]}`,
		"alb":                  `{"httpMethod":"GET","path":"/","requestContext":{"elb":{}}}`,
		"null_context":         strings.Replace(minimalV1, `{"apiId":"example"}`, "null", 1),
		"array_context":        strings.Replace(minimalV1, `{"apiId":"example"}`, "[]", 1),
		"missing_context":      `{"httpMethod":"GET","path":"/"}`,
		"empty_api_id":         strings.Replace(minimalV1, `"example"`, `""`, 1),
		"null_api_id":          strings.Replace(minimalV1, `"example"`, "null", 1),
		"numeric_api_id":       strings.Replace(minimalV1, `"example"`, "1", 1),
		"missing_method":       strings.Replace(minimalV1, `"httpMethod":"GET",`, "", 1),
		"null_method":          strings.Replace(minimalV1, `"GET"`, "null", 1),
		"empty_method":         strings.Replace(minimalV1, `"GET"`, `""`, 1),
		"wrong_case_method":    strings.Replace(minimalV1, "httpMethod", "HTTPMethod", 1),
		"null_path":            strings.Replace(minimalV1, `"path":"/"`, `"path":null`, 1),
		"missing_path":         strings.Replace(minimalV1, `"path":"/",`, "", 1),
		"null_version":         strings.Replace(minimalV2, `"2.0"`, "null", 1),
		"numeric_version":      strings.Replace(minimalV2, `"2.0"`, "2.0", 1),
		"empty_version":        strings.Replace(minimalV2, `"2.0"`, `""`, 1),
		"future_version":       strings.Replace(minimalV2, `"2.0"`, `"3.0"`, 1),
		"v2_missing_http":      strings.Replace(minimalV2, `,"http":{"method":"GET"}`, "", 1),
		"v2_null_http":         strings.Replace(minimalV2, `{"method":"GET"}`, "null", 1),
		"v2_missing_raw_path":  strings.Replace(minimalV2, `"rawPath":"/",`, "", 1),
		"v2_no_path_fallback":  strings.Replace(minimalV2, `"rawPath":"/"`, `"path":"/","httpMethod":"GET"`, 1),
		"duplicate_method":     strings.Replace(minimalV1, `"GET"`, `"GET","httpMethod":"POST"`, 1),
		"escaped_duplicate":    strings.Replace(minimalV1, `"GET"`, `"GET","http\u004dethod":"POST"`, 1),
		"unknown_duplicate":    strings.Replace(minimalV1, `"path":"/"`, `"path":"/","future":{"x":1,"x":2}`, 1),
		"null_binary_flag":     strings.Replace(minimalV1, `"path":"/"`, `"path":"/","isBase64Encoded":null`, 1),
		"string_binary_flag":   strings.Replace(minimalV1, `"path":"/"`, `"path":"/","isBase64Encoded":"true"`, 1),
		"numeric_body":         strings.Replace(minimalV1, `"path":"/"`, `"path":"/","body":1`, 1),
		"array_headers":        strings.Replace(minimalV1, `"path":"/"`, `"path":"/","headers":[]`, 1),
		"null_header_value":    strings.Replace(minimalV1, `"path":"/"`, `"path":"/","headers":{"x":null}`, 1),
		"null_multivalue_item": strings.Replace(minimalV1, `"path":"/"`, `"path":"/","multiValueHeaders":{"x":[null]}`, 1),
		"null_cookie_item":     strings.Replace(minimalV2, `"rawPath":"/"`, `"rawPath":"/","cookies":[null]`, 1),
		"invalid_utf8":         strings.Replace(minimalV1, "example", "\xff", 1),
		"authorizer_duplicate": strings.Replace(minimalV1, `"apiId":"example"`, `"apiId":"example","authorizer":{"secret":1,"secret":2}`, 1),
		"authorizer_bad_utf8":  strings.Replace(minimalV1, `"apiId":"example"`, "\"apiId\":\"example\",\"authorizer\":\"\xff\"", 1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeEvent([]byte(payload))
			if err == nil {
				t.Fatal("decodeEvent(invalid envelope) succeeded")
			}
			if got.v1 != nil || got.v2 != nil || got.version != "" || got.authorizer != nil {
				t.Error("decodeEvent(invalid envelope) returned partial data")
			}
		})
	}
}

func TestDecodePreservesOpaqueAuthorizer(t *testing.T) {
	for _, base := range []string{minimalV1, minimalV2} {
		for _, authorizer := range []string{`null`, `"opaque"`, `[1,"two"]`, `9007199254740993`, `{"jwt":{"claims":{"groups":["a","b"],"n":9007199254740993,"exponent":1e999}}}`} {
			payload := []byte(strings.Replace(base, `"apiId":"example"`, `"apiId":"example","authorizer":`+authorizer, 1))
			got, err := decodeEvent(payload)
			if err != nil {
				t.Fatalf("decodeEvent(authorizer=%s) = %v", authorizer, err)
			}
			clear(payload)
			if string(got.authorizer) != authorizer {
				t.Errorf("authorizer after clearing input = %s, want %s", got.authorizer, authorizer)
			}
		}
	}
}

func TestDecodeDoesNotExposeInputInErrors(t *testing.T) {
	const secret = "credential-marker-not-for-errors"
	for _, payload := range []string{
		`{"` + secret + `":1,"` + secret + `":2}`,
		strings.Replace(minimalV2, `"2.0"`, `"`+secret+`"`, 1),
		strings.Replace(minimalV1, `"path":"/"`, `"path":"/","headers":{"`+secret+`":123}`, 1),
		strings.Replace(minimalV1, `"GET"`, `{"`+secret+`":true}`, 1),
	} {
		_, err := decodeEvent([]byte(payload))
		if err == nil {
			t.Fatal("decodeEvent(invalid secret-bearing input) succeeded")
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("decodeEvent error leaked input: %v", err)
		}
	}
}

func TestDecodeNullOptionalFieldsAndIndependentMaps(t *testing.T) {
	payload := strings.Replace(minimalV1, `"path":"/"`, `"path":"/","body":null,"headers":{"x":"single","single":"keep"},"multiValueHeaders":{"x":["a","b"]},"queryStringParameters":null,"multiValueQueryStringParameters":null`, 1)
	got, err := decodeEvent([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if got.v1.Body != "" || got.v1.IsBase64Encoded || got.v1.Headers["single"] != "keep" || got.v1.Headers["x"] != "single" || !slices.Equal(got.v1.MultiValueHeaders["x"], []string{"a", "b"}) {
		t.Error("decodeEvent changed optional defaults or merged single/multivalue headers before HTTP translation")
	}
}

func TestTypedValidationAndSDKCompatibility(t *testing.T) {
	v1 := events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: "/", RequestContext: events.APIGatewayProxyRequestContext{APIID: "example"}}
	v2 := events.APIGatewayV2HTTPRequest{RawPath: "/", RequestContext: events.APIGatewayV2HTTPRequestContext{APIID: "example", HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "GET"}}}
	if err := validateV1(v1); err != nil {
		t.Errorf("validateV1(valid) = %v", err)
	}
	for _, version := range []string{"", "2.0", "1.0", "3.0"} {
		v2.Version = version
		err := validateV2(v2)
		wantErr := version != "" && version != "2.0"
		if (err != nil) != wantErr {
			t.Errorf("validateV2(version=%q) = %v, want error %v", version, err, wantErr)
		}
	}
	v2.Version = "2.0"
	for _, event := range []any{v1, v2} {
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeEvent(payload); err != nil {
			t.Errorf("decodeEvent(SDK %T) = %v", event, err)
		}
	}
	v1.RequestContext.APIID = ""
	v2.RequestContext.APIID = ""
	if validateV1(v1) == nil || validateV2(v2) == nil {
		t.Error("typed validation accepted missing API ID")
	}
}

func FuzzDecodeEvent(f *testing.F) {
	f.Add([]byte(minimalV1))
	f.Add([]byte(minimalV2))
	f.Add([]byte(`{"version":"2.0","version":"1.0"}`))
	for _, file := range []string{"rest", "http-v1", "http-v2"} {
		payload, err := os.ReadFile("testdata/events/" + file + ".json")
		if err != nil {
			f.Fatal(err)
		}
		f.Add(payload)
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		before := bytes.Clone(payload)
		got, err := decodeEvent(payload)
		if !bytes.Equal(payload, before) {
			t.Fatal("decodeEvent mutated input")
		}
		if err != nil {
			if got.v1 != nil || got.v2 != nil || got.authorizer != nil || got.version != "" {
				t.Fatal("decodeEvent returned partial data with error")
			}
			return
		}
		var object map[string]jsontext.Value
		if err := json.Unmarshal(payload, &object); err != nil || object == nil {
			t.Fatalf("decodeEvent accepted a non-object or invalid JSON: %v", err)
		}
		if (got.v1 == nil) == (got.v2 == nil) {
			t.Fatal("decodeEvent did not select exactly one event format")
		}
		if got.v2 != nil && (got.v2.Version != "2.0" || got.v2.RawPath == "" || got.v2.RequestContext.HTTP.Method == "" || got.v2.RequestContext.APIID == "") {
			t.Fatal("decodeEvent accepted v2 without its required values")
		}
		if got.v1 != nil && (got.v1.Path == "" || got.v1.HTTPMethod == "" || got.v1.RequestContext.APIID == "") {
			t.Fatal("decodeEvent accepted v1 without its required values")
		}
	})
}
