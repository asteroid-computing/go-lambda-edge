// Package orders is application code for the dispatcher example, not an edge API.
package orders

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/asteroid-computing/go-lambda-edge/actionheader"
	"github.com/asteroid-computing/go-lambda-edge/authn"
	"github.com/asteroid-computing/go-lambda-edge/authz"
	"github.com/asteroid-computing/go-lambda-edge/iamproof"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// Grants resolves application enrollment and permissions, independently of token scopes and AWS IAM policies.
// Implementations must support concurrent requests.
type Grants interface {
	Allowed(context.Context, identity.Caller, string, string) (bool, error)
}

// Store reads the exact authorized resource.
// Events calls yield synchronously, stops on its first error, and observes context cancellation.
// A database-backed application must also preserve object/version consistency through execution.
type Store interface {
	Read(context.Context, string) (string, error)
	Events(context.Context, string, func(string) error) error
}

// Config belongs to this example.
// Required strings have no implicit defaults.
// Transports are trusted infrastructure;
// nil selects the verifier's default.
type Config struct {
	Cognito       authn.CognitoConfig
	Region        string
	ProofAudience string
	IAMPrincipal  string
	Origin        string
	STSTransport  http.RoundTripper
	Grants        Grants
	Store         Store
	Streaming     bool
}

// New constructs the complete application without network I/O.
// Streaming is a startup choice: orders.watch is absent from the buffered variant's registry.
func New(cfg Config) (http.Handler, error) {
	if cfg.Grants == nil || cfg.Store == nil || cfg.Origin == "" {
		return nil, errors.New("orders: missing application configuration")
	}
	bearer, err := authn.NewCognitoVerifier(cfg.Cognito)
	if err != nil {
		return nil, err
	}
	var proofOptions []iamproof.VerifierOption
	if cfg.STSTransport != nil {
		proofOptions = append(proofOptions, iamproof.WithTransport(cfg.STSTransport))
	}
	proof, err := iamproof.NewVerifier(cfg.Region, cfg.ProofAudience, proofOptions...)
	if err != nil {
		return nil, err
	}
	authentication, err := authn.New(authn.Config{
		Realm:  "orders",
		Bearer: bearer.Verify,
		IAMProof: func(ctx context.Context, credential string) (identity.Caller, error) {
			caller, err := proof.Verify(ctx, credential)
			if errors.Is(err, iamproof.ErrInvalidProof) {
				return identity.Caller{}, authn.ErrInvalidCredentials
			}
			return caller, err
		},
	})
	if err != nil {
		return nil, err
	}
	selector, err := actionheader.NewSelector("Action")
	if err != nil {
		return nil, err
	}
	jwtPermission, err := authz.JWTScopes(cfg.Cognito.Issuer, "orders.read")
	if err != nil {
		return nil, err
	}
	iamPermission, err := authz.IAMPrincipal(cfg.IAMPrincipal)
	if err != nil {
		return nil, err
	}
	permission, err := authz.Any(jwtPermission, iamPermission)
	if err != nil {
		return nil, err
	}
	grant, err := authz.Check(func(ctx context.Context, req authz.Request) (bool, error) {
		return cfg.Grants.Allowed(ctx, req.Caller, req.Action, req.Resource)
	})
	if err != nil {
		return nil, err
	}
	policy, err := authz.All(permission, grant)
	if err != nil {
		return nil, err
	}
	type entry struct {
		policy  authz.Rule
		execute func(http.ResponseWriter, *http.Request, string)
	}
	registry := map[string]entry{
		"orders.read": {policy: policy, execute: func(w http.ResponseWriter, r *http.Request, resource string) {
			state, err := cfg.Store.Read(r.Context(), resource)
			if err != nil {
				writeError(w, 503, "unavailable")
				return
			}
			writeJSON(w, 200, map[string]string{"resource": resource, "state": state})
		}},
	}
	if cfg.Streaming {
		registry["orders.watch"] = entry{policy: policy, execute: func(w http.ResponseWriter, r *http.Request, resource string) {
			watch(w, r, cfg.Store, resource)
		}}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This example owns CORS.
		// Disable conflicting Gateway-managed CORS when deploying it, or move this policy wholly into that deployment layer.
		w.Header().Add("Vary", "Origin")
		if origins := r.Header.Values("Origin"); len(origins) == 1 && origins[0] == cfg.Origin {
			w.Header().Set("Access-Control-Allow-Origin", cfg.Origin)
			w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate")
		}
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			writeJSON(w, 200, map[string]string{"status": "ok"})
			return
		}
		resource, ok := resourcePath(r.URL.Path)
		if !ok {
			writeError(w, 404, "not_found")
			return
		}
		if r.Method == http.MethodOptions {
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			if w.Header().Get("Access-Control-Allow-Origin") == "" || r.Header.Get("Access-Control-Request-Method") != http.MethodGet {
				writeError(w, 403, "forbidden")
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Action, Custom-Trace")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, OPTIONS")
			writeError(w, 405, "method_not_allowed")
			return
		}
		caller, err := authentication.Authenticate(r.Context(), r.Header)
		if err != nil {
			writeAuthenticationError(w, err)
			return
		}
		ctx, err := identity.NewContext(r.Context(), caller)
		if err != nil {
			writeError(w, 500, "internal_error")
			return
		}
		r = r.WithContext(ctx)
		action, err := selector.Parse(r.Header)
		if err != nil {
			writeError(w, 400, "invalid_action")
			return
		}
		selected, ok := registry[action]
		if !ok {
			writeError(w, 404, "unknown_action")
			return
		}
		facts := authz.Request{Caller: caller, Action: action, Resource: resource}
		if err := selected.policy.Authorize(ctx, facts); err != nil {
			switch {
			case errors.Is(err, authz.ErrDenied):
				w.Header().Set("WWW-Authenticate", `Bearer realm="orders"`)
				writeError(w, 403, "forbidden")
			case errors.Is(err, authz.ErrUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				writeError(w, 503, "unavailable")
			default:
				writeError(w, 500, "internal_error")
			}
			return
		}
		selected.execute(w, r, resource)
	}), nil
}

