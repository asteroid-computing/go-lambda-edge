package authn_test

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/authn"
)

func TestCognitoJWKSValidation(t *testing.T) {
	key := signingKey(t)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing_kid", func(k map[string]any) { delete(k, "kid") }},
		{"empty_kid", func(k map[string]any) { k["kid"] = "" }},
		{"large_kid", func(k map[string]any) { k["kid"] = strings.Repeat("x", 257) }},
		{"null_alg", func(k map[string]any) { k["alg"] = nil }},
		{"null_use", func(k map[string]any) { k["use"] = nil }},
		{"missing_n", func(k map[string]any) { delete(k, "n") }},
		{"padded_n", func(k map[string]any) { k["n"] = k["n"].(string) + "=" }},
		{"newline_n", func(k map[string]any) { k["n"] = k["n"].(string) + "\n" }},
		{"leading_zero_n", func(k map[string]any) {
			k["n"] = base64.RawURLEncoding.EncodeToString(append([]byte{0}, key.N.Bytes()...))
		}},
		{"small_n", func(k map[string]any) { k["n"] = "AQ" }},
		{"even_n", func(k map[string]any) {
			b := key.N.Bytes()
			b[len(b)-1] &^= 1
			k["n"] = base64.RawURLEncoding.EncodeToString(b)
		}},
		{"oversize_n", func(k map[string]any) {
			k["n"] = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 513)))
		}},
		{"small_e", func(k map[string]any) { k["e"] = "AQ" }},
		{"even_e", func(k map[string]any) { k["e"] = "BA" }},
		{"leading_zero_e", func(k map[string]any) { k["e"] = "AAEAAQ" }},
		{"large_e", func(k map[string]any) { k["e"] = "_____w" }},
		{"noncanonical_e", func(k map[string]any) { k["e"] = "Ax" }},
		{"private", func(k map[string]any) { k["d"] = nil }},
		{"null_operations", func(k map[string]any) { k["key_ops"] = nil }},
		{"signing_operations", func(k map[string]any) { k["key_ops"] = []string{"sign", "verify"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jwk := publicJWK(key, "key")
			tc.mutate(jwk)
			// A good key must not hide a malformed eligible key in the same set.
			body := keySet(t, publicJWK(key, "good"), jwk)
			v := cognitoVerifier(t, cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) { return jwksResponse(body), nil })))
			if err := v.Warm(t.Context()); !errors.Is(err, authn.ErrUnavailable) {
				t.Fatalf("malformed JWKS returned %v", err)
			}
		})
	}
	good := publicJWK(key, "key")
	goodWire := keySet(t, good)
	for _, body := range []string{
		`null`, `[]`, `{}`, `{"keys":null}`, `{"keys":[]}`, `{"keys":[null]}`, `{"keys":[{}]}`,
		keySet(t, good, good), keySet(t, good, map[string]any{"kty": "EC", "kid": "key"}),
		keySet(t, good) + `{}`, `{"keys":[],"keys":[]}`, `{"keys":[],"ignored":{"x":1,"x":2}}`,
		`{"keys":[],"ignored":` + strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64) + `}`,
		goodWire[:len(goodWire)-1] + `,"ignored":{"x":1,"x":2}}`,
		goodWire[:len(goodWire)-1] + `,"ignored":` + strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64) + `}`,
	} {
		v := cognitoVerifier(t, cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) { return jwksResponse(body), nil })))
		if err := v.Warm(t.Context()); !errors.Is(err, authn.ErrUnavailable) {
			t.Fatalf("invalid key set accepted: %v", err)
		}
	}
	// Unsupported keys do not participate in signature verification. Optional
	// metadata may be absent; an opaque kid can contain '=' or URL-like text.
	minimal := publicJWK(key, "https://untrusted.invalid/key=")
	delete(minimal, "use")
	delete(minimal, "alg")
	minimal["key_ops"] = []string{"verify"}
	body := keySet(t, minimal, map[string]any{"kty": "EC", "kid": "other"}, map[string]any{"kty": "RSA", "kid": "encryption", "use": "enc"})
	v := cognitoVerifier(t, cognitoConfig(transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != cognitoIssuer+"/.well-known/jwks.json" {
			t.Error("kid became a network destination")
		}
		return jwksResponse(body), nil
	})))
	verifyResult(t, v, t.Context(), signedToken(t, key, minimal["kid"].(string), accessClaims()), nil)
	for _, count := range []int{32, 33} {
		var entries []map[string]any
		for i := range count {
			entries = append(entries, publicJWK(key, fmt.Sprint(i)))
		}
		body := keySet(t, entries...)
		v := cognitoVerifier(t, cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) { return jwksResponse(body), nil })))
		want := error(nil)
		if count > 32 {
			want = authn.ErrUnavailable
		}
		if err := v.Warm(t.Context()); !errors.Is(err, want) {
			t.Fatalf("%d keys: error=%v; want %v", count, err, want)
		}
	}
}

func FuzzCognitoToken(f *testing.F) {
	valid, body := cognitoFuzzFixture(f)
	v := cognitoVerifier(f, cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) { return jwksResponse(body), nil })))
	if err := v.Warm(f.Context()); err != nil {
		f.Fatal(err)
	}
	verifyResult(f, v, f.Context(), valid, nil)
	f.Add(valid)
	f.Add("a.b.c")
	f.Add("..")
	f.Fuzz(func(t *testing.T, token string) {
		caller, err := v.Verify(t.Context(), token)
		if err == nil && token != valid {
			t.Fatal("altered input authenticated without a matching signature")
		}
		if err != nil && !errors.Is(err, authn.ErrInvalidCredentials) && !errors.Is(err, authn.ErrUnavailable) {
			t.Fatalf("unexpected error category: %v", err)
		}
		if err != nil && caller.Kind() != 0 {
			t.Fatal("invalid token returned an identity")
		}
	})
}

func FuzzCognitoJWKS(f *testing.F) {
	_, body := cognitoFuzzFixture(f)
	f.Add(body)
	f.Add(`{"keys":[{"kid":"k","kty":"RSA","n":"AA","e":"AQAB"}]}`)
	f.Add(`{"keys":[]}`)
	f.Fuzz(func(t *testing.T, body string) {
		v := cognitoVerifier(t, cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) { return jwksResponse(body), nil })))
		if err := v.Warm(t.Context()); err != nil && !errors.Is(err, authn.ErrUnavailable) {
			t.Fatalf("unexpected key-set error: %v", err)
		}
	})
}

func cognitoFuzzFixture(t testing.TB) (string, string) {
	t.Helper()
	data, err := os.ReadFile("testdata/cognito-fuzz.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Token string         `json:"token"`
		JWKS  jsontext.Value `json:"jwks"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture.Token, string(fixture.JWKS)
}
