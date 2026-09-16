package authn_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/asteroid-computing/go-lambda-edge/authn"
)

func TestCognitoColdFetchSharingAndBackoff(t *testing.T) {
	key := signingKey(t)
	body := keySet(t, publicJWK(key, "key"))
	for _, fail := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int64
			release := make(chan struct{})
			v := cognitoVerifier(t, cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				<-release
				if fail {
					return nil, errors.New("private network diagnostic")
				}
				return jwksResponse(body), nil
			})))
			var wg sync.WaitGroup
			results := make(chan error, 64)
			for range 64 {
				wg.Go(func() { results <- v.Warm(t.Context()) })
			}
			synctest.Wait()
			if calls.Load() != 1 {
				t.Fatalf("concurrent cold requests fetched %d times", calls.Load())
			}
			close(release)
			wg.Wait()
			close(results)
			want := error(nil)
			if fail {
				want = authn.ErrUnavailable
			}
			for err := range results {
				if !errors.Is(err, want) || err != nil && strings.Contains(err.Error(), "private") {
					t.Fatalf("shared fetch result %v; want %v", err, want)
				}
			}
			for range 20 {
				if err := v.Warm(t.Context()); !errors.Is(err, want) {
					t.Fatalf("Warm during cache/backoff: %v", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("cold failure bypassed backoff or fresh Warm fetched again")
			}
			if !fail {
				return
			}
			for i, delay := range []time.Duration{5, 10, 20, 40, 60, 60} {
				synctest.Sleep(delay*time.Second - time.Nanosecond)
				v.Warm(t.Context())
				if calls.Load() != int64(i+1) {
					t.Fatal("retry occurred before backoff elapsed")
				}
				synctest.Sleep(time.Nanosecond)
				v.Warm(t.Context())
				if calls.Load() != int64(i+2) {
					t.Fatal("retry did not occur at backoff boundary")
				}
			}
		})
	}
}

func TestCognitoRotationRemovalAndOutage(t *testing.T) {
	key := signingKey(t)
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		body := keySet(t, publicJWK(key, "old"))
		fail := false
		cfg := cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			if fail {
				return nil, errors.New("offline")
			}
			return jwksResponse(body), nil
		}))
		cfg.CacheTTL = time.Minute
		v := cognitoVerifier(t, cfg)
		old := signedToken(t, key, "old", accessClaims())
		rotated := signedToken(t, key, "new", accessClaims())
		verifyResult(t, v, t.Context(), old, nil)
		body = keySet(t, publicJWK(key, "new"))
		for range 10 {
			verifyResult(t, v, t.Context(), rotated, authn.ErrInvalidCredentials)
		}
		if calls.Load() != 1 {
			t.Fatal("unknown kid bypassed successful-fetch cooldown")
		}
		synctest.Sleep(30 * time.Second)
		verifyResult(t, v, t.Context(), rotated, nil)
		verifyResult(t, v, t.Context(), old, authn.ErrInvalidCredentials)
		if calls.Load() != 2 {
			t.Fatal("rotation failed to replace snapshot atomically")
		}
		synctest.Sleep(30 * time.Second)
		fail = true
		verifyResult(t, v, t.Context(), old, authn.ErrUnavailable)
		verifyResult(t, v, t.Context(), rotated, nil)
		synctest.Sleep(5 * time.Second)
		verifyResult(t, v, t.Context(), old, authn.ErrUnavailable)
		if calls.Load() != 3 {
			t.Fatal("failure backoff bypassed longer unknown-key cooldown")
		}
		synctest.Sleep(25 * time.Second)
		verifyResult(t, v, t.Context(), rotated, authn.ErrUnavailable)
		if calls.Load() != 4 {
			t.Fatal("expired keys were used or expiry did not refresh")
		}
		fail = false
		body = keySet(t, publicJWK(key, "new"))
		synctest.Sleep(10 * time.Second)
		verifyResult(t, v, t.Context(), rotated, nil)
		if calls.Load() != 5 {
			t.Fatal("recovery did not replace failed state")
		}
		verifyResult(t, v, t.Context(), old, authn.ErrInvalidCredentials)
	})
}

func TestCognitoWaiterCancellation(t *testing.T) {
	key := signingKey(t)
	body := keySet(t, publicJWK(key, "key"))
	for _, allCanceled := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int64
			release := make(chan struct{})
			type requestKey struct{}
			v := cognitoVerifier(t, cognitoConfig(transportFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.Context().Value(requestKey{}) != nil {
					t.Error("shared fetch retained a caller context value")
				}
				select {
				case <-r.Context().Done():
					return nil, r.Context().Err()
				case <-release:
					return jwksResponse(body), nil
				}
			})))
			ctx, cancel := context.WithCancelCause(context.WithValue(t.Context(), requestKey{}, "private"))
			first := make(chan error, 1)
			go func() { first <- v.Warm(ctx) }()
			synctest.Wait()
			otherCtx, cancelOther := context.WithCancel(t.Context())
			defer cancelOther()
			second := make(chan error, 1)
			go func() { second <- v.Warm(otherCtx) }()
			synctest.Wait()
			cancel(errors.New("sensitive cancellation cause"))
			if err := <-first; err != context.Canceled {
				t.Fatalf("canceled waiter returned %v", err)
			}
			if allCanceled {
				cancelOther()
				if err := <-second; err != context.Canceled {
					t.Fatalf("second canceled waiter returned %v", err)
				}
			}
			close(release)
			synctest.Wait()
			if !allCanceled {
				if err := <-second; err != nil {
					t.Fatalf("initiator cancellation disrupted other waiter: %v", err)
				}
			}
			verifyResult(t, v, t.Context(), signedToken(t, key, "key", accessClaims()), nil)
			if calls.Load() != 1 {
				t.Fatal("completed shared fetch was not retained")
			}
			verifyResult(t, v, ctx, "malformed", context.Canceled)
		})
	}
}

