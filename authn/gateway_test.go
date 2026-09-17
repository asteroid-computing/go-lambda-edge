package authn_test

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/authn"
	"github.com/asteroid-computing/go-lambda-edge/authz"
	"github.com/asteroid-computing/go-lambda-edge/iamproof"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func localProofVerifier(t testing.TB) (*iamproof.Verifier, iamproof.Token, *atomic.Int64) {
	t.Helper()
	g, err := iamproof.NewGenerator("eu-west-2", "orders.production", aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "synthetic-only", SessionToken: "synthetic+/="}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	token, err := g.Generate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	count := new(atomic.Int64)
	v, err := iamproof.NewVerifier("eu-west-2", "orders.production", iamproof.WithTransport(transportFunc(func(r *http.Request) (*http.Response, error) {
		count.Add(1)
		if r.URL.Host != "sts.eu-west-2.amazonaws.com" || r.Header.Get("X-Edge-IAM-Audience") != "orders.production" || r.URL.Query().Get("Action") != "GetCallerIdentity" || r.URL.Query().Get("X-Amz-Signature") == "" {
			t.Error("incorrect reconstructed STS request")
		}
		// A synthetic STS response exercises production composition, not AWS
		// acceptance. Signature reconstruction is separately tested by iamproof.
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Arn>arn:aws:iam::123456789012:user/Alice</Arn><Account>123456789012</Account><UserId>AIDAEXAMPLE</UserId></GetCallerIdentityResult></GetCallerIdentityResponse>`))}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	return v, token, count
}

func mapProofErrors(v *iamproof.Verifier) authn.VerifyFunc {
	return func(ctx context.Context, token string) (identity.Caller, error) {
		caller, err := v.Verify(ctx, token)
		if errors.Is(err, iamproof.ErrInvalidProof) {
			return identity.Caller{}, authn.ErrInvalidCredentials
		}
		return caller, err
	}
}

func TestAuthenticationAcrossTransports(t *testing.T) {
	proofVerifier, proof, requests := localProofVerifier(t)
	key := signingKey(t)
	jwt := signedToken(t, key, "integration", accessClaims())
	keys := keySet(t, publicJWK(key, "integration"))
	selector, err := edge.NewActionHeader("Action")
	if err != nil {
		t.Fatal(err)
	}
	jwtRule, err := authz.JWTSubject(cognitoIssuer, "alice")
	if err != nil {
		t.Fatal(err)
	}
	iamRule, err := authz.IAMPrincipal("arn:aws:iam::123456789012:user/Alice")
	if err != nil {
		t.Fatal(err)
	}
	permission, err := authz.Any(jwtRule, iamRule)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action, err := selector.Parse(r.Header)
		if err != nil {
			http.Error(w, "invalid action", 400)
			return
		}
		// Deny by default and authorize this exact action before executing it.
		if action != "orders.read" || permission.Authorize(r.Context(), authz.Request{Caller: identity.FromContext(r.Context()), Action: action}) != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="edge"`)
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Header.Get("Custom-Trace") != "keep" {
			t.Error("middleware lost extra consumer header")
		}
		w.WriteHeader(204)
	})
	for _, tc := range []struct {
		name       string
		values     []string
		action     string
		status     int
		challenges []string
	}{
		{name: "missing", status: 401, challenges: []string{`Bearer realm="edge"`, `EdgeIAM realm="edge"`}},
		{name: "bearer_success", values: []string{"Bearer " + jwt}, status: 204},
		{name: "iam_success", values: []string{"EdgeIAM " + proof.Value()}, status: 204},
		{name: "bearer_rejected", values: []string{"Bearer invalid"}, status: 401, challenges: []string{`Bearer realm="edge", error="invalid_token"`}},
		{name: "iam_rejected", values: []string{"EdgeIAM v2.invalid"}, status: 401, challenges: []string{`EdgeIAM realm="edge"`}},
		{name: "iam_proof_size", values: []string{"EdgeIAM v1." + strings.Repeat("A", 8192)}, status: 401, challenges: []string{`EdgeIAM realm="edge"`}},
		{name: "duplicates", values: []string{"Bearer synthetic-jwt", "Bearer synthetic-jwt"}, status: 400, challenges: []string{`Bearer realm="edge"`, `EdgeIAM realm="edge"`}},
		{name: "combined", values: []string{"Bearer synthetic-jwt,EdgeIAM invalid"}, status: 400, challenges: []string{`Bearer realm="edge"`, `EdgeIAM realm="edge"`}},
		{name: "malformed", values: []string{"Bearer a=b"}, status: 400, challenges: []string{`Bearer realm="edge", error="invalid_request"`, `EdgeIAM realm="edge"`}},
		{name: "unsupported", values: []string{"Basic abc"}, status: 401, challenges: []string{`Bearer realm="edge"`, `EdgeIAM realm="edge"`}},
		{name: "dependency", values: []string{"Bearer " + jwt}, status: 503},
		{name: "authz_denial", values: []string{"Bearer " + jwt}, action: "orders.delete", status: 403, challenges: []string{`Bearer realm="edge"`}},
		{name: "iam_authz_denial", values: []string{"EdgeIAM " + proof.Value()}, action: "orders.delete", status: 403, challenges: []string{`Bearer realm="edge"`}},
		{name: "field_size", values: []string{"Bearer " + strings.Repeat("A", 16384)}, status: 431},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verifier := cognitoVerifier(t, cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) {
				if tc.name == "dependency" {
					return nil, errors.New("synthetic JWKS outage")
				}
				return jwksResponse(keys), nil
			})))
			a := authenticator(t, authn.Config{Bearer: verifier.Verify, IAMProof: mapProofErrors(proofVerifier)})
			h := handler(t, a, dispatcher)
			for _, format := range []string{"native", "typed_v1", "typed_v2", "raw_rest", "raw_http_v1", "raw_http_v2"} {
				t.Run(format, func(t *testing.T) {
					action := tc.action
					if action == "" {
						action = "orders.read"
					}
					status, headers := serveFormat(t, h, format, tc.values, action)
					wantChallenges := tc.challenges
					if strings.HasSuffix(format, "v2") && len(wantChallenges) > 0 {
						wantChallenges = []string{strings.Join(wantChallenges, ", ")}
					}
					if status != tc.status || !slices.Equal(headers.Values("WWW-Authenticate"), wantChallenges) {
						t.Errorf("response = %d %q; want %d %q", status, headers.Values("WWW-Authenticate"), tc.status, wantChallenges)
					}
				})
			}
		})
	}
	if requests.Load() != 12 {
		t.Fatalf("STS requests=%d; want 12 for the two valid-proof cases across six transports", requests.Load())
	}
}

