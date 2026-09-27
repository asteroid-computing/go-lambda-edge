package authn_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/asteroid-computing/go-lambda-edge/authn"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// Synthetic callers stand in for already verified results.
// They do not prove that a token was authenticated;
// actual verification belongs to each producer.
func jwtCaller(t testing.TB, source identity.Source) identity.Caller {
	t.Helper()
	claims, err := identity.NewClaims(map[string]any{"iss": "https://issuer.example", "sub": "alice", "scope": "orders.read"})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := identity.NewJWT(claims, source)
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func iamCaller(t testing.TB, source identity.Source) identity.Caller {
	t.Helper()
	caller, err := identity.NewIAM("arn:aws:iam::123456789012:user/Alice", source)
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func authenticator(t testing.TB, cfg authn.Config) *authn.Authenticator {
	t.Helper()
	a, err := authn.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func handler(t testing.TB, a *authn.Authenticator, next http.Handler) http.Handler {
	t.Helper()
	h, err := a.Handler(next)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func failure(t testing.TB, caller identity.Caller, err, category error, status int, challenges ...string) {
	t.Helper()
	if caller.Kind() != identity.KindAnonymous || !errors.Is(err, category) {
		t.Fatalf("Authenticate = %v, %v; want anonymous, %v", caller, err, category)
	}
	e, ok := errors.AsType[*authn.Error](err)
	if !ok {
		t.Fatalf("error type = %T; want *authn.Error", err)
	}
	if e.StatusCode() != status || !slices.Equal(e.Challenges(), challenges) {
		t.Errorf("failure = %d %q; want %d %q", e.StatusCode(), e.Challenges(), status, challenges)
	}
}

func TestHeaderSelection(t *testing.T) {
	jwt, iam := jwtCaller(t, identity.SourceLocallyVerifiedToken), iamCaller(t, identity.SourceVerifiedIAMProof)
	const bearerChallenge = `Bearer realm="edge"`
	const iamChallenge = `EdgeIAM realm="edge"`
	for _, tc := range []struct {
		name       string
		headers    http.Header
		kind       identity.Kind
		token      string
		category   error
		status     int
		challenges []string
	}{
		{name: "bearer", headers: http.Header{"Authorization": {"Bearer Exact.Case+/_~=="}}, kind: identity.KindJWT, token: "Exact.Case+/_~=="},
		{name: "iam", headers: http.Header{"authorization": {"eDgEiAm v1.Abc_-"}}, kind: identity.KindIAM, token: "v1.Abc_-"},
		{name: "whitespace", headers: http.Header{"AUTHORIZATION": {" \tbEaReR   Token\t "}}, kind: identity.KindJWT, token: "Token"},
		{name: "empty_alias", headers: http.Header{"Authorization": nil, "authorization": {"Bearer token"}}, kind: identity.KindJWT, token: "token"},
		{name: "missing", category: authn.ErrMissingCredentials, status: 401, challenges: []string{bearerChallenge, iamChallenge}},
		{name: "no_values", headers: http.Header{"Authorization": {}, "authorization": nil}, category: authn.ErrMissingCredentials, status: 401, challenges: []string{bearerChallenge, iamChallenge}},
		{name: "empty", headers: http.Header{"Authorization": {""}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge, iamChallenge}},
		{name: "repeated", headers: http.Header{"Authorization": {"Bearer token", "Bearer token"}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge, iamChallenge}},
		{name: "aliases", headers: http.Header{"Authorization": {"Bearer token"}, "authorization": {"EdgeIAM token"}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge, iamChallenge}},
		{name: "comma", headers: http.Header{"Authorization": {"Bearer token,EdgeIAM token"}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge, iamChallenge}},
		{name: "unsupported", headers: http.Header{"Authorization": {"Basic abc="}}, category: authn.ErrUnsupportedScheme, status: 401, challenges: []string{bearerChallenge, iamChallenge}},
		{name: "missing_token", headers: http.Header{"Authorization": {"Bearer "}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge + `, error="invalid_request"`, iamChallenge}},
		{name: "tab_separator", headers: http.Header{"Authorization": {"Bearer\ttoken"}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge + `, error="invalid_request"`, iamChallenge}},
		{name: "internal_space", headers: http.Header{"Authorization": {"Bearer one two"}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge + `, error="invalid_request"`, iamChallenge}},
		{name: "bad_padding", headers: http.Header{"Authorization": {"Bearer a=b"}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge + `, error="invalid_request"`, iamChallenge}},
		{name: "padding_only", headers: http.Header{"Authorization": {"Bearer ==="}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge + `, error="invalid_request"`, iamChallenge}},
		{name: "punctuation", headers: http.Header{"Authorization": {"Bearer a!b"}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge + `, error="invalid_request"`, iamChallenge}},
		{name: "control", headers: http.Header{"Authorization": {"Bearer token\r\n"}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge + `, error="invalid_request"`, iamChallenge}},
		{name: "unicode", headers: http.Header{"Authorization": {"Bearer café"}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge + `, error="invalid_request"`, iamChallenge}},
		{name: "scheme_punctuation", headers: http.Header{"Authorization": {"Bearer: token"}}, category: authn.ErrMalformedCredentials, status: 400, challenges: []string{bearerChallenge, iamChallenge}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bearerCalls, iamCalls int
			var received string
			a := authenticator(t, authn.Config{
				Bearer: func(_ context.Context, token string) (identity.Caller, error) {
					bearerCalls++
					received = token
					return jwt, nil
				},
				IAMProof: func(_ context.Context, token string) (identity.Caller, error) {
					iamCalls++
					received = token
					return iam, nil
				},
			})
			caller, err := a.Authenticate(t.Context(), tc.headers)
			if tc.category != nil {
				failure(t, caller, err, tc.category, tc.status, tc.challenges...)
				if bearerCalls+iamCalls != 0 {
					t.Error("invalid header reached a verifier")
				}
				return
			}
			if err != nil || caller.Kind() != tc.kind || received != tc.token || bearerCalls+iamCalls != 1 || tc.kind == identity.KindJWT && bearerCalls != 1 || tc.kind == identity.KindIAM && iamCalls != 1 {
				t.Errorf("Authenticate = %v, %v; token=%q calls=%d/%d", caller, err, received, bearerCalls, iamCalls)
			}
			if identity.FromContext(t.Context()).Kind() != identity.KindAnonymous {
				t.Fatal("Authenticate installed a caller")
			}
		})
	}
}

func TestLimitsAndDisabledSchemes(t *testing.T) {
	verify := func(context.Context, string) (identity.Caller, error) {
		return identity.Caller{}, authn.ErrInvalidCredentials
	}
	a := authenticator(t, authn.Config{Bearer: verify, MaxAuthorizationBytes: 8})
	caller, err := a.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer x"}})
	failure(t, caller, err, authn.ErrInvalidCredentials, 401, `Bearer realm="edge", error="invalid_token"`)
	for _, headers := range []http.Header{{"Authorization": {"Bearer xx"}}, {"Authorization": {" Bearer x "}}, {"Authorization": {"Bearer x"}, "authorization": {"x"}}} {
		caller, err := a.Authenticate(t.Context(), headers)
		failure(t, caller, err, authn.ErrHeaderTooLarge, 431)
	}
	for _, tc := range []struct {
		cfg       authn.Config
		value     string
		challenge string
	}{
		{cfg: authn.Config{Bearer: verify}, value: "EdgeIAM proof", challenge: `Bearer realm="edge"`},
		{cfg: authn.Config{IAMProof: verify}, value: "Bearer token", challenge: `EdgeIAM realm="edge"`},
	} {
		caller, err := authenticator(t, tc.cfg).Authenticate(t.Context(), http.Header{"Authorization": {tc.value}})
		failure(t, caller, err, authn.ErrUnsupportedScheme, 401, tc.challenge)
	}
	for _, size := range []int{16384, 16385} {
		a := authenticator(t, authn.Config{Bearer: verify})
		caller, err := a.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer " + strings.Repeat("x", size-7)}})
		if size == 16384 {
			failure(t, caller, err, authn.ErrInvalidCredentials, 401, `Bearer realm="edge", error="invalid_token"`)
		} else {
			failure(t, caller, err, authn.ErrHeaderTooLarge, 431)
		}
	}
}

func TestVerifierErrorsAndResults(t *testing.T) {
	jwt := jwtCaller(t, identity.SourceLocallyVerifiedToken)
	iam := iamCaller(t, identity.SourceVerifiedIAMProof)
	secret := errors.New("secret token and provider URL")
	for _, tc := range []struct {
		name     string
		caller   identity.Caller
		err      error
		category error
		status   int
	}{
		{name: "invalid", err: fmt.Errorf("secret: %w", authn.ErrInvalidCredentials), category: authn.ErrInvalidCredentials, status: 401},
		{name: "unavailable", err: authn.ErrUnavailable, category: authn.ErrUnavailable, status: 503},
		{name: "both", err: errors.Join(authn.ErrInvalidCredentials, authn.ErrUnavailable), category: authn.ErrUnavailable, status: 503},
		{name: "unknown", err: secret, category: authn.ErrUnavailable, status: 503},
		{name: "internal_cancel", err: context.Canceled, category: authn.ErrUnavailable, status: 503},
		{name: "internal_deadline", err: context.DeadlineExceeded, category: authn.ErrUnavailable, status: 503},
		{name: "error_with_caller", caller: jwt, err: secret, category: authn.ErrUnavailable, status: 503},
		{name: "anonymous_success", category: authn.ErrVerifierContract, status: 500},
		{name: "wrong_kind", caller: iam, category: authn.ErrVerifierContract, status: 500},
		{name: "gateway_jwt", caller: jwtCaller(t, identity.SourceGatewayAssertion), category: authn.ErrVerifierContract, status: 500},
		{name: "custom_jwt", caller: jwtCaller(t, identity.SourceCustomAssertion), category: authn.ErrVerifierContract, status: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count int
			a := authenticator(t, authn.Config{
				Bearer: func(context.Context, string) (identity.Caller, error) { count++; return tc.caller, tc.err },
				IAMProof: func(context.Context, string) (identity.Caller, error) {
					t.Error("fallback verifier called")
					return iam, nil
				},
			})
			caller, err := a.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer secret"}})
			var challenges []string
			if tc.status == 401 {
				challenges = []string{`Bearer realm="edge", error="invalid_token"`}
			}
			failure(t, caller, err, tc.category, tc.status, challenges...)
			if count != 1 || errors.Is(err, secret) || strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "secret") {
				t.Error("wrong verifier count or unsafe diagnostic/error chain")
			}
		})
	}
	for _, caller := range []identity.Caller{jwt, iamCaller(t, identity.SourceGatewayAssertion), iamCaller(t, identity.SourceCustomAssertion)} {
		a := authenticator(t, authn.Config{IAMProof: func(context.Context, string) (identity.Caller, error) { return caller, nil }})
		got, err := a.Authenticate(t.Context(), http.Header{"Authorization": {"EdgeIAM proof"}})
		failure(t, got, err, authn.ErrVerifierContract, 500)
	}
}

func TestContextIsolationAndConflict(t *testing.T) {
	jwt := jwtCaller(t, identity.SourceLocallyVerifiedToken)
	var calls int
	a := authenticator(t, authn.Config{Bearer: func(context.Context, string) (identity.Caller, error) { calls++; return jwt, nil }})
	ctx, err := identity.NewContext(t.Context(), jwt)
	if err != nil {
		t.Fatal(err)
	}
	caller, err := a.Authenticate(ctx, nil)
	failure(t, caller, err, identity.ErrConflict, 500)
	if calls != 0 {
		t.Fatal("existing caller reached verifier")
	}
	type key struct{}
	r := httptest.NewRequestWithContext(context.WithValue(t.Context(), key{}, "keep"), "GET", "/", nil)
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("Action", "orders.read")
	var nextCalls int
	h := handler(t, a, http.HandlerFunc(func(w http.ResponseWriter, next *http.Request) {
		nextCalls++
		if identity.FromContext(next.Context()).Kind() != identity.KindJWT || next.Context().Value(key{}) != "keep" || next.Header.Get("Action") != "orders.read" {
			t.Error("middleware lost caller, unrelated context or action")
		}
		w.WriteHeader(204)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || nextCalls != 1 || identity.FromContext(r.Context()).Kind() != identity.KindAnonymous || r.Header.Get("Authorization") != "Bearer token" {
		t.Error("middleware modified original request or failed to call next once")
	}
}

func TestCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		jwt := jwtCaller(t, identity.SourceLocallyVerifiedToken)
		for _, before := range []bool{false, true} {
			ctx, cancel := context.WithCancelCause(t.Context())
			if before {
				cancel(errors.New("secret context cause"))
			}
			var calls int
			a := authenticator(t, authn.Config{Bearer: func(context.Context, string) (identity.Caller, error) {
				calls++
				cancel(errors.New("secret context cause"))
				return jwt, nil
			}})
			caller, err := a.Authenticate(ctx, http.Header{"Authorization": {"Bearer token"}})
			if err != context.Canceled || caller.Kind() != identity.KindAnonymous || before && calls != 0 {
				t.Fatalf("canceled Authenticate = %v, %v; calls=%d", caller, err, calls)
			}
			w := httptest.NewRecorder()
			handler(t, a, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("canceled request ran next") })).ServeHTTP(w, httptest.NewRequestWithContext(ctx, "GET", "/", nil))
			if w.Code != 503 || w.Header().Get("WWW-Authenticate") != "" {
				t.Error("canceled middleware did not produce generic 503")
			}
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		a := authenticator(t, authn.Config{Bearer: func(ctx context.Context, _ string) (identity.Caller, error) { <-ctx.Done(); return jwt, nil }})
		caller, err := a.Authenticate(ctx, http.Header{"Authorization": {"Bearer token"}})
		if err != context.DeadlineExceeded || caller.Kind() != identity.KindAnonymous {
			t.Fatalf("deadline Authenticate = %v, %v", caller, err)
		}
	})
}

