package edge

import (
	"context"
	"errors"
	"io"
	"net/http"
)

// invocation owns the transport-created request and original body. The context
// is passed separately so every stage uses the same explicit lifetime.
type invocation struct {
	request *http.Request
	body    io.ReadCloser
}

// ownRequest must run immediately after successful conversion, before identity
// or application code can fail or replace the request's Body field.
func (inv *invocation) ownRequest(r *http.Request) {
	inv.request = r
	inv.body = r.Body
}

// withInvocation scopes conversion, identity, serving, and finalization. A panic
// passes through after cleanup. The caller must discard its result on error.
func withInvocation(parent context.Context, run func(context.Context, *invocation) error) (err error) {
	if parent == nil {
		return errors.New("edge: nil invocation context")
	}
	if err := parent.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var inv invocation
	defer func() {
		if inv.body != nil {
			if closeErr := inv.body.Close(); closeErr != nil {
				err = errors.Join(err, errors.New("edge: request body cleanup failed"))
			}
		}
		if inv.request != nil && inv.request.MultipartForm != nil {
			if removeErr := inv.request.MultipartForm.RemoveAll(); removeErr != nil {
				err = errors.Join(err, errors.New("edge: multipart cleanup failed"))
			}
		}
		// Normal child cancellation is not a failed invocation. Preserve an
		// existing operation/cleanup error when the parent also canceled.
		if err == nil {
			err = parent.Err()
		}
	}()
	return run(ctx, &inv)
}
