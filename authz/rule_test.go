package authz_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/authz"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func rule(t testing.TB, r authz.Rule, err error) authz.Rule {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func checked(t testing.TB, f authz.CheckFunc) authz.Rule {
	t.Helper()
	r, err := authz.Check(f)
	return rule(t, r, err)
}

func iamCaller(t testing.TB, arn string, opts ...identity.IAMOption) identity.Caller {
	t.Helper()
	c, err := identity.NewIAM(arn, identity.SourceCustomAssertion, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func request(t testing.TB) authz.Request {
	t.Helper()
	return authz.Request{Caller: iamCaller(t, "arn:aws:iam::123456789012:user/Alice"), Action: "orders.read", Resource: "tenant/42/order/7"}
}

func TestAuthorizeValidationOrder(t *testing.T) {
	var calls int
	r := checked(t, func(context.Context, authz.Request) (bool, error) { calls++; return true, nil })
	valid := request(t)
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errors.New("private cancellation cause"))
	for _, tc := range []struct {
		name    string
		rule    authz.Rule
		ctx     context.Context
		request authz.Request
		want    error
	}{
		{"zero_before_cancellation", authz.Rule{}, ctx, valid, authz.ErrInvalidConfiguration},
		{"nil_context", r, nil, valid, authz.ErrInvalidConfiguration},
		{"canceled_before_request", r, ctx, authz.Request{}, context.Canceled},
		{"invalid_before_anonymous", r, t.Context(), authz.Request{}, authz.ErrInvalidRequest},
		{"anonymous", r, t.Context(), authz.Request{Action: "orders.read"}, authz.ErrUnauthenticated},
		{"invalid_action", r, t.Context(), authz.Request{Caller: valid.Caller, Action: "\xff"}, authz.ErrInvalidRequest},
		{"invalid_resource", r, t.Context(), authz.Request{Caller: valid.Caller, Action: valid.Action, Resource: "\xff"}, authz.ErrInvalidRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.rule.Authorize(tc.ctx, tc.request); !errors.Is(err, tc.want) {
				t.Errorf("Authorize = %v; want %v", err, tc.want)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid requests invoked callback %d times", calls)
	}
	// authz preserves application strings;
	// it does not impose the action header grammar.
	valid.Action, valid.Resource = " Read 日本語 ", ""
	if err := r.Authorize(t.Context(), valid); err != nil || calls != 1 {
		t.Fatalf("valid UTF-8 request = %v, calls=%d", err, calls)
	}
}

func TestCallbackOutcomes(t *testing.T) {
	private := errors.New("private grant service diagnostic")
	for _, callbackErr := range []error{nil, private, context.Canceled, context.DeadlineExceeded, authz.ErrDenied, authz.ErrInvalidConfiguration, authz.ErrInvalidRequest, authz.ErrUnauthenticated, authz.ErrUnavailable, fmt.Errorf("private wrapper: %w", authz.ErrDenied)} {
		for _, allow := range []bool{false, true} {
			r := checked(t, func(context.Context, authz.Request) (bool, error) { return allow, callbackErr })
			want := authz.ErrDenied
			if callbackErr != nil {
				want = authz.ErrUnavailable
			} else if allow {
				want = nil
			}
			err := r.Authorize(t.Context(), request(t))
			if !errors.Is(err, want) || err != nil && (errors.Is(err, private) || strings.Contains(err.Error(), "private")) {
				t.Errorf("allow=%t callback=%v: got %v; want sanitized %v", allow, callbackErr, err, want)
			}
		}
	}
}

func TestOrderedCombinators(t *testing.T) {
	for _, op := range []string{"all", "any"} {
		for _, first := range []string{"allow", "deny", "error"} {
			for _, second := range []string{"allow", "deny", "error"} {
				t.Run(op+"/"+first+"/"+second, func(t *testing.T) {
					var visited []string
					makeCheck := func(label, outcome string) authz.Rule {
						return checked(t, func(context.Context, authz.Request) (bool, error) {
							visited = append(visited, label)
							if outcome == "error" {
								return true, errors.New("private")
							}
							return outcome == "allow", nil
						})
					}
					children := []authz.Rule{makeCheck("first", first), makeCheck("second", second)}
					combine := authz.All
					if op == "any" {
						combine = authz.Any
					}
					r, err := combine(children...)
					r = rule(t, r, err)
					if len(visited) != 0 {
						t.Fatal("constructor called a check")
					}
					wantVisits := []string{"first"}
					outcome := first
					if op == "all" && first == "allow" || op == "any" && first == "deny" {
						wantVisits = append(wantVisits, "second")
						outcome = second
					}
					want := map[string]error{"allow": nil, "deny": authz.ErrDenied, "error": authz.ErrUnavailable}[outcome]
					if err := r.Authorize(t.Context(), request(t)); !errors.Is(err, want) || !slices.Equal(visited, wantVisits) {
						t.Errorf("Authorize = %v, visits=%v; want %v, %v", err, visited, want, wantVisits)
					}
				})
			}
		}
	}
}

func TestCancellationWinsAndStopsChecks(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, callbackErr := range []error{nil, errors.New("private dependency failure")} {
			for _, combine := range []func(...authz.Rule) (authz.Rule, error){authz.All, authz.Any} {
				ctx, cancel := context.WithCancelCause(t.Context())
				first := checked(t, func(context.Context, authz.Request) (bool, error) {
					cancel(errors.New("secret cause"))
					return allow, callbackErr
				})
				second := checked(t, func(context.Context, authz.Request) (bool, error) {
					t.Error("check ran after cancellation")
					return true, nil
				})
				r, err := combine(first, second)
				r = rule(t, r, err)
				if err := r.Authorize(ctx, request(t)); err != context.Canceled {
					t.Errorf("Authorize = %v; want context.Canceled", err)
				}
			}
		}
	}
}

func TestConfigurationBounds(t *testing.T) {
	leaf := checked(t, func(context.Context, authz.Request) (bool, error) { return true, nil })
	if _, err := authz.Check(nil); !errors.Is(err, authz.ErrInvalidConfiguration) {
		t.Fatal(err)
	}
	for _, combine := range []func(...authz.Rule) (authz.Rule, error){authz.All, authz.Any} {
		for _, children := range [][]authz.Rule{nil, {authz.Rule{}}, {leaf, authz.Rule{}}} {
			if _, err := combine(children...); !errors.Is(err, authz.ErrInvalidConfiguration) {
				t.Fatal(err)
			}
		}
		deep := leaf
		for range 31 {
			next, err := combine(deep)
			deep = rule(t, next, err)
		}
		if err := deep.Authorize(t.Context(), request(t)); err != nil {
			t.Fatal(err)
		}
		if _, err := combine(deep); !errors.Is(err, authz.ErrInvalidConfiguration) {
			t.Fatalf("depth 33 = %v", err)
		}
		wide := slices.Repeat([]authz.Rule{leaf}, 1023)
		r, err := combine(wide...)
		r = rule(t, r, err)
		if err := r.Authorize(t.Context(), request(t)); err != nil {
			t.Fatal(err)
		}
		if _, err := combine(append(wide, leaf)...); !errors.Is(err, authz.ErrInvalidConfiguration) {
			t.Fatalf("1025 nodes = %v", err)
		}
		// This shared DAG has only ten distinct nodes but 1,023 expanded visits.
		dag := leaf
		for range 9 {
			next, err := combine(dag, dag)
			dag = rule(t, next, err)
		}
		if _, err := combine(dag); err != nil {
			t.Fatal(err)
		}
		if _, err := combine(dag, leaf); !errors.Is(err, authz.ErrInvalidConfiguration) {
			t.Fatalf("expanded DAG bound = %v", err)
		}
	}
}

func TestOwnershipAndConcurrentReuse(t *testing.T) {
	var calls atomic.Int64
	want := request(t)
	leaf := checked(t, func(_ context.Context, got authz.Request) (bool, error) {
		calls.Add(1)
		if got.Action != want.Action || got.Resource != want.Resource || got.Caller.Kind() != want.Caller.Kind() {
			t.Errorf("check received changed request: action=%q resource=%q", got.Action, got.Resource)
		}
		got.Action, got.Resource, got.Caller = "changed", "changed", identity.Caller{}
		return true, nil
	})
	children := []authz.Rule{leaf, leaf}
	r, err := authz.All(children...)
	r = rule(t, r, err)
	children[0] = authz.Rule{}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if err := r.Authorize(t.Context(), want); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 64 {
		t.Fatalf("shared check calls=%d; want 64", calls.Load())
	}
}

func TestCallbackPanicPropagates(t *testing.T) {
	r := checked(t, func(context.Context, authz.Request) (bool, error) { panic("programmer bug") })
	defer func() {
		if got := recover(); got != "programmer bug" {
			t.Errorf("panic = %v", got)
		}
	}()
	_ = r.Authorize(t.Context(), request(t))
}

func TestExplicitCallerAndCallerContext(t *testing.T) {
	facts := request(t)
	other := iamCaller(t, "arn:aws:iam::123456789012:user/Bob")
	ctx, err := identity.WithCaller(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	r := checked(t, func(got context.Context, req authz.Request) (bool, error) {
		if got != ctx {
			t.Error("callback did not receive caller context")
		}
		i, _ := req.Caller.IAM()
		return i.PrincipalARN() == "arn:aws:iam::123456789012:user/Alice", nil
	})
	if err := r.Authorize(ctx, facts); err != nil {
		t.Fatal(err)
	}
}
