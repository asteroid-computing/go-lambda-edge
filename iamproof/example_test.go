package iamproof_test

import (
	"context"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/asteroid-computing/go-lambda-edge/iamproof"
)

func ExampleGenerator_Generate() {
	ctx := context.Background()
	// Synthetic credentials keep this example entirely local.
	// In an application, pass cfg.Credentials from your existing AWS SDK configuration instead.
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "synthetic-example-only"}, nil
	})
	generator, err := iamproof.NewGenerator("eu-west-2", "orders.production", provider)
	if err != nil {
		fmt.Println(err)
		return
	}
	token, err := generator.Generate(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://orders.example.com/", nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	req.Header.Set("Authorization", "EdgeIAM "+token.Value())
	req.Header.Set("Action", "ListOrders")
	// Sending req is the application's responsibility.
	// Never log its credential.
	fmt.Println(req.Header.Get("Action"))
	fmt.Println(token) // Default diagnostics redact the proof.
	// Output:
	// ListOrders
	// iamproof.Token(redacted)
}

func ExampleNewVerifier() {
	verifier, err := iamproof.NewVerifier("eu-west-2", "orders.production")
	if err != nil {
		fmt.Println(err)
		return
	}
	// Pass verifier.Verify to your authentication layer.
	// It accepts a context and the proof credential, returns an identity.Caller, and calls real STS.
	// Construction alone makes no network request.
	fmt.Println(verifier != nil)
	// Output: true
}
