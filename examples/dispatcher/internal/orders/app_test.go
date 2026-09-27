package orders_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

type grantFunc func(context.Context, identity.Caller, string, string) (bool, error)

func (f grantFunc) Allowed(ctx context.Context, caller identity.Caller, action, resource string) (bool, error) {
	return f(ctx, caller, action, resource)
}

type spyStore struct {
	reads  atomic.Int64
	events func(context.Context, string, func(string) error) error
}

func (s *spyStore) Read(ctx context.Context, target string) (string, error) {
	s.reads.Add(1)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// The response exposes which target actually executed, independently of the
	// grant callback's observation. A changed target makes the test fail.
	return "read:" + target, nil
}

func (s *spyStore) Events(ctx context.Context, target string, yield func(string) error) error {
	s.reads.Add(1)
	return s.events(ctx, target, yield)
}

func TestConsumerOutcomes(t *testing.T) {
	cfg, credentials := providerFixtures(t)
	for _, kind := range []string{"bearer", "iam"} {
		for _, tc := range []struct {
			name          string
			authorization []string
			actions       []string
			grant         string
			status        int
			code          string
			challenges    []string
		}{
			{name: "success", status: 200},
			{name: "missing", authorization: []string{}, status: 401, code: "unauthenticated", challenges: []string{`Bearer realm="orders"`, `EdgeIAM realm="orders"`}},
			{name: "invalid_bearer", authorization: []string{"Bearer invalid"}, status: 401, code: "unauthenticated", challenges: []string{`Bearer realm="orders", error="invalid_token"`}},
			{name: "invalid_proof", authorization: []string{"EdgeIAM invalid"}, status: 401, code: "unauthenticated", challenges: []string{`EdgeIAM realm="orders"`}},
			{name: "duplicate_authorization", authorization: []string{credentials[kind], credentials[kind]}, status: 400, code: "invalid_credentials", challenges: []string{`Bearer realm="orders"`, `EdgeIAM realm="orders"`}},
			{name: "duplicate_action", actions: []string{"orders.read", "orders.read"}, status: 400, code: "invalid_action"},
			{name: "missing_action", actions: []string{}, status: 400, code: "invalid_action"},
			{name: "unknown_action", actions: []string{"orders.delete"}, status: 404, code: "unknown_action"},
			{name: "watch_not_registered", actions: []string{"orders.watch"}, status: 404, code: "unknown_action"},
			{name: "grant_denied", grant: "denied", status: 403, code: "forbidden", challenges: []string{`Bearer realm="orders"`}},
			{name: "grant_outage", grant: "outage", status: 503, code: "unavailable"},
		} {
			for _, format := range []string{"native", "typed_v1", "typed_v2", "raw_rest", "raw_http_v1", "raw_http_v2", "stream_raw", "stream_typed"} {
				t.Run(kind+"/"+tc.name+"/"+format, func(t *testing.T) {
					cfg := cfg
					store := new(spyStore)
					cfg.Store = store
					var granted atomic.Int64
					cfg.Grants = grantFunc(func(ctx context.Context, caller identity.Caller, action, target string) (bool, error) {
						granted.Add(1)
						if ctx.Err() != nil || caller.Kind() == identity.KindAnonymous || action != "orders.read" || target != resource {
							t.Error("grant did not receive the authenticated caller and selected action/resource")
							return false, nil
						}
						if kind == "bearer" && caller.Source() != identity.SourceLocallyVerifiedToken || kind == "iam" && caller.Source() != identity.SourceVerifiedIAMProof {
							t.Errorf("caller source = %v for %s", caller.Source(), kind)
						}
						if tc.grant == "outage" {
							return false, errors.New("sensitive dependency diagnostic")
						}
						return tc.grant != "denied", nil
					})
					authorization := tc.authorization
					if authorization == nil {
						authorization = []string{credentials[kind]}
					}
					actions := tc.actions
					if actions == nil {
						actions = []string{"orders.read"}
					}
					headers := http.Header{"Authorization": authorization, "Action": actions, "Origin": {origin}, "Custom-Trace": {"first", "second"}, "Cookie": {"client=example"}}
					h := application(t, cfg)
					// Consumer middleware sees ordinary Go headers/cookies, including
					// the intentionally lossy V2 comma combination.
					observed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						wantTrace := []string{"first", "second"}
						if strings.HasSuffix(format, "v2") {
							wantTrace = []string{"first,second"}
						}
						if !slices.Equal(r.Header.Values("Custom-Trace"), wantTrace) {
							t.Errorf("custom header = %q; want %q", r.Header.Values("Custom-Trace"), wantTrace)
						}
						cookie, err := r.Cookie("client")
						if err != nil || cookie.Value != "example" {
							t.Errorf("request cookie = %v, %v", cookie, err)
						}
						http.SetCookie(w, &http.Cookie{Name: "one", Value: "1"})
						http.SetCookie(w, &http.Cookie{Name: "two", Value: "2"})
						h.ServeHTTP(w, r)
					})
					out := serve(t, observed, format, resource, headers)
					if out.status != tc.status {
						t.Errorf("status = %d; want %d", out.status, tc.status)
					}
					wantChallenges := tc.challenges
					cookies := out.headers.Values("Set-Cookie")
					if strings.HasSuffix(format, "v2") {
						cookies = out.cookies
						if len(wantChallenges) != 0 {
							wantChallenges = []string{strings.Join(wantChallenges, ", ")}
						}
					}
					if !slices.Equal(cookies, []string{"one=1", "two=2"}) {
						t.Errorf("response cookies = %q", cookies)
					}
					if !slices.Equal(out.headers.Values("WWW-Authenticate"), wantChallenges) {
						t.Errorf("challenges = %q; want %q", out.headers.Values("WWW-Authenticate"), wantChallenges)
					}
					if out.headers.Get("Access-Control-Allow-Origin") != origin || out.headers.Get("Cache-Control") != "no-store" || out.headers.Get("Content-Type") != "application/json" {
						t.Errorf("missing CORS/cache/content-type headers: %v", out.headers)
					}
					var body map[string]string
					if err := json.Unmarshal(out.body, &body); err != nil {
						t.Fatal(err)
					}
					if tc.status == 200 {
						if body["resource"] != resource || body["state"] != "read:"+resource || store.reads.Load() != 1 || granted.Load() != 1 {
							t.Errorf("execution did not match authorized target: %v, reads=%d grants=%d", body, store.reads.Load(), granted.Load())
						}
					} else if body["error"] != tc.code || len(body) != 1 || store.reads.Load() != 0 {
						t.Errorf("failure = %v, executions=%d; want only %q and no execution", body, store.reads.Load(), tc.code)
					}
				})
			}
		}
	}
}

