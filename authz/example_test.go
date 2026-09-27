package authz_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/asteroid-computing/go-lambda-edge/actionheader"
	"github.com/asteroid-computing/go-lambda-edge/authn"
	"github.com/asteroid-computing/go-lambda-edge/authz"
	"github.com/asteroid-computing/go-lambda-edge/iamproof"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

const orderIssuer = "https://issuer.example/pool"

// grantStore is an application interface, not a universal edge principal model.
// Its implementation chooses the authority and enrollment keys, resolves tenant membership/suspension and checks the exact action/resource in one operation.
type grantStore interface {
	Allowed(context.Context, identity.Caller, string, string) (bool, error)
}

// orderHandler owns an immutable registry.
// This example has one fixed resource;
// a real application validates/resolves its target before calling Authorize and keeps that same object/version for execution, using a transaction if needed.
func orderHandler(authentication *authn.Authenticator, grants grantStore, issuer string) (http.Handler, error) {
	selector, err := actionheader.NewSelector("Action")
	if err != nil {
		return nil, err
	}
	scopes, err := authz.JWTScopes(issuer, "orders.read")
	if err != nil {
		return nil, err
	}
	iam, err := authz.IAMRoleSessions("aws", "123456789012", "OrderReader")
	if err != nil {
		return nil, err
	}
	permission, err := authz.Any(scopes, iam)
	if err != nil {
		return nil, err
	}
	applicationGrant, err := authz.Check(func(ctx context.Context, req authz.Request) (bool, error) {
		if req.Resource == "" {
			return false, nil
		}
		return grants.Allowed(ctx, req.Caller, req.Action, req.Resource)
	})
	if err != nil {
		return nil, err
	}
	policy, err := authz.All(permission, applicationGrant)
	if err != nil {
		return nil, err
	}
	type entry struct {
		policy   authz.Rule
		resource string
		execute  func(http.ResponseWriter, *http.Request, authz.Request)
	}
	registry := map[string]entry{
		"orders.read": {
			policy:   policy,
			resource: "tenant/42/order/7",
			execute: func(w http.ResponseWriter, _ *http.Request, facts authz.Request) {
				w.Header().Set("Content-Type", "application/json")
				// A write failure cannot be replaced after response commitment.
				_ = json.MarshalWrite(w, struct {
					Resource string `json:"resource"`
				}{facts.Resource})
			},
		},
	}
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action, err := selector.Parse(r.Header)
		if err != nil {
			writePolicyError(w, 400, "invalid_action")
			return
		}
		selected, ok := registry[action]
		if !ok {
			writePolicyError(w, 404, "unknown_action")
			return
		}
		facts := authz.Request{Caller: identity.FromContext(r.Context()), Action: action, Resource: selected.resource}
		if err := selected.policy.Authorize(r.Context(), facts); err != nil {
			switch {
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, authz.ErrUnavailable):
				writePolicyError(w, 503, "unavailable")
			case errors.Is(err, authz.ErrDenied):
				// This route accepts Bearer.
				// A generic challenge reveals no policy reason and does not mislabel an IAM or grant denial as a scope error.
				w.Header().Set("WWW-Authenticate", `Bearer realm="orders"`)
				writePolicyError(w, 403, "forbidden")
			case errors.Is(err, authz.ErrUnauthenticated):
				w.Header().Add("WWW-Authenticate", `Bearer realm="orders"`)
				w.Header().Add("WWW-Authenticate", `EdgeIAM realm="orders"`)
				writePolicyError(w, 401, "unauthenticated")
			default:
				writePolicyError(w, 500, "internal_error")
			}
			return
		}
		selected.execute(w, r, facts)
	})
	return authentication.Handler(dispatch)
}

func writePolicyError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, struct {
		Error string `json:"error"`
	}{code})
}

// exampleGrants is a server-owned, read-only fixture.
// A database implementation can return false,nil for suspended/unregistered principals and an error when its dependency cannot decide.
// These grants are never put in identity.Claims.
type exampleGrants struct{}

func (exampleGrants) Allowed(ctx context.Context, caller identity.Caller, action, resource string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if action != "orders.read" || resource != "tenant/42/order/7" {
		return false, nil
	}
	if j, ok := caller.JWT(); ok {
		subject, present := j.Subject()
		return present && j.Issuer() == orderIssuer && subject == "alice", nil
	}
	if i, ok := caller.IAM(); ok {
		// This fixture explicitly enrolls a full session.
		// Applications can use a different reviewed enrollment key;
		// account or session name alone is not one.
		return i.PrincipalARN() == "arn:aws:sts::123456789012:assumed-role/OrderReader/session-one", nil
	}
	return false, nil
}

func ExampleCheck() {
	// These are validated fixture facts, not credential verification.
	// A request handler obtains the caller from its configured authentication producer.
	claims, err := identity.NewClaims(map[string]any{"iss": orderIssuer, "sub": "alice"})
	if err != nil {
		panic(err)
	}
	caller, err := identity.NewJWT(claims, identity.SourceCustomAssertion)
	if err != nil {
		panic(err)
	}
	grants := exampleGrants{}
	policy, err := authz.Check(func(ctx context.Context, req authz.Request) (bool, error) {
		return grants.Allowed(ctx, req.Caller, req.Action, req.Resource)
	})
	if err != nil {
		panic(err)
	}
	request := authz.Request{Caller: caller, Action: "orders.read", Resource: "tenant/42/order/7"}
	fmt.Println(policy.Authorize(context.Background(), request))
	request.Resource = "tenant/99/order/8"
	fmt.Println(errors.Is(policy.Authorize(context.Background(), request), authz.ErrDenied))
	// Output:
	// <nil>
	// true
}

func Example_dispatcher() {
	// Construction performs no network I/O.
	// Configure real pool/client/resource and STS audience values for deployment.
	// The example sends no credentials, so neither verifier contacts AWS when run.
	bearer, err := authn.NewCognitoVerifier(authn.CognitoConfig{
		Issuer:  orderIssuer,
		Clients: []authn.CognitoClient{{ClientID: "orders-client", Audience: "https://orders.example"}},
	})
	if err != nil {
		panic(err)
	}
	proof, err := iamproof.NewVerifier("eu-west-2", "orders.production")
	if err != nil {
		panic(err)
	}
	authentication, err := authn.New(authn.Config{
		Realm:  "orders",
		Bearer: bearer.Verify,
		IAMProof: func(ctx context.Context, token string) (identity.Caller, error) {
			caller, err := proof.Verify(ctx, token)
			if errors.Is(err, iamproof.ErrInvalidProof) {
				return identity.Caller{}, authn.ErrInvalidCredentials
			}
			return caller, err
		},
	})
	if err != nil {
		panic(err)
	}
	h, err := orderHandler(authentication, exampleGrants{}, orderIssuer)
	if err != nil {
		panic(err)
	}
	// h is an ordinary http.Handler.
	// For Lambda, edge.New(h) uses the same dispatcher, with gateway identity left disabled for these local verifiers.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Action", "orders.read")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	fmt.Println(w.Code)
	fmt.Println(w.Header().Values("WWW-Authenticate"))
	// Output:
	// 401
	// [Bearer realm="orders" EdgeIAM realm="orders"]
}
