package orders_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/authn"
	"github.com/asteroid-computing/go-lambda-edge/examples/dispatcher/internal/orders"
	"github.com/asteroid-computing/go-lambda-edge/iamproof"
)

const (
	issuer    = "https://issuer.example/pool"
	principal = "arn:aws:iam::123456789012:user/Alice"
	resource  = "/tenants/42/orders/7"
	origin    = "https://app.example"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// providerFixtures uses production verifiers and local cryptographic signatures.
// STS is a synthetic response, not an AWS signature or enrollment assertion.
// Neither transport ever delegates to a network-capable RoundTripper.
func providerFixtures(t *testing.T) (orders.Config, map[string]string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := marshal(t, map[string]any{"keys": []any{map[string]any{
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"kid": "fixture",
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
	now := time.Now().Unix()
	claims := marshal(t, map[string]any{"iss": issuer, "sub": "alice", "client_id": "app", "aud": "https://orders.example", "token_use": "access", "scope": "orders.read", "iat": now, "exp": now + 3600})
	header := marshal(t, map[string]string{"alg": "RS256", "kid": "fixture"})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	jwt := input + "." + base64.RawURLEncoding.EncodeToString(signature)
	generator, err := iamproof.NewGenerator("eu-west-2", "orders.test", aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "synthetic-only"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := generator.Generate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg := orders.Config{
		Cognito: authn.CognitoConfig{
			Issuer:  issuer,
			Clients: []authn.CognitoClient{{ClientID: "app", Audience: "https://orders.example"}},
			Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != issuer+"/.well-known/jwks.json" {
					return nil, errors.New("unexpected JWKS request")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(jwks))}, nil
			}),
		},
		Region:        "eu-west-2",
		ProofAudience: "orders.test",
		IAMPrincipal:  principal,
		Origin:        origin,
		STSTransport: transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "sts.eu-west-2.amazonaws.com" || r.Header.Get("X-Edge-IAM-Audience") != "orders.test" || r.URL.Query().Get("Action") != "GetCallerIdentity" || r.URL.Query().Get("X-Amz-Signature") == "" {
				return nil, errors.New("unexpected STS request")
			}
			body := `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Arn>` + principal + `</Arn><Account>123456789012</Account><UserId>AIDAEXAMPLE</UserId></GetCallerIdentityResult></GetCallerIdentityResponse>`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	}
	return cfg, map[string]string{"bearer": "Bearer " + jwt, "iam": "EdgeIAM " + proof.Value()}
}

func marshal(t testing.TB, value any) []byte {
	t.Helper()
	wire, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func application(t *testing.T, cfg orders.Config) http.Handler {
	t.Helper()
	h, err := orders.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Explicitly authored fixtures retain the product/version distinction.
// The SDK V1 request type cannot distinguish REST from HTTP API payload 1.0 by itself.
func restEvent(path string, headers http.Header) events.APIGatewayProxyRequest {
	return events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: path, MultiValueHeaders: headers.Clone(), RequestContext: events.APIGatewayProxyRequestContext{APIID: "synthetic-rest"}}
}

func httpV2Event(path string, headers http.Header) events.APIGatewayV2HTTPRequest {
	combined := make(map[string]string)
	var cookies []string
	for name, values := range headers {
		if len(values) == 0 {
			continue
		}
		if strings.EqualFold(name, "Cookie") {
			cookies = append(cookies, values...)
			continue
		}
		combined[strings.ToLower(name)] = strings.Join(values, ",")
	}
	return events.APIGatewayV2HTTPRequest{
		Version:        "2.0",
		RawPath:        path,
		Headers:        combined,
		Cookies:        cookies,
		RequestContext: events.APIGatewayV2HTTPRequestContext{APIID: "synthetic-http", HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "GET"}},
	}
}

// Small responses only.
// Repeated V2 headers remain combined;
// cookies are kept separate.
// Incremental/late-error assertions use the reader directly elsewhere.
type response struct {
	status  int
	headers http.Header
	cookies []string
	body    []byte
}

func serve(t *testing.T, h http.Handler, format, path string, headers http.Header) response {
	t.Helper()
	if format == "native" {
		server := httptest.NewTestServer(t, h)
		r, err := http.NewRequestWithContext(t.Context(), "GET", "http://example.com"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header = headers.Clone()
		out, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Body.Close()
		body, err := io.ReadAll(out.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response{status: out.StatusCode, headers: out.Header, body: body}
	}
	if format == "stream_raw" || format == "stream_typed" {
		stream := openStream(t, h, format, path, headers)
		defer stream.Close()
		wire, err := io.ReadAll(stream)
		if err != nil {
			t.Fatal(err)
		}
		prefix, body, ok := bytes.Cut(wire, make([]byte, 8))
		if !ok {
			t.Fatal("stream missing metadata delimiter")
		}
		var metadata struct {
			Status  int         `json:"statusCode"`
			Headers http.Header `json:"multiValueHeaders"`
		}
		if err := json.Unmarshal(prefix, &metadata); err != nil {
			t.Fatal(err)
		}
		return response{status: metadata.Status, headers: metadata.Headers, body: body}
	}
	adapter, err := edge.New(h)
	if err != nil {
		t.Fatal(err)
	}
	v1, v2 := restEvent(path, headers), httpV2Event(path, headers)
	var out1 events.APIGatewayProxyResponse
	var out2 events.APIGatewayV2HTTPResponse
	switch format {
	case "typed_v1":
		out1, err = adapter.HandleV1(t.Context(), v1)
	case "typed_v2":
		out2, err = adapter.HandleV2(t.Context(), v2)
	case "raw_rest", "raw_http_v1", "raw_http_v2":
		wire := marshal(t, v1)
		if format == "raw_http_v1" {
			// HTTP API 1.0's version is present only at the wire boundary.
			wire = append([]byte(`{"version":"1.0",`), wire[1:]...)
		}
		if format == "raw_http_v2" {
			wire = marshal(t, v2)
		}
		var out []byte
		out, err = adapter.Invoke(t.Context(), wire)
		if err == nil {
			if format == "raw_http_v2" {
				err = json.Unmarshal(out, &out2)
			} else {
				err = json.Unmarshal(out, &out1)
			}
		}
	default:
		t.Fatalf("unknown fixture format %q", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(format, "v2") {
		h := make(http.Header)
		for name, value := range out2.Headers {
			h.Set(name, value)
		}
		return response{status: out2.StatusCode, headers: h, cookies: out2.Cookies, body: decodeBody(t, out2.Body, out2.IsBase64Encoded)}
	}
	return response{status: out1.StatusCode, headers: http.Header(out1.MultiValueHeaders), body: decodeBody(t, out1.Body, out1.IsBase64Encoded)}
}

func decodeBody(t *testing.T, body string, encoded bool) []byte {
	t.Helper()
	if !encoded {
		return []byte(body)
	}
	out, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func openStream(t *testing.T, h http.Handler, format, path string, headers http.Header) io.ReadCloser {
	t.Helper()
	adapter, err := edge.NewStreaming(h)
	if err != nil {
		t.Fatal(err)
	}
	var stream io.ReadCloser
	if format == "stream_raw" {
		stream, err = adapter.Handle(t.Context(), marshal(t, restEvent(path, headers)))
	} else {
		stream, err = adapter.HandleV1(t.Context(), restEvent(path, headers))
	}
	if err != nil {
		t.Fatal(err)
	}
	return stream
}
