package authn_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/asteroid-computing/go-lambda-edge/authn"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

const cognitoIssuer = "https://issuer.example/pool"

func signingKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func encodeJSON(t testing.TB, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func publicJWK(key *rsa.PrivateKey, kid string) map[string]any {
	return map[string]any{
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}
}

func keySet(t testing.TB, entries ...map[string]any) string {
	t.Helper()
	return string(encodeJSON(t, map[string]any{"keys": entries}))
}

func accessClaims() map[string]any {
	now := time.Now().Unix()
	return map[string]any{
		"iss":            cognitoIssuer,
		"client_id":      "app",
		"sub":            "alice",
		"token_use":      "access",
		"aud":            "https://orders.example",
		"iat":            now,
		"exp":            now + 3600,
		"scope":          "orders.read",
		"cognito:groups": []string{"operators"},
		"precise":        jsontext.Value("9007199254740993"),
	}
}

func signRawToken(t testing.TB, key *rsa.PrivateKey, header, payload []byte) string {
	t.Helper()
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func signedToken(t testing.TB, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	return signRawToken(t, key, encodeJSON(t, map[string]any{"alg": "RS256", "kid": kid}), encodeJSON(t, claims))
}

func cognitoConfig(transport http.RoundTripper) authn.CognitoConfig {
	return authn.CognitoConfig{Issuer: cognitoIssuer, Clients: []authn.CognitoClient{{ClientID: "app", Audience: "https://orders.example"}}, Transport: transport}
}

func cognitoVerifier(t testing.TB, cfg authn.CognitoConfig) *authn.CognitoVerifier {
	t.Helper()
	v, err := authn.NewCognitoVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func jwksResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func verifyResult(t testing.TB, v *authn.CognitoVerifier, ctx context.Context, token string, want error) identity.Caller {
	t.Helper()
	caller, err := v.Verify(ctx, token)
	if !errors.Is(err, want) {
		t.Fatalf("Verify error = %v; want %v", err, want)
	}
	if err != nil {
		if caller.Kind() != identity.KindAnonymous {
			t.Fatal("verification failure returned an identity")
		}
		return caller
	}
	if caller.Kind() != identity.KindJWT || caller.Source() != identity.SourceLocallyVerifiedToken {
		t.Fatalf("unexpected verified caller: %v", caller)
	}
	if identity.FromContext(ctx).Kind() != identity.KindAnonymous {
		t.Fatal("Verify installed a caller")
	}
	return caller
}

func TestCognitoConfiguration(t *testing.T) {
	never := transportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("constructor performed I/O")
		return nil, errors.New("unexpected I/O")
	})
	for _, issuer := range []string{"", "http://issuer.example/pool", "https://issuer.example", "https://issuer.example/", "https://user@issuer.example/pool", "https://issuer.example/pool/", "https://issuer.example/a/../pool", "https://issuer.example/%70ool", "https://issuer.example/pool?", "https://issuer.example/pool#", "https://issuer.example/pool?q=x", "https://issuer.example/pool#x"} {
		cfg := cognitoConfig(never)
		cfg.Issuer = issuer
		if v, err := authn.NewCognitoVerifier(cfg); v != nil || !errors.Is(err, authn.ErrInvalidConfiguration) {
			t.Errorf("issuer %q: got %v, %v", issuer, v, err)
		}
	}
	for _, mutate := range []func(*authn.CognitoConfig){
		func(c *authn.CognitoConfig) { c.Clients = nil },
		func(c *authn.CognitoConfig) { c.Clients[0].ClientID = "" },
		func(c *authn.CognitoConfig) { c.Clients[0].Audience = "" },
		func(c *authn.CognitoConfig) { c.Clients = append(c.Clients, c.Clients[0]) },
		func(c *authn.CognitoConfig) { c.ClockSkew = -1 },
		func(c *authn.CognitoConfig) { c.ClockSkew = 5*time.Minute + 1 },
		func(c *authn.CognitoConfig) { c.CacheTTL = time.Second },
		func(c *authn.CognitoConfig) { c.CacheTTL = 24*time.Hour + 1 },
		func(c *authn.CognitoConfig) { c.CacheTTL = -1 },
		func(c *authn.CognitoConfig) { c.Timeout = -1 },
		func(c *authn.CognitoConfig) { c.Timeout = time.Minute + 1 },
		func(c *authn.CognitoConfig) { c.MaxTokenBytes = -1 },
		func(c *authn.CognitoConfig) { c.MaxTokenBytes = 1<<20 + 1 },
		func(c *authn.CognitoConfig) { c.ClaimsBudget = -1 },
		func(c *authn.CognitoConfig) { c.ClaimsBudget = 6<<20 + 1 },
		func(c *authn.CognitoConfig) { c.Transport = (*http.Transport)(nil) },
		func(c *authn.CognitoConfig) { c.Transport = transportFunc(nil) },
	} {
		cfg := cognitoConfig(never)
		mutate(&cfg)
		if v, err := authn.NewCognitoVerifier(cfg); v != nil || !errors.Is(err, authn.ErrInvalidConfiguration) {
			t.Errorf("invalid config produced %v, %v", v, err)
		}
	}
	for _, issuer := range []string{"https://cognito-idp.eu-west-2.amazonaws.com/eu-west-2_example", "https://issuer-cognito-idp.eu-west-2.amazonaws.com/eu-west-2_example"} {
		cfg := cognitoConfig(never)
		cfg.Issuer = issuer
		cognitoVerifier(t, cfg)
	}
	for _, v := range []*authn.CognitoVerifier{nil, new(authn.CognitoVerifier)} {
		_, err := v.Verify(t.Context(), "x")
		if !errors.Is(err, authn.ErrInvalidConfiguration) || !errors.Is(v.Warm(t.Context()), authn.ErrInvalidConfiguration) {
			t.Fatal("unconfigured verifier did not fail configuration validation")
		}
	}
	v := cognitoVerifier(t, cognitoConfig(never))
	_, err := v.Verify(nil, "x")
	if !errors.Is(err, authn.ErrInvalidConfiguration) || !errors.Is(v.Warm(nil), authn.ErrInvalidConfiguration) {
		t.Fatal("nil context did not fail configuration validation")
	}
}

func TestCognitoClientAudienceAndClaims(t *testing.T) {
	key := signingKey(t)
	body := keySet(t, publicJWK(key, "access-key="))
	for _, tc := range []struct {
		name    string
		policy  authn.CognitoClient
		aud     any
		present bool
		want    error
	}{
		{"bound", authn.CognitoClient{ClientID: "app", Audience: "https://orders.example"}, "https://orders.example", true, nil},
		{"bound_missing", authn.CognitoClient{ClientID: "app", Audience: "https://orders.example"}, nil, false, authn.ErrInvalidCredentials},
		{"bound_wrong", authn.CognitoClient{ClientID: "app", Audience: "https://orders.example"}, "elsewhere", true, authn.ErrInvalidCredentials},
		{"mixed_bound", authn.CognitoClient{ClientID: "app", Audience: "https://orders.example", AllowUnbound: true}, "https://orders.example", true, nil},
		{"mixed_unbound", authn.CognitoClient{ClientID: "app", Audience: "https://orders.example", AllowUnbound: true}, nil, false, nil},
		{"mixed_wrong", authn.CognitoClient{ClientID: "app", Audience: "https://orders.example", AllowUnbound: true}, "elsewhere", true, authn.ErrInvalidCredentials},
		{"m2m", authn.CognitoClient{ClientID: "app", AllowUnbound: true}, nil, false, nil},
		{"m2m_wrong", authn.CognitoClient{ClientID: "app", AllowUnbound: true}, "https://orders.example", true, authn.ErrInvalidCredentials},
		{"null", authn.CognitoClient{ClientID: "app", AllowUnbound: true}, nil, true, authn.ErrInvalidCredentials},
		{"empty", authn.CognitoClient{ClientID: "app", AllowUnbound: true}, "", true, authn.ErrInvalidCredentials},
		{"array", authn.CognitoClient{ClientID: "app", Audience: "https://orders.example"}, []string{"https://orders.example"}, true, authn.ErrInvalidCredentials},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			cfg := cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return jwksResponse(body), nil
			}))
			cfg.Clients = []authn.CognitoClient{tc.policy}
			v := cognitoVerifier(t, cfg)
			cfg.Clients[0].ClientID = "mutated-after-construction"
			claims := accessClaims()
			delete(claims, "sub") // Client-only identities are intentional.
			if tc.present {
				claims["aud"] = tc.aud
			} else {
				delete(claims, "aud")
			}
			caller := verifyResult(t, v, t.Context(), signedToken(t, key, "access-key=", claims), tc.want)
			if tc.want != nil && calls.Load() != 0 {
				t.Fatal("disallowed audience caused I/O")
			}
			if tc.want == nil {
				jwt, _ := caller.JWT()
				value, _ := jwt.Claims().Lookup("precise")
				number, _ := value.NumberText()
				if number != "9007199254740993" {
					t.Fatalf("number fidelity lost: %q", number)
				}
			}
		})
	}
	v := cognitoVerifier(t, cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) { return jwksResponse(body), nil })))
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"iss", "https://elsewhere.example/pool"},
		{"iss", nil},
		{"client_id", "other"},
		{"client_id", nil},
		{"token_use", "id"},
		{"token_use", nil},
		{"sub", ""},
		{"sub", 42},
		{"scope", "read\twrite"},
		{"cognito:groups", "operators"},
		{"scp", []string{"inconsistent"}},
	} {
		claims := accessClaims()
		claims[tc.name] = tc.value
		verifyResult(t, v, t.Context(), signedToken(t, key, "access-key=", claims), authn.ErrInvalidCredentials)
	}
}

