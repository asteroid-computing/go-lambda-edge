package authn_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/authn"
	"github.com/asteroid-computing/go-lambda-edge/iamproof"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func ExampleAuthenticator_Handler() {
	proofVerifier, err := iamproof.NewVerifier("eu-west-2", "orders.production")
	if err != nil {
		fmt.Println(err)
		return
	}
	verifyIAM := func(ctx context.Context, token string) (identity.Caller, error) {
		caller, err := proofVerifier.Verify(ctx, token)
		if errors.Is(err, iamproof.ErrInvalidProof) {
			return identity.Caller{}, authn.ErrInvalidCredentials
		}
		return caller, err
	}
	// Synthetic fixture only.
	// Replace this function with your actual issuer/key/signature/expiry/client-policy verifier.
	// Parsing claims is NOT verification.
	verifyBearer := func(_ context.Context, token string) (identity.Caller, error) {
		if token != "synthetic-example-token" {
			return identity.Caller{}, authn.ErrInvalidCredentials
		}
		claims, err := identity.NewClaims(map[string]any{"iss": "https://issuer.example", "sub": "alice"})
		if err != nil {
			return identity.Caller{}, err
		}
		return identity.NewJWT(claims, identity.SourceLocallyVerifiedToken)
	}
	a, err := authn.New(authn.Config{Bearer: verifyBearer, IAMProof: verifyIAM, Realm: "orders"})
	if err != nil {
		fmt.Println(err)
		return
	}
	selector, err := edge.NewActionHeader("Action")
	if err != nil {
		fmt.Println(err)
		return
	}
	dispatcher := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action, err := selector.Parse(r.Header)
		if err != nil {
			http.Error(w, "invalid action", http.StatusBadRequest)
			return
		}
		// Consumer-owned authorization: deny by default.
		// Neither successful token verification nor AWS account membership grants this action alone.
		caller := identity.FromContext(r.Context())
		allowed := false
		if jwt, ok := caller.JWT(); ok {
			sub, present := jwt.Subject()
			allowed = present && jwt.Issuer() == "https://issuer.example" && sub == "alice"
		}
		if iam, ok := caller.IAM(); ok {
			allowed = iam.PrincipalARN() == "arn:aws:iam::123456789012:user/Alice"
		}
		if !allowed || action != "orders.read" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// Execute the exact selection just authorized.
		// Extra consumer headers remain available through r.Header;
		// authn does not interpret them.
		w.WriteHeader(http.StatusNoContent)
	})
	protected, err := a.Handler(dispatcher)
	if err != nil {
		fmt.Println(err)
		return
	}
	// protected also goes directly to edge.New for Lambda registration.
	// This local example exercises only the synthetic Bearer path;
	// no AWS calls.
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer synthetic-example-token")
	r.Header.Set("Action", "orders.read")
	w := httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	fmt.Println(w.Code)
	// The malformed proof is rejected before any STS call, using the mapping.
	r.Header.Set("Authorization", "EdgeIAM v2.invalid")
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	fmt.Println(w.Code)
	fmt.Println(w.Header().Get("WWW-Authenticate"))
	// Output:
	// 204
	// 401
	// EdgeIAM realm="orders"
}

func ExampleAuthenticator_Authenticate() {
	// This example demonstrates a custom JSON error envelope.
	// Its verifier rejects all credentials and is never called for the missing-header case.
	a, err := authn.New(authn.Config{Bearer: func(context.Context, string) (identity.Caller, error) {
		return identity.Caller{}, authn.ErrInvalidCredentials
	}})
	if err != nil {
		fmt.Println(err)
		return
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, err := a.Authenticate(r.Context(), r.Header)
		if err != nil {
			status := http.StatusInternalServerError
			if failure, ok := errors.AsType[*authn.Error](err); ok {
				status = failure.StatusCode()
				for _, challenge := range failure.Challenges() {
					w.Header().Add("WWW-Authenticate", challenge)
				}
			} else if r.Context().Err() != nil {
				status = http.StatusServiceUnavailable
			}
			body, encodeErr := json.Marshal(struct {
				Error string `json:"error"`
			}{Error: http.StatusText(status)})
			if encodeErr != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			if _, err := w.Write(body); err != nil {
				return // The transport can no longer deliver this response.
			}
			return
		}
		// A real custom pipeline would install caller, authorize its selected action, then invoke the application.
		// No such success is possible here.
		_ = caller
		http.Error(w, "no action authorized", http.StatusForbidden)
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", nil))
	fmt.Println(w.Code)
	fmt.Println(w.Header().Get("WWW-Authenticate"))
	fmt.Println(w.Body.String())
	// Output:
	// 401
	// Bearer realm="edge"
	// {"error":"Unauthorized"}
}
