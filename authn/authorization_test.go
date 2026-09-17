package authn_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/authn"
	"github.com/asteroid-computing/go-lambda-edge/authz"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// Exercise actual token verification and STS-proof parsing before application
// policy. Only JWKS/STS network responses and the grant service are synthetic.
func TestAuthorizationAcrossTransports(t *testing.T) {
	key := signingKey(t)
	keys := keySet(t, publicJWK(key, "authorization"))
	proofVerifier, proof, _ := localProofVerifier(t)
	verifier := cognitoVerifier(t, cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) {
		return jwksResponse(keys), nil
	})))
	a := authenticator(t, authn.Config{Bearer: verifier.Verify, IAMProof: mapProofErrors(proofVerifier)})
	selector, err := edge.NewActionHeader("Action")
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := authz.JWTScopes(cognitoIssuer, "orders.read")
	if err != nil {
		t.Fatal(err)
	}
	groups, err := authz.CognitoGroups(cognitoIssuer, "operators")
	if err != nil {
		t.Fatal(err)
	}
	jwtPath, err := authz.All(scopes, groups)
	if err != nil {
		t.Fatal(err)
	}
	iamPath, err := authz.IAMPrincipal("arn:aws:iam::123456789012:user/Alice")
	if err != nil {
		t.Fatal(err)
	}
	permission, err := authz.Any(jwtPath, iamPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, scheme, action, grant string
		scope, group                bool
		status, lookups             int
	}{
		{"jwt_allowed", "Bearer", "orders.read", "allow", true, true, 200, 1},
		{"iam_allowed", "EdgeIAM", "orders.read", "allow", false, false, 200, 1},
		{"scope_denied", "Bearer", "orders.read", "allow", false, true, 403, 0},
		{"group_denied", "Bearer", "orders.read", "allow", true, false, 403, 0},
		{"unregistered", "Bearer", "orders.read", "unregistered", true, true, 403, 1},
		{"suspended", "EdgeIAM", "orders.read", "suspended", false, false, 403, 1},
		{"wrong_resource", "Bearer", "orders.other", "allow", true, true, 403, 1},
		{"grant_outage", "Bearer", "orders.read", "outage", true, true, 503, 1},
		{"iam_grant_outage", "EdgeIAM", "orders.read", "outage", false, false, 503, 1},
		{"unknown_action", "Bearer", "orders.unknown", "allow", true, true, 404, 0},
		{"malformed_action", "Bearer", "orders.read,orders.delete", "allow", true, true, 400, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := accessClaims()
			if !tc.scope {
				delete(claims, "scope")
			}
			if !tc.group {
				delete(claims, "cognito:groups")
			}
			token := signedToken(t, key, "authorization", claims)
			if tc.scheme == "EdgeIAM" {
				token = proof.Value()
			}
			for _, format := range []string{"native", "typed_v1", "typed_v2", "raw_rest", "raw_http_v1", "raw_http_v2"} {
				t.Run(format, func(t *testing.T) {
					var lookups, executions atomic.Int64
					grant, err := authz.Check(func(ctx context.Context, req authz.Request) (bool, error) {
						if err := ctx.Err(); err != nil {
							return false, err
						}
						lookups.Add(1)
						if req.Action != tc.action {
							t.Errorf("grant checked action %q", req.Action)
						}
						if j, ok := req.Caller.JWT(); ok {
							if _, present := j.Claims().Lookup("grants"); present {
								t.Error("application grants injected into token facts")
							}
							if req.Caller.Source() != identity.SourceLocallyVerifiedToken {
								t.Error("JWT was not verified")
							}
						} else if req.Caller.Source() != identity.SourceVerifiedIAMProof {
							t.Error("IAM proof was not verified")
						}
						if tc.grant == "outage" {
							return true, errors.New("private tenant and grant database details")
						}
						return tc.grant == "allow" && req.Resource == "tenant/42/order/7", nil
					})
					if err != nil {
						t.Fatal(err)
					}
					policy, err := authz.All(permission, grant)
					if err != nil {
						t.Fatal(err)
					}
					type entry struct {
						rule     authz.Rule
						resource string
						execute  func(http.ResponseWriter, *http.Request, authz.Request)
					}
					execute := func(w http.ResponseWriter, _ *http.Request, facts authz.Request) {
						executions.Add(1)
						if facts.Action != "orders.read" || facts.Resource != "tenant/42/order/7" {
							t.Error("executed a different action or resource")
						}
						w.Header().Set("Content-Type", "application/json")
						if err := json.MarshalWrite(w, map[string]string{"action": facts.Action, "resource": facts.Resource}); err != nil {
							t.Error(err)
						}
					}
					registry := map[string]entry{
						"orders.read":  {policy, "tenant/42/order/7", execute},
						"orders.other": {policy, "tenant/99/order/8", execute},
					}
					dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						action, err := selector.Parse(r.Header)
						if err != nil {
							authorizationResponse(t, w, 400, "invalid_action")
							return
						}
						selected, ok := registry[action]
						if !ok {
							authorizationResponse(t, w, 404, "unknown_action")
							return
						}
						facts := authz.Request{Caller: identity.FromContext(r.Context()), Action: action, Resource: selected.resource}
						// Downstream header changes must not change authorization or execution.
						r.Header.Set("Action", "orders.delete")
						r.Header.Set("Resource", "tenant/99/order/8")
						if err := selected.rule.Authorize(r.Context(), facts); err != nil {
							switch {
							case errors.Is(err, authz.ErrDenied):
								// This route supports Bearer; no claim about the denial's cause.
								w.Header().Set("WWW-Authenticate", `Bearer realm="edge"`)
								authorizationResponse(t, w, 403, "forbidden")
							case errors.Is(err, authz.ErrUnavailable):
								authorizationResponse(t, w, 503, "unavailable")
							default:
								authorizationResponse(t, w, 500, "internal_error")
							}
							return
						}
						selected.execute(w, r, facts)
					})
					h := handler(t, a, dispatch)
					withCORS := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Access-Control-Allow-Origin", "https://app.example")
						h.ServeHTTP(w, r)
					})
					status, headers, body := serveFormatBody(t, withCORS, format, []string{tc.scheme + " " + token}, tc.action)
					if status != tc.status || lookups.Load() != int64(tc.lookups) {
						t.Fatalf("status=%d lookups=%d; want %d %d", status, lookups.Load(), tc.status, tc.lookups)
					}
					wantExecutions := int64(0)
					if status == 200 {
						wantExecutions = 1
					}
					if executions.Load() != wantExecutions {
						t.Errorf("executions=%d; want %d", executions.Load(), wantExecutions)
					}
					challenge := ""
					if status == 403 {
						challenge = `Bearer realm="edge"`
					}
					if headers.Get("WWW-Authenticate") != challenge || headers.Get("Access-Control-Allow-Origin") != "https://app.example" {
						t.Errorf("incorrect challenge/CORS: %v", headers)
					}
					if headers.Get("Content-Type") != "application/json" || status != 200 && headers.Get("Cache-Control") != "no-store" {
						t.Errorf("unsafe response headers: %v", headers)
					}
					var envelope map[string]string
					if err := json.Unmarshal(body, &envelope); err != nil {
						t.Fatal(err)
					}
					if status != 200 && (len(envelope) != 1 || envelope["error"] == "" || strings.Contains(string(body), "private")) {
						t.Errorf("unsafe error envelope: %s", body)
					}
				})
			}
		})
	}
}

func authorizationResponse(t *testing.T, w http.ResponseWriter, status int, code string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, map[string]string{"error": code}); err != nil {
		t.Error(err)
	}
}
