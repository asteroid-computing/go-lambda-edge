package orders

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/asteroid-computing/go-lambda-edge/authn"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// FromEnvironment configures real verification and a small, explicitly enrolled
// read-only demonstration store. It never obtains server-side AWS credentials.
func FromEnvironment(streaming bool) (http.Handler, error) {
	values := make(map[string]string)
	for _, name := range []string{"COGNITO_ISSUER", "COGNITO_CLIENT_ID", "COGNITO_AUDIENCE", "IAM_PROOF_REGION", "IAM_PROOF_AUDIENCE", "ORDER_JWT_SUBJECT", "ORDER_IAM_PRINCIPAL", "CORS_ORIGIN"} {
		values[name] = os.Getenv(name)
		if values[name] == "" {
			return nil, fmt.Errorf("orders: missing %s", name)
		}
	}
	origin, err := url.Parse(values["CORS_ORIGIN"])
	if err != nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || (origin.Scheme != "https" && origin.Scheme != "http") {
		return nil, errors.New("orders: invalid CORS_ORIGIN")
	}
	return New(Config{
		Cognito: authn.CognitoConfig{
			Issuer:  values["COGNITO_ISSUER"],
			Clients: []authn.CognitoClient{{ClientID: values["COGNITO_CLIENT_ID"], Audience: values["COGNITO_AUDIENCE"]}},
		},
		Region: values["IAM_PROOF_REGION"], ProofAudience: values["IAM_PROOF_AUDIENCE"],
		IAMPrincipal: values["ORDER_IAM_PRINCIPAL"], Origin: values["CORS_ORIGIN"],
		Grants: demoGrants{issuer: values["COGNITO_ISSUER"], subject: values["ORDER_JWT_SUBJECT"], principal: values["ORDER_IAM_PRINCIPAL"]},
		Store:  demoStore{}, Streaming: streaming,
	})
}

// These application grants are server configuration, never client claims. This
// example enrolls one exact JWT subject and one full IAM caller ARN. It does not
// infer IAM role paths, role lifecycle identity or a human behind a session.
type demoGrants struct {
	issuer    string
	subject   string
	principal string
}

func (g demoGrants) Allowed(ctx context.Context, caller identity.Caller, action, resource string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if resource != "/tenants/42/orders/7" || (action != "orders.read" && action != "orders.watch") {
		return false, nil
	}
	if jwt, ok := caller.JWT(); ok {
		subject, present := jwt.Subject()
		return present && jwt.Issuer() == g.issuer && subject == g.subject, nil
	}
	if iam, ok := caller.IAM(); ok {
		return iam.PrincipalARN() == g.principal, nil
	}
	return false, nil
}

type demoStore struct{}

func (demoStore) Read(ctx context.Context, resource string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if resource != "/tenants/42/orders/7" {
		return "", errors.New("orders: unavailable resource")
	}
	return "shipped", nil
}

func (s demoStore) Events(ctx context.Context, resource string, yield func(string) error) error {
	state, err := s.Read(ctx, resource)
	if err != nil {
		return err
	}
	for _, update := range []string{"accepted", state} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := yield(update); err != nil {
			return err
		}
	}
	return nil
}
