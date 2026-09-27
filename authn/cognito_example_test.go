package authn_test

import (
	"context"
	"fmt"
	"net/http"

	"github.com/asteroid-computing/go-lambda-edge/authn"
)

func ExampleNewCognitoVerifier() {
	verifier, err := authn.NewCognitoVerifier(authn.CognitoConfig{
		Issuer: "https://issuer-cognito-idp.eu-west-2.amazonaws.com/eu-west-2_example",
		Clients: []authn.CognitoClient{
			{ClientID: "user-app-client", Audience: "https://orders.example"},
			// Cognito M2M grants do not provide resource binding.
			// This permits absent aud only;
			// any audience on this client's token is rejected.
			{ClientID: "machine-app-client", AllowUnbound: true},
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	a, err := authn.New(authn.Config{Bearer: verifier.Verify})
	if err != nil {
		fmt.Println(err)
		return
	}
	// Constructors perform no I/O.
	// Verification is lazy;
	// optional Warm(ctx) can fetch keys during application initialization.
	// Reuse the verifier.
	// Missing credentials are rejected without contacting the issuer.
	_, err = a.Authenticate(context.Background(), make(http.Header))
	fmt.Println(err)
	// Output:
	// authn: missing credentials
}
