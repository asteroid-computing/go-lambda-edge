package edge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func TestInvocationRejectsInheritedCaller(t *testing.T) {
	caller, err := identity.NewIAM("arn:aws:iam::123456789012:user/Alice", identity.SourceGatewayAssertion)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := identity.NewContext(t.Context(), caller)
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	err = withInvocation(parent, func(context.Context, *invocation) error {
		ran = true
		return nil
	})
	if ran || !errors.Is(err, ErrIdentity) || !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("inherited caller: ran=%t, err=%v; want identity conflict before work", ran, err)
	}
	if detail, ok := errors.AsType[*InvocationError](err); !ok || detail.Operation() != "identity" {
		t.Errorf("inherited caller error = %v, want identity operation", err)
	}
	if strings.Contains(err.Error(), "Alice") {
		t.Error("isolation error leaked caller identity")
	}
	if err := withInvocation(t.Context(), func(context.Context, *invocation) error { return nil }); err != nil {
		t.Errorf("independent next invocation rejected: %v", err)
	}
}

func TestInvocationRejectsInvalidContextBeforeRunning(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, stop := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer stop()
	for _, tt := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{name: "nil"},
		{name: "canceled", ctx: canceled, want: context.Canceled},
		{name: "expired", ctx: expired, want: context.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ran := false
			err := withInvocation(tt.ctx, func(context.Context, *invocation) error {
				ran = true
				return nil
			})
			if ran || err == nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("withInvocation(%s) ran=%v err=%v, want no run and %v", tt.name, ran, err, tt.want)
			}
		})
	}
}

func TestInvocationScopeIncludesAllStages(t *testing.T) {
	type contextKey struct{}
	parent := context.WithValue(t.Context(), contextKey{}, "invocation value")
	var scope context.Context
	var original *trackedBody
	var replacement *trackedBody
	err := withInvocation(parent, func(ctx context.Context, inv *invocation) error {
		scope = ctx
		// Conversion, identity, handler, and finalization all execute under ctx.
		r, err := requestV2(ctx, v2RequestEvent())
		if err != nil {
			return err
		}
		original = &trackedBody{Reader: r.Body}
		r.Body = original
		inv.ownRequest(r)
		if r.Context() != ctx || ctx.Err() != nil || ctx.Value(contextKey{}) != "invocation value" {
			t.Error("converted request lost invocation scope/value")
		}
		replacement = &trackedBody{Reader: strings.NewReader("application-owned")}
		r.Body = replacement
		if original.closed || ctx.Err() != nil {
			t.Error("scope/body closed before finalization")
		}
		_, err = marshalResponse(struct{ StatusCode int }{StatusCode: http.StatusOK})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if scope.Err() != context.Canceled || parent.Err() != nil || !original.closed || replacement.closed {
		t.Errorf("completed scope: child=%v parent=%v original closed=%v replacement closed=%v", scope.Err(), parent.Err(), original.closed, replacement.closed)
	}
}

type failingCloseBody struct {
	io.Reader
	closed bool
}

func (b *failingCloseBody) Close() error {
	b.closed = true
	return errors.New("private body credential or path")
}

func TestInvocationErrorPrecedence(t *testing.T) {
	operationErr := errors.New("operation failed")
	for _, tt := range []struct {
		name        string
		operation   error
		cancel      bool
		failCleanup bool
	}{
		{name: "success"},
		{name: "late_cancel", cancel: true},
		{name: "operation_failure", operation: operationErr},
		{name: "operation_and_cancel", operation: operationErr, cancel: true},
		{name: "cleanup_failure", failCleanup: true},
		{name: "cleanup_and_cancel", failCleanup: true, cancel: true},
		{name: "operation_and_cleanup", operation: operationErr, failCleanup: true},
		{name: "all_failures", operation: operationErr, failCleanup: true, cancel: true},
		{name: "standard_http_error", operation: http.ErrContentLength},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			var scope context.Context
			body := &failingCloseBody{Reader: http.NoBody}
			err := withInvocation(parent, func(ctx context.Context, inv *invocation) error {
				scope = ctx
				if tt.failCleanup {
					inv.ownRequest(&http.Request{Body: body})
				}
				if tt.cancel {
					cancel()
				}
				return tt.operation
			})
			wantError := tt.operation != nil || tt.failCleanup || tt.cancel
			if (err != nil) != wantError {
				t.Fatalf("withInvocation() = %v, want error %v", err, wantError)
			}
			if tt.operation != nil && !errors.Is(err, tt.operation) {
				t.Errorf("error %v does not preserve operation %v", err, tt.operation)
			}
			wantCanceled := tt.cancel && tt.operation == nil && !tt.failCleanup
			if errors.Is(err, context.Canceled) != wantCanceled {
				t.Errorf("error %v matches cancellation=%v, want %v", err, errors.Is(err, context.Canceled), wantCanceled)
			}
			if tt.failCleanup && (!body.closed || strings.Contains(err.Error(), "private")) {
				t.Errorf("cleanup closed=%v err=%v, want sanitized failure", body.closed, err)
			}
			if scope.Err() != context.Canceled {
				t.Errorf("child after return = %v, want canceled", scope.Err())
			}
		})
	}
}

func TestInvocationCleansUpBeforeHandler(t *testing.T) {
	identityErr := errors.New("identity failed")
	body := &trackedBody{Reader: http.NoBody}
	var scope context.Context
	err := withInvocation(t.Context(), func(ctx context.Context, inv *invocation) error {
		scope = ctx
		inv.ownRequest(&http.Request{Body: body})
		return identityErr
	})
	if !errors.Is(err, identityErr) || !body.closed || scope.Err() != context.Canceled {
		t.Errorf("pre-handler failure: err=%v closed=%v context=%v", err, body.closed, scope.Err())
	}
}

func TestInvocationPreservesPanic(t *testing.T) {
	panicValue := new("application panic")
	body := &failingCloseBody{Reader: http.NoBody}
	var scope context.Context
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		if err := withInvocation(t.Context(), func(ctx context.Context, inv *invocation) error {
			scope = ctx
			inv.ownRequest(&http.Request{Body: body})
			panic(panicValue)
		}); err != nil {
			t.Errorf("panicking invocation returned %v", err)
		}
	}()
	if recovered != panicValue || !body.closed || scope.Err() != context.Canceled {
		t.Errorf("panic result: value=%v closed=%v context=%v", recovered, body.closed, scope.Err())
	}
}