func TestFailureResponseOwnership(t *testing.T) {
	for _, providerErr := range []error{authn.ErrInvalidCredentials, authn.ErrUnavailable, nil} {
		a := authenticator(t, authn.Config{Bearer: func(context.Context, string) (identity.Caller, error) { return identity.Caller{}, providerErr }})
		h := handler(t, a, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("failure ran next") }))
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		w.Header()["www-authenticate"] = []string{"stale secret"}
		w.Header()["WWW-Authenticate"] = []string{"another stale challenge"}
		w.Header()["cache-control"] = []string{"public"}
		w.Header().Set("Access-Control-Allow-Origin", "https://app.example")
		h.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "secret") || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
			t.Error("failure exposed credentials or lost response header ownership")
		}
		for name, values := range w.Header() {
			if strings.EqualFold(name, "WWW-Authenticate") && (providerErr != authn.ErrInvalidCredentials || !slices.Equal(values, []string{`Bearer realm="edge", error="invalid_token"`})) {
				t.Errorf("unexpected challenges: %q", values)
			}
		}
	}
}

func TestConfigurationAndChallengeOwnership(t *testing.T) {
	verify := func(context.Context, string) (identity.Caller, error) {
		return identity.Caller{}, authn.ErrInvalidCredentials
	}
	for _, cfg := range []authn.Config{{}, {Bearer: verify, Realm: "bad\r\n"}, {Bearer: verify, Realm: "café"}, {Bearer: verify, Realm: strings.Repeat("a", 257)}, {Bearer: verify, MaxAuthorizationBytes: -1}, {Bearer: verify, MaxAuthorizationBytes: 1<<20 + 1}} {
		if a, err := authn.New(cfg); a != nil || !errors.Is(err, authn.ErrInvalidConfiguration) {
			t.Fatalf("invalid configuration accepted: %v", err)
		}
	}
	cfg := authn.Config{Bearer: verify, Realm: `orders "prod"\west`}
	a := authenticator(t, cfg)
	cfg.Bearer, cfg.Realm = nil, "changed"
	_, err := a.Authenticate(t.Context(), nil)
	e, ok := errors.AsType[*authn.Error](err)
	if !ok || !slices.Equal(e.Challenges(), []string{`Bearer realm="orders \"prod\"\\west"`}) {
		t.Fatalf("realm not correctly escaped/snapshotted: %v", err)
	}
	challenges := e.Challenges()
	challenges[0] = "changed"
	if e.Challenges()[0] == "changed" {
		t.Fatal("Challenges exposed mutable backing data")
	}
	for _, a := range []*authn.Authenticator{nil, new(authn.Authenticator)} {
		if _, err := a.Authenticate(t.Context(), nil); !errors.Is(err, authn.ErrInvalidConfiguration) {
			t.Fatal("unconfigured authenticator accepted")
		}
		if _, err := a.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); !errors.Is(err, authn.ErrInvalidConfiguration) {
			t.Fatal("unconfigured Handler accepted")
		}
	}
	if _, err := a.Authenticate(nil, nil); !errors.Is(err, authn.ErrInvalidConfiguration) {
		t.Fatal("nil context accepted")
	}
	var nilFunc http.HandlerFunc
	var nilPointer *http.ServeMux
	for _, next := range []http.Handler{nil, nilFunc, nilPointer} {
		if _, err := a.Handler(next); !errors.Is(err, authn.ErrInvalidConfiguration) {
			t.Fatal("nil next handler accepted")
		}
	}
}

