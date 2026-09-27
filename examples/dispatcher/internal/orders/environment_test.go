package orders

import (
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// Test the actual demonstration enrollment policy separately from cryptographic verification.
// All identities here are explicitly custom-assertion fixtures.
func TestDemoEnrollment(t *testing.T) {
	const issuer = "https://issuer.example/pool"
	const principal = "arn:aws:iam::123456789012:user/Alice"
	grants := demoGrants{issuer: issuer, subject: "alice", principal: principal}
	for _, tc := range []struct {
		name    string
		issuer  string
		subject string
		arn     string
		allowed bool
	}{
		{name: "enrolled_subject", issuer: issuer, subject: "alice", allowed: true},
		{name: "wrong_issuer", issuer: "https://other.example/pool", subject: "alice"},
		{name: "wrong_subject", issuer: issuer, subject: "bob"},
		{name: "missing_subject", issuer: issuer},
		{name: "enrolled_iam", arn: principal, allowed: true},
		{name: "different_iam", arn: "arn:aws:iam::123456789012:user/Bob"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var caller identity.Caller
			var err error
			if tc.arn != "" {
				caller, err = identity.NewIAM(tc.arn, identity.SourceCustomAssertion)
			} else {
				// A subjectless caller still needs a client identity.
				// Enrollment must not silently treat that client as the configured subject.
				claims := map[string]any{"iss": tc.issuer, "client_id": "app"}
				if tc.subject != "" {
					claims["sub"] = tc.subject
				}
				facts, parseErr := identity.NewClaims(claims)
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				caller, err = identity.NewJWT(facts, identity.SourceCustomAssertion)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"orders.read", "orders.watch", "orders.delete"} {
				for _, resource := range []string{"/tenants/42/orders/7", "/tenants/99/orders/7"} {
					allowed, err := grants.Allowed(t.Context(), caller, action, resource)
					want := tc.allowed && action != "orders.delete" && resource == "/tenants/42/orders/7"
					if err != nil || allowed != want {
						t.Errorf("Allowed(%q, %q) = %v, %v; want %v", action, resource, allowed, err, want)
					}
				}
			}
		})
	}
}
