package edge

import (
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func TestClaimsInvocationErrors(t *testing.T) {
	_, malformed := identity.ParseClaims(jsontext.Value(`{"secret":`))
	_, exceeded := identity.NewClaims(map[string]any{"secret": "value"}, identity.WithClaimsBudget(64))
	deep := map[string]any{}
	for range 64 {
		deep = map[string]any{"secret": deep}
	}
	_, depth := identity.NewClaims(deep)
	for _, tc := range []struct {
		name  string
		cause error
		limit bool
	}{
		{name: "malformed", cause: malformed},
		{name: "bytes", cause: exceeded, limit: true},
		{name: "depth", cause: depth},
		{name: "unexpected", cause: errors.New("secret")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cause == nil {
				t.Fatal("fixture unexpectedly accepted")
			}
			err := claimsError(tc.cause)
			if !errors.Is(err, ErrIdentity) || errors.Is(err, ErrLimitExceeded) != tc.limit || err.Operation() != "identity" {
				t.Errorf("claimsError(%s) category or operation incorrect: %v", tc.name, err)
			}
			name, maximum, ok := err.Limit()
			if ok != tc.limit || tc.limit && (name != "identity_claims" || maximum != 64) {
				t.Errorf("claimsError(%s).Limit() = %q, %d, %t", tc.name, name, maximum, ok)
			}
			if tc.name == "depth" && !errors.Is(err, identity.ErrInvalidClaims) {
				t.Error("depth failure lost its structural classification")
			}
			var visit func(error)
			visit = func(cause error) {
				if cause == nil {
					return
				}
				if strings.Contains(cause.Error(), "secret") {
					t.Errorf("error tree exposed input: %v", cause)
				}
				if joined, ok := cause.(interface{ Unwrap() []error }); ok {
					for _, child := range joined.Unwrap() {
						visit(child)
					}
					return
				}
				visit(errors.Unwrap(cause))
			}
			visit(err)
		})
	}
}