func TestPublicRoutesAndPreflight(t *testing.T) {
	cfg, _ := providerFixtures(t)
	cfg.Store = new(spyStore)
	cfg.Grants = grantFunc(func(context.Context, identity.Caller, string, string) (bool, error) {
		t.Error("public route reached grant resolution")
		return false, nil
	})
	h := application(t, cfg)
	for _, tc := range []struct {
		method, path, origin string
		status               int
	}{
		{"GET", "/healthz", origin, 200},
		{"OPTIONS", resource, origin, 204},
		{"OPTIONS", "/unknown", origin, 404},
		{"OPTIONS", resource, "https://untrusted.example", 403},
		{"POST", resource, origin, 405},
		{"GET", "/tenants/042/orders/7", origin, 404},
	} {
		r := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Access-Control-Request-Method", "GET")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s = %d; want %d", tc.method, tc.path, w.Code, tc.status)
		}
		if tc.status == 204 && (w.Header().Get("Access-Control-Allow-Headers") != "Authorization, Action, Custom-Trace" || w.Header().Get("Access-Control-Allow-Methods") != "GET") {
			t.Errorf("preflight headers = %v", w.Header())
		}
	}
}

func TestVerificationOutagesNeverExecute(t *testing.T) {
	cfg, credentials := providerFixtures(t)
	for _, kind := range []string{"bearer", "iam"} {
		t.Run(kind, func(t *testing.T) {
			cfg := cfg
			store := new(spyStore)
			cfg.Store = store
			cfg.Grants = grantFunc(func(context.Context, identity.Caller, string, string) (bool, error) {
				t.Error("verification outage reached grant resolution")
				return true, nil
			})
			outage := transportFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("private provider diagnostic")
			})
			if kind == "bearer" {
				cfg.Cognito.Transport = outage
			} else {
				cfg.STSTransport = outage
			}
			headers := http.Header{"Authorization": {credentials[kind]}, "Action": {"orders.read"}, "Origin": {origin}}
			out := serve(t, application(t, cfg), "native", resource, headers)
			var body map[string]string
			if err := json.Unmarshal(out.body, &body); err != nil {
				t.Fatal(err)
			}
			if out.status != 503 || len(body) != 1 || body["error"] != "unavailable" || len(out.headers.Values("WWW-Authenticate")) != 0 || store.reads.Load() != 0 {
				t.Errorf("provider outage = %d %v, executions=%d", out.status, body, store.reads.Load())
			}
		})
	}
}

func TestCancellationDuringGrantNeverExecutes(t *testing.T) {
	cfg, credentials := providerFixtures(t)
	for _, format := range []string{"native", "buffered", "streaming"} {
		t.Run(format, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cfg := cfg
			store := new(spyStore)
			cfg.Store = store
			cfg.Grants = grantFunc(func(context.Context, identity.Caller, string, string) (bool, error) {
				cancel()
				return true, nil
			})
			h := application(t, cfg)
			headers := http.Header{"Authorization": {credentials["bearer"]}, "Action": {"orders.read"}}
			switch format {
			case "native":
				r := httptest.NewRequestWithContext(ctx, "GET", resource, nil)
				r.Header = headers
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 503 {
					t.Errorf("canceled native status = %d; want 503", w.Code)
				}
			case "buffered":
				a, err := edge.New(h)
				if err != nil {
					t.Fatal(err)
				}
				_, err = a.HandleV1(ctx, restEvent(resource, headers))
				if !errors.Is(err, context.Canceled) {
					t.Errorf("buffered error = %v; want cancellation", err)
				}
			case "streaming":
				a, err := edge.NewStreaming(h)
				if err != nil {
					t.Fatal(err)
				}
				stream, err := a.HandleV1(ctx, restEvent(resource, headers))
				if stream != nil {
					stream.Close()
				}
				if !errors.Is(err, context.Canceled) {
					t.Errorf("stream error = %v; want cancellation", err)
				}
			}
			if store.reads.Load() != 0 {
				t.Error("canceled grant executed an action")
			}
		})
	}
}
