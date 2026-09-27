package identity

import "context"

type callerKey struct{}

// FromContext returns the established caller or the anonymous zero value.
// Like ordinary context operations, it requires a nonnil context.
func FromContext(ctx context.Context) Caller {
	caller, _ := ctx.Value(callerKey{}).(Caller)
	return caller
}

// NewContext returns a derived context carrying caller, for retrieval with [FromContext].
// Anonymous on an empty context is a no-op.
// Any attempted installation over a nonanonymous caller, including an identical caller or anonymous, returns [ErrConflict] and a nil context.
// A nil context returns [ErrInvalidCaller].
// Unrelated values are preserved.
func NewContext(ctx context.Context, caller Caller) (context.Context, error) {
	if ctx == nil {
		return nil, ErrInvalidCaller
	}
	if FromContext(ctx).Kind() != KindAnonymous {
		return nil, ErrConflict
	}
	if caller.Kind() == KindAnonymous {
		return ctx, nil
	}
	return context.WithValue(ctx, callerKey{}, caller), nil
}
