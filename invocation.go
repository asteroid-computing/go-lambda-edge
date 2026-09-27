package edge

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// invocation owns the transport-created request and original body.
// The context is passed separately so every stage uses the same explicit lifetime.
type invocation struct {
	request *http.Request
	body    io.ReadCloser
}

// ownRequest must run immediately after successful conversion, before identity or application code can fail or replace the request's Body field.
func (inv *invocation) ownRequest(r *http.Request) {
	inv.request = r
	inv.body = r.Body
}

// cleanup runs once under the invocation owner's control, after application execution.
// Transport errors remain primary;
// cleanup details stay private.
func (inv *invocation) cleanup(err error) error {
	if inv.body != nil {
		if closeErr := inv.body.Close(); closeErr != nil {
			err = errors.Join(err, invocationError(OperationCleanup, ErrCleanup, "request body cleanup failed"))
		}
	}
	if inv.request != nil && inv.request.MultipartForm != nil {
		if removeErr := inv.request.MultipartForm.RemoveAll(); removeErr != nil {
			err = errors.Join(err, invocationError(OperationCleanup, ErrCleanup, "multipart cleanup failed"))
		}
	}
	return err
}

func invocationContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		return nil, nil, invocationError(OperationValidate, ErrInvalidInvocation, "nil invocation context")
	}
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	if identity.FromContext(parent).Kind() != identity.KindAnonymous {
		return nil, nil, invocationError(OperationIdentity, ErrIdentity, "invocation context already has a caller", identity.ErrConflict)
	}
	ctx, cancel := context.WithCancel(parent)
	return ctx, cancel, nil
}

// withInvocation scopes conversion, identity, serving, and finalization.
// A panic passes through after cleanup.
// The caller must discard its result on error.
func withInvocation(parent context.Context, run func(context.Context, *invocation) error) (err error) {
	ctx, cancel, err := invocationContext(parent)
	if err != nil {
		return err
	}
	defer cancel()
	var inv invocation
	defer func() {
		err = inv.cleanup(err)
		// Normal child cancellation is not a failed invocation.
		// Preserve an existing operation/cleanup error when the parent also canceled.
		if err == nil {
			err = parent.Err()
		}
	}()
	return run(ctx, &inv)
}