func TestCognitoTokenSyntaxAndSignature(t *testing.T) {
	key := signingKey(t)
	var calls atomic.Int64
	v := cognitoVerifier(t, cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return jwksResponse(keySet(t, publicJWK(key, "key"))), nil
	})))
	payload := encodeJSON(t, accessClaims())
	for _, header := range []string{
		`null`,
		`[]`,
		`{"alg":"RS256","kid":"key","alg":"RS256"}`,
		`{"alg":"none","kid":"key"}`,
		`{"alg":"HS256","kid":"key"}`,
		`{"alg":"RS384","kid":"key"}`,
		`{"alg":"RS256","kid":""}`,
		`{"alg":"RS256","kid":"key","typ":null}`,
		`{"alg":"RS256","kid":"key","typ":"at+jwt"}`,
		`{"alg":"RS256","kid":"key","extension":{"x":1,"x":2}}`,
		`{"alg":"RS256","kid":"key"} {}`,
		`{"alg":"RS256","kid":"key","x":"` + string([]byte{0xff}) + `"}`,
	} {
		verifyResult(t, v, t.Context(), signRawToken(t, key, []byte(header), payload), authn.ErrInvalidCredentials)
	}
	for _, extension := range []string{"crit", "b64", "jku", "jwk", "x5u", "x5c", "zip", "cty"} {
		header := map[string]any{"alg": "RS256", "kid": "key", extension: nil}
		verifyResult(t, v, t.Context(), signRawToken(t, key, encodeJSON(t, header), payload), authn.ErrInvalidCredentials)
	}
	valid := signedToken(t, key, "key", accessClaims())
	parts := strings.Split(valid, ".")
	for _, token := range []string{"", ".", "..", valid + ".x", " " + valid, parts[0] + "=." + parts[1] + "." + parts[2], parts[0] + "." + parts[1] + "." + parts[2] + "\n", strings.Join(parts[:2], ".") + ".AA"} {
		verifyResult(t, v, t.Context(), token, authn.ErrInvalidCredentials)
	}
	for _, badPayload := range []string{`null`, `[]`, string(payload) + `{}`, `{"iss":"x","iss":"x"}`, `{"x":"\ud800"}`} {
		verifyResult(t, v, t.Context(), signRawToken(t, key, []byte(`{"alg":"RS256","kid":"key"}`), []byte(badPayload)), authn.ErrInvalidCredentials)
	}
	if calls.Load() != 0 {
		t.Fatal("malformed or disallowed token caused I/O")
	}
	verifyResult(t, v, t.Context(), valid, nil)
	// Alter a signed permission claim while retaining the original signature.
	changed := strings.Replace(string(payload), "orders.read", "orders.root", 1)
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(changed)) + "." + parts[2]
	verifyResult(t, v, t.Context(), tampered, authn.ErrInvalidCredentials)
	verifyResult(t, v, t.Context(), signedToken(t, signingKey(t), "key", accessClaims()), authn.ErrInvalidCredentials)
	if calls.Load() != 1 {
		t.Fatal("bad signature triggered a key refresh")
	}
	// Whitespace and member ordering are signed bytes, not a canonical JSON object.
	spaced := signRawToken(t, key, []byte("{ \"kid\" : \"key\", \"alg\": \"RS256\", \"typ\": \"JWT\" }"), append([]byte(" \n"), payload...))
	verifyResult(t, v, t.Context(), spaced, nil)
	for _, size := range []int{4096, 4097} {
		header := `{"alg":"RS256","kid":"key","padding":""}`
		header = strings.Replace(header, `"padding":""`, `"padding":"`+strings.Repeat("x", size-len(header))+`"`, 1)
		want := error(nil)
		if size > 4096 {
			want = authn.ErrInvalidCredentials
		}
		verifyResult(t, v, t.Context(), signRawToken(t, key, []byte(header), payload), want)
	}
	for _, levels := range []int{63, 64} {
		header := `{"alg":"RS256","kid":"key","extension":` + strings.Repeat("[", levels) + "0" + strings.Repeat("]", levels) + `}`
		want := error(nil)
		if levels == 64 { // The enclosing header object is also a container.
			want = authn.ErrInvalidCredentials
		}
		verifyResult(t, v, t.Context(), signRawToken(t, key, []byte(header), payload), want)
	}
}

