package authn_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/authn"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func FuzzAuthorization(f *testing.F) {
	f.Add("Authorization", "Bearer abc", "", false)
	f.Add("authorization", "EdgeIAM v1.abc", "Bearer def", true)
	f.Add("AUTHORIZATION", "Bearer\tfoo", "", false)
	f.Add("Authorization", "Bearer abc,def", "", false)
	f.Fuzz(func(t *testing.T, key, value, extra string, repeated bool) {
		var calls int
		verify := func(context.Context, string) (identity.Caller, error) {
			calls++
			return identity.Caller{}, authn.ErrInvalidCredentials
		}
		a := authenticator(t, authn.Config{Bearer: verify, IAMProof: verify})
		headers := http.Header{key: {value}}
		if repeated {
			headers[key] = append(headers[key], extra)
		}
		caller, err := a.Authenticate(t.Context(), headers)
		failure, ok := errors.AsType[*authn.Error](err)
		if caller.Kind() != identity.KindAnonymous || !ok || calls > 1 || repeated && calls != 0 {
			t.Fatalf("untrusted input returned caller=%v err=%v calls=%d", caller, err, calls)
		}
		if failure.StatusCode() == 401 && len(failure.Challenges()) == 0 {
			t.Fatal("401 without a challenge")
		}
	})
}