func serveFormat(t *testing.T, h http.Handler, format string, values []string, action string) (int, http.Header) {
	t.Helper()
	status, headers, _ := serveFormatBody(t, h, format, values, action)
	return status, headers
}

func serveFormatBody(t *testing.T, h http.Handler, format string, values []string, action string) (int, http.Header, []byte) {
	t.Helper()
	if format == "native" {
		server := httptest.NewTestServer(t, h)
		req, err := http.NewRequestWithContext(t.Context(), "GET", "http://example.com/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = http.Header{"Authorization": values, "Action": {action}, "Custom-Trace": {"keep"}}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header, body
	}
	adapter, err := edge.New(h)
	if err != nil {
		t.Fatal(err)
	}
	v1 := events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: "/", RequestContext: events.APIGatewayProxyRequestContext{APIID: "example"},
		Headers: map[string]string{"action": action, "custom-trace": "keep"}}
	v2 := events.APIGatewayV2HTTPRequest{Version: "2.0", RawPath: "/", RequestContext: events.APIGatewayV2HTTPRequestContext{APIID: "example", HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "GET"}},
		Headers: map[string]string{"action": action, "custom-trace": "keep"}}
	if len(values) != 0 {
		v1.Headers["authorization"] = values[0]
		v1.MultiValueHeaders = map[string][]string{"Authorization": values}
		v2.Headers["authorization"] = strings.Join(values, ",")
	}
	var resp1 events.APIGatewayProxyResponse
	var resp2 events.APIGatewayV2HTTPResponse
	switch format {
	case "typed_v1":
		resp1, err = adapter.HandleV1(t.Context(), v1)
	case "typed_v2":
		resp2, err = adapter.HandleV2(t.Context(), v2)
	default:
		var event any = v1
		if format == "raw_http_v2" {
			event = v2
		}
		wire, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if format == "raw_http_v1" {
			wire = append([]byte(`{"version":"1.0",`), wire[1:]...)
		}
		out, invokeErr := adapter.Invoke(t.Context(), wire)
		if invokeErr != nil {
			t.Fatal(invokeErr)
		}
		if format == "raw_http_v2" {
			err = json.Unmarshal(out, &resp2)
		} else {
			err = json.Unmarshal(out, &resp1)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(format, "v2") {
		headers := make(http.Header)
		for name, value := range resp2.Headers {
			headers.Set(name, value)
		}
		return resp2.StatusCode, headers, responseBody(t, resp2.Body, resp2.IsBase64Encoded)
	}
	return resp1.StatusCode, http.Header(resp1.MultiValueHeaders), responseBody(t, resp1.Body, resp1.IsBase64Encoded)
}

func responseBody(t *testing.T, body string, base64Encoded bool) []byte {
	t.Helper()
	if !base64Encoded {
		return []byte(body)
	}
	out, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIAMErrorMappingIsExplicit(t *testing.T) {
	v, _, _ := localProofVerifier(t)
	for _, mapped := range []bool{false, true} {
		verify := authn.VerifyFunc(v.Verify)
		if mapped {
			verify = mapProofErrors(v)
		}
		a := authenticator(t, authn.Config{IAMProof: verify})
		caller, err := a.Authenticate(t.Context(), http.Header{"Authorization": {"EdgeIAM v2.invalid"}})
		if mapped {
			failure(t, caller, err, authn.ErrInvalidCredentials, 401, `EdgeIAM realm="edge"`)
		} else {
			failure(t, caller, err, authn.ErrUnavailable, 503)
		}
	}
}

func TestGatewayProducerConflictAndInvocationCancellation(t *testing.T) {
	a := authenticator(t, authn.Config{Bearer: func(context.Context, string) (identity.Caller, error) {
		t.Error("conflicting/canceled request reached verifier")
		return identity.Caller{}, authn.ErrInvalidCredentials
	}})
	h := handler(t, a, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("failure reached handler") }))
	adapter, err := edge.New(h, edge.WithGatewayIdentity(true))
	if err != nil {
		t.Fatal(err)
	}
	event := events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: "/", RequestContext: events.APIGatewayProxyRequestContext{
		APIID: "example", Identity: events.APIGatewayRequestIdentity{UserArn: "arn:aws:iam::123456789012:user/Alice", AccountID: "123456789012"},
	}}
	resp, err := adapter.HandleV1(t.Context(), event)
	if err != nil || resp.StatusCode != 500 || len(resp.MultiValueHeaders["Www-Authenticate"]) != 0 {
		t.Fatalf("gateway/local conflict: response=%d err=%v; want HTTP 500", resp.StatusCode, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	resp, err = adapter.HandleV1(ctx, event)
	if !errors.Is(err, context.Canceled) || resp.StatusCode != 0 {
		t.Fatalf("canceled invocation returned usable response=%d err=%v", resp.StatusCode, err)
	}
}

func TestNoAlternateCredentialsOrOptionsBypass(t *testing.T) {
	a := authenticator(t, authn.Config{Bearer: func(context.Context, string) (identity.Caller, error) {
		t.Error("alternate credential source reached verifier")
		return identity.Caller{}, authn.ErrInvalidCredentials
	}})
	h := handler(t, a, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("anonymous request reached next") }))
	for _, method := range []string{http.MethodPost, http.MethodOptions} {
		r := httptest.NewRequest(method, "/?access_token=secret", strings.NewReader("access_token=secret"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: "Authorization", Value: "secret"})
		r.Header.Set("X-Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "access_token=secret" || w.Code != 401 {
			t.Fatalf("method %s: status=%d body=%q err=%v", method, w.Code, body, err)
		}
	}
}