func TestCognitoDatesAndBounds(t *testing.T) {
	key := signingKey(t)
	synctest.Test(t, func(t *testing.T) {
		body := keySet(t, publicJWK(key, "key"))
		cfg := cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) { return jwksResponse(body), nil }))
		v := cognitoVerifier(t, cfg)
		now := time.Now().Unix()
		for _, tc := range []struct {
			name  string
			value any
		}{
			{"exp", now},
			{"exp", nil},
			{"exp", "9999999999"},
			{"exp", jsontext.Value("1e10")},
			{"exp", jsontext.Value("9999999999.0")},
			{"exp", jsontext.Value("253402300800")},
			{"exp", jsontext.Value("999999999999999999999999")},
			{"iat", -1},
			{"iat", now + 1},
			{"iat", nil},
			{"nbf", now + 1},
			{"nbf", nil},
			{"nbf", now + 3600},
		} {
			claims := accessClaims()
			claims[tc.name] = tc.value
			verifyResult(t, v, t.Context(), signedToken(t, key, "key", claims), authn.ErrInvalidCredentials)
		}
		for _, name := range []string{"exp", "iat"} {
			claims := accessClaims()
			delete(claims, name)
			verifyResult(t, v, t.Context(), signedToken(t, key, "key", claims), authn.ErrInvalidCredentials)
		}
		claims := accessClaims()
		claims["nbf"] = now
		valid := signedToken(t, key, "key", claims)
		verifyResult(t, v, t.Context(), valid, nil)
		cfg.ClockSkew = time.Second
		leeway := cognitoVerifier(t, cfg)
		claims["iat"] = now + 1
		claims["nbf"] = now + 1
		verifyResult(t, leeway, t.Context(), signedToken(t, key, "key", claims), nil)
		claims["iat"] = now - 2
		claims["nbf"] = now - 2
		claims["exp"] = now - 1
		verifyResult(t, leeway, t.Context(), signedToken(t, key, "key", claims), authn.ErrInvalidCredentials)
		claims["exp"] = now
		verifyResult(t, leeway, t.Context(), signedToken(t, key, "key", claims), nil)
		for _, limit := range []int{len(valid) - 1, len(valid)} {
			cfg.MaxTokenBytes = limit
			want := error(nil)
			if limit < len(valid) {
				want = authn.ErrInvalidCredentials
			}
			verifyResult(t, cognitoVerifier(t, cfg), t.Context(), valid, want)
		}
		cfg.ClaimsBudget = 1
		verifyResult(t, cognitoVerifier(t, cfg), t.Context(), valid, authn.ErrInvalidCredentials)
		cfg.ClaimsBudget = 0
		cfg.MaxTokenBytes = 0
		cfg.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
			time.Sleep(2 * time.Second)
			return jwksResponse(body), nil
		})
		cfg.ClockSkew = 0
		claims["exp"] = now + 1
		verifyResult(t, cognitoVerifier(t, cfg), t.Context(), signedToken(t, key, "key", claims), authn.ErrInvalidCredentials)
	})
}
