package identity_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func ExampleNewJWT() {
	// Synthetic facts for this example. In an application, an authenticating
	// producer must establish these facts before constructing the caller.
	claims, err := identity.NewClaims(map[string]any{
		"iss": "https://issuer.example", "client_id": "batch-worker", "scope": "orders.read",
	})
	if err != nil {
		panic(err)
	}
	caller, err := identity.NewJWT(claims, identity.SourceCustomAssertion)
	if err != nil {
		panic(err)
	}
	jwt, _ := caller.JWT()
	client, _ := jwt.ClientID()
	_, hasSubject := jwt.Subject()
	scopes, known := jwt.Scopes()
	fmt.Println(client, hasSubject, scopes, known)
	// Output: batch-worker false [orders.read] true
}

func ExampleWithCaller() {
	// This validates a synthetic identifier; it does not authenticate it.
	caller, err := identity.NewIAM("arn:aws:sts::123456789012:assumed-role/Worker/batch-1", identity.SourceCustomAssertion)
	if err != nil {
		panic(err)
	}
	ctx, err := identity.WithCaller(context.Background(), caller)
	if err != nil {
		panic(err)
	}
	iam, _ := identity.FromContext(ctx).IAM()
	fmt.Println(iam.PrincipalARN())
	_, err = identity.WithCaller(ctx, caller)
	fmt.Println(errors.Is(err, identity.ErrConflict))
	// Output:
	// arn:aws:sts::123456789012:assumed-role/Worker/batch-1
	// true
}
