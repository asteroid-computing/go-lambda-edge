// Package edge provides the foundation for adapting AWS API Gateway events to
// net/http handlers. Public invocation is not yet implemented.
package edge

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
)

// Adapter holds an HTTP handler and its API Gateway adaptation configuration.
// Construct an Adapter with [New]; its zero value is not usable.
// Configuration is fixed at construction. Invocation is not yet implemented.
type Adapter struct {
	handler http.Handler
	config  config
}

type config struct {
	gatewayIdentity bool
}

// Option configures an [Adapter] during construction.
// Use the option functions provided by this package. A nil Option is invalid.
// Options apply in argument order; the last assignment to a scalar setting wins.
type Option func(*config)

// WithGatewayIdentity configures whether gateway assertions establish identity.
// The default is false. When enabled, the policy covers native IAM and
// JWT/Cognito assertions; it does not configure authorization in AWS or enable
// local token verification. Custom-authorizer contexts need an explicit mapper.
// This option currently records policy; invocation and extraction are not yet
// implemented.
func WithGatewayIdentity(enabled bool) Option {
	return func(c *config) {
		c.gatewayIdentity = enabled
	}
}

// New validates handler and opts and returns an Adapter without performing I/O
// or invoking handler. Nil handlers, including typed nil handlers, and nil
// options are rejected. On error, the returned Adapter is nil.
//
// The caller retains ownership of handler and is responsible for its concurrency
// safety when sharing it. New does not use [http.DefaultServeMux].
func New(handler http.Handler, opts ...Option) (*Adapter, error) {
	if handler == nil {
		return nil, errors.New("edge: nil HTTP handler")
	}
	// An interface containing a typed nil is non-nil. Check nil-capable kinds
	// before accepting a handler that would defer a startup fault to invocation.
	v := reflect.ValueOf(handler)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return nil, errors.New("edge: nil HTTP handler")
		}
	}

	var cfg config
	for i, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("edge: nil option at index %d", i)
		}
		opt(&cfg)
	}
	return &Adapter{handler: handler, config: cfg}, nil
}
