package authn

import (
	"context"
	"errors"
	"net/http"
	"reflect"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// Handler constructs required-authentication middleware.
// It validates next, including typed nils.
// Success installs one caller in a derived request context and invokes next exactly once.
// It leaves the original request and headers intact.
//
// Failure writes a generic text response, no-store and the applicable challenges;
// next is not invoked.
// Caller cancellation attempts 503 without a challenge, but delivery is not guaranteed.
// Provider/handler panics propagate.
// Public routes and CORS/preflight must be configured separately;
// there is no OPTIONS bypass.
func (a *Authenticator) Handler(next http.Handler) (http.Handler, error) {
	if a == nil || a.cfg.MaxAuthorizationBytes == 0 || next == nil {
		return nil, ErrInvalidConfiguration
	}
	v := reflect.ValueOf(next)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return nil, ErrInvalidConfiguration
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, err := a.Authenticate(r.Context(), r.Header)
		if err != nil {
			writeFailure(w, err)
			return
		}
		ctx, err := identity.NewContext(r.Context(), caller)
		if err != nil {
			writeFailure(w, a.failure(identity.ErrConflict, http.StatusInternalServerError, noScheme))
			return
		}
		if err := ctx.Err(); err != nil {
			writeFailure(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	}), nil
}

func writeFailure(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var challenges []string
	if failure, ok := errors.AsType[*Error](err); ok {
		status, challenges = failure.StatusCode(), failure.Challenges()
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusServiceUnavailable
	}
	headers := w.Header()
	// Direct map users can leave noncanonical aliases.
	// Own these failure fields without dropping unrelated outer middleware headers such as CORS.
	for name := range headers {
		switch http.CanonicalHeaderKey(name) {
		case "Www-Authenticate", "Cache-Control":
			delete(headers, name)
		}
	}
	for _, challenge := range challenges {
		headers.Add("WWW-Authenticate", challenge)
	}
	headers.Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(status), status)
}