func TestCognitoFetchDeadlineAndLateTransport(t *testing.T) {
	key := signingKey(t)
	body := keySet(t, publicJWK(key, "key"))
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		release := make(chan struct{})
		cfg := cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			<-release // Deliberately violates the transport cancellation contract.
			return jwksResponse(body), nil
		}))
		cfg.Timeout = time.Second
		v := cognitoVerifier(t, cfg)
		start := time.Now()
		if err := v.Warm(t.Context()); !errors.Is(err, authn.ErrUnavailable) || time.Since(start) != time.Second {
			t.Fatalf("bounded waiter returned %v after %v", err, time.Since(start))
		}
		for range 10 {
			if err := v.Warm(t.Context()); !errors.Is(err, authn.ErrUnavailable) {
				t.Fatal(err)
			}
		}
		if calls.Load() != 1 {
			t.Fatal("slow transport accumulated replacement fetches")
		}
		close(release)
		synctest.Wait()
		if err := v.Warm(t.Context()); !errors.Is(err, authn.ErrUnavailable) {
			t.Fatal("late success was published")
		}
		synctest.Sleep(5 * time.Second)
		if err := v.Warm(t.Context()); err != nil || calls.Load() != 2 {
			t.Fatalf("late transport cleanup prevented recovery: %v", err)
		}
	})
}

func TestCognitoFreshKeysDoNotWaitForRefresh(t *testing.T) {
	key := signingKey(t)
	body := keySet(t, publicJWK(key, "old"))
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		release := make(chan struct{})
		v := cognitoVerifier(t, cognitoConfig(transportFunc(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) > 1 {
				<-release
				return nil, errors.New("offline")
			}
			return jwksResponse(body), nil
		})))
		old := signedToken(t, key, "old", accessClaims())
		verifyResult(t, v, t.Context(), old, nil)
		synctest.Sleep(30 * time.Second)
		unknown := signedToken(t, key, "unknown", accessClaims())
		result := make(chan error, 1)
		go func() { _, err := v.Verify(t.Context(), unknown); result <- err }()
		synctest.Wait()
		start := time.Now()
		verifyResult(t, v, t.Context(), old, nil)
		if time.Now() != start {
			t.Fatal("fresh known key waited for unrelated refresh")
		}
		close(release)
		if err := <-result; !errors.Is(err, authn.ErrUnavailable) {
			t.Fatalf("failed unknown-key refresh: %v", err)
		}
	})
}

type deadlineBody struct {
	ctx    context.Context
	closed atomic.Bool
}

func (b *deadlineBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *deadlineBody) Close() error { b.closed.Store(true); return nil }

func TestCognitoBodyDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var body *deadlineBody
		cfg := cognitoConfig(transportFunc(func(r *http.Request) (*http.Response, error) {
			body = &deadlineBody{ctx: r.Context()}
			return &http.Response{StatusCode: 200, Body: body}, nil
		}))
		cfg.Timeout = time.Second
		v := cognitoVerifier(t, cfg)
		start := time.Now()
		err := v.Warm(t.Context())
		synctest.Wait()
		if !errors.Is(err, authn.ErrUnavailable) || time.Since(start) != time.Second || !body.closed.Load() {
			t.Fatalf("body deadline: err=%v elapsed=%v closed=%v", err, time.Since(start), body.closed.Load())
		}
	})
}

type jwksBody struct {
	reader io.Reader
	read   int
	closed bool
}

func (b *jwksBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}

func (b *jwksBody) Close() error { b.closed = true; return nil }

func TestCognitoHTTPBoundAndOwnership(t *testing.T) {
	key := signingKey(t)
	good := keySet(t, publicJWK(key, "key"))
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{200, good, nil}, {200, good + strings.Repeat(" ", 65536-len(good)), nil},
		{200, strings.Repeat(" ", 1<<20), authn.ErrUnavailable},
		{200, "not JSON", authn.ErrUnavailable}, {302, good, authn.ErrUnavailable},
		{304, good, authn.ErrUnavailable}, {429, good, authn.ErrUnavailable}, {500, good, authn.ErrUnavailable},
	} {
		body := &jwksBody{reader: strings.NewReader(tc.body)}
		var calls atomic.Int64
		v := cognitoVerifier(t, cognitoConfig(transportFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			if r.URL.String() != cognitoIssuer+"/.well-known/jwks.json" || r.Method != "GET" || len(r.Header) != 0 || r.Body != nil {
				t.Error("fetch destination or request contents changed")
			}
			return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": {"https://attacker.invalid/keys"}}, Body: body}, nil
		})))
		err := v.Warm(t.Context())
		if !errors.Is(err, tc.want) || calls.Load() != 1 || !body.closed || body.read > 65537 {
			t.Fatalf("status=%d result=%v calls=%d closed=%v bytes=%d", tc.status, err, calls.Load(), body.closed, body.read)
		}
	}
	// The real HTTP transport exercises TLS and response handling locally.
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/pool/.well-known/jwks.json" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected network request")
		}
		w.Header().Set("Set-Cookie", "untrusted=never-retained")
		io.WriteString(w, good)
	}))
	defer server.Close()
	cfg := cognitoConfig(server.Client().Transport)
	cfg.Issuer = server.URL + "/pool"
	v := cognitoVerifier(t, cfg)
	claims := accessClaims()
	claims["iss"] = cfg.Issuer
	verifyResult(t, v, t.Context(), signedToken(t, key, "key", claims), nil)
	if calls.Load() != 1 {
		t.Fatal("real HTTP verification did not fetch keys")
	}
}