// The application accepts canonical decimal IDs only;
// no aliases, escaping or query parameters can change the target between authorization and execution.
func resourcePath(path string) (string, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "tenants" || parts[3] != "orders" {
		return "", false
	}
	for _, id := range []string{parts[2], parts[4]} {
		if len(id) == 0 || len(id) > 18 || id[0] == '0' {
			return "", false
		}
		for _, c := range id {
			if c < '0' || c > '9' {
				return "", false
			}
		}
	}
	return path, true
}

func writeAuthenticationError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if failure, ok := errors.AsType[*authn.Error](err); ok {
		status = failure.StatusCode()
		for _, challenge := range failure.Challenges() {
			w.Header().Add("WWW-Authenticate", challenge)
		}
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusServiceUnavailable
	}
	code := "internal_error"
	switch status {
	case 400, 431:
		code = "invalid_credentials"
	case 401:
		code = "unauthenticated"
	case 503:
		code = "unavailable"
	}
	writeError(w, status, code)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, payload map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	// Status is committed.
	// A client write failure cannot be replaced by JSON.
	_ = json.MarshalWrite(w, payload)
}

func watch(w http.ResponseWriter, r *http.Request, store Store, resource string) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	controller := http.NewResponseController(w)
	// Publish only after authorization;
	// this also makes an empty stream's commitment explicit before entering a potentially slow producer.
	if err := controller.Flush(); err != nil {
		return
	}
	err := store.Events(r.Context(), resource, func(state string) error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		if err := json.MarshalWrite(w, map[string]string{"resource": resource, "state": state}); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
		return controller.Flush()
	})
	if err != nil {
		// net/http's documented abort sentinel prevents a truncated result from appearing complete.
		// Edge converts this post-handoff panic to ErrStream;
		// it never exposes the store's diagnostic.
		// Do not recover it in middleware.
		panic(http.ErrAbortHandler)
	}
}
