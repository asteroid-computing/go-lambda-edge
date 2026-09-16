package iamproof_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/iamproof"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func FuzzProofValidation(f *testing.F) {
	f.Add("")
	f.Add("v1.e30")
	f.Add(generate(f).Value())
	v := verifier(f, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(403, errorXML("SignatureDoesNotMatch")), nil
	}))
	f.Fuzz(func(t *testing.T, token string) {
		caller, err := v.Verify(t.Context(), token)
		if caller.Kind() != identity.KindAnonymous || !errors.Is(err, iamproof.ErrInvalidProof) {
			t.Fatalf("unverified input produced caller=%v err=%v", caller, err)
		}
	})
}

func FuzzSTSXML(f *testing.F) {
	f.Add(successXML(testARN))
	f.Add(errorXML("InvalidClientTokenId"))
	f.Add("")
	f.Fuzz(func(t *testing.T, body string) {
		v := verifier(t, roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, body), nil }))
		caller, err := v.Verify(t.Context(), generate(t).Value())
		if err != nil {
			assertFailure(t, caller, err, iamproof.ErrUnavailable)
			return
		}
		iam, ok := caller.IAM()
		if !ok || iam.Partition() != "aws" || caller.Source() != identity.SourceVerifiedIAMProof {
			t.Fatal("XML success violated caller invariants")
		}
	})
}