func TestConcurrentRequests(t *testing.T) {
	jwt, iam := jwtCaller(t, identity.SourceLocallyVerifiedToken), iamCaller(t, identity.SourceVerifiedIAMProof)
	a := authenticator(t, authn.Config{
		Bearer:   func(context.Context, string) (identity.Caller, error) { return jwt, nil },
		IAMProof: func(context.Context, string) (identity.Caller, error) { return iam, nil },
	})
	var count atomic.Int64
	h := handler(t, a, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		kind := identity.KindJWT
		if strings.HasPrefix(r.Header.Get("Authorization"), "EdgeIAM") {
			kind = identity.KindIAM
		}
		if identity.FromContext(r.Context()).Kind() != kind {
			t.Error("caller crossed request boundaries")
		}
		w.WriteHeader(204)
	}))
	var group sync.WaitGroup
	for i := range 64 {
		group.Go(func() {
			r := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
			scheme := "Bearer"
			if i%2 == 0 {
				scheme = "EdgeIAM"
			}
			r.Header.Set("Authorization", scheme+" token")
			h.ServeHTTP(httptest.NewRecorder(), r)
		})
	}
	group.Wait()
	if count.Load() != 64 {
		t.Fatalf("handler ran %d times; want 64", count.Load())
	}
}

func TestPanicPropagation(t *testing.T) {
	marker := new(int)
	a := authenticator(t, authn.Config{Bearer: func(context.Context, string) (identity.Caller, error) { panic(marker) }})
	defer func() {
		if got := recover(); got != marker {
			t.Errorf("panic=%v; want original marker", got)
		}
	}()
	a.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer token"}})
}
