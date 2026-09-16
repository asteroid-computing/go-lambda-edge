// Package edge adapts AWS API Gateway Lambda proxy events to net/http handlers.
// It supports raw JSON v2 and already typed AWS events with optional native
// gateway identity. Buffered responses use one shared HTTP translation policy.
package edge

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
)

// Adapter holds an HTTP handler and its API Gateway adaptation configuration.
// Construct an Adapter with [New]; its zero value is not usable.
// Configuration is fixed at construction. Calls own separate request state;
// a shared handler must be safe for concurrent use.
type Adapter struct {
	handler http.Handler
	config  config
}

type config struct {
	gatewayIdentity      bool
	identityClaimsBudget int
	responseHeaderBudget int
}

// Option configures an [Adapter] during construction.
// Use the option functions provided by this package. A nil Option is invalid.
// Options apply in argument order; the last assignment to a scalar setting wins.
type Option func(*config)

// WithGatewayIdentity configures whether gateway assertions establish identity.
// The default is false. When enabled, the policy covers native IAM and
// JWT/Cognito assertions; it does not configure authorization in AWS or enable
// local token verification. Custom-authorizer contexts need an explicit mapper.
// Unsupported or conflicting assertions fail before running the HTTP handler.
func WithGatewayIdentity(enabled bool) Option {
	return func(c *config) {
		c.gatewayIdentity = enabled
	}
}

// WithIdentityClaimsBudget sets the gateway identity claims allowance in bytes.
// The default is 256 KiB; New rejects a final setting outside 1..6 MiB.
// Each claim value costs 64 bytes plus name, string and exact-number text bytes.
// Dedicated gateway scopes consume the same allowance. Raw claim JSON is also
// limited to this byte length. This is a resource policy, not a heap cap or AWS
// quota. It does not enable gateway identity.
func WithIdentityClaimsBudget(bytes int) Option {
	return func(c *config) {
		c.identityClaimsBudget = bytes
	}
}

// WithResponseHeaderBudget sets the response header resource budget in bytes.
// The default is 256 KiB. New rejects a final configured value outside 1..6 MiB;
// zero does not disable the bound. Each original value costs its name length,
// value length and 32 bytes; nil/empty slices cost their name length and 32.
// Generated headers also consume the budget. This is a library resource policy,
// not an AWS quota or a bound on total heap usage. It does not change the separate
// buffered envelope or streaming metadata limits.
func WithResponseHeaderBudget(bytes int) Option {
	return func(c *config) {
		c.responseHeaderBudget = bytes
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

	cfg := config{identityClaimsBudget: 256 * 1024, responseHeaderBudget: defaultResponseHeaderBudget}
	for i, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("edge: nil option at index %d", i)
		}
		opt(&cfg)
	}
	if cfg.responseHeaderBudget <= 0 || cfg.responseHeaderBudget > maxResponseBytes {
		return nil, errors.New("edge: response header budget must be between 1 and 6291456 bytes")
	}
	if cfg.identityClaimsBudget <= 0 || cfg.identityClaimsBudget > maxResponseBytes {
		return nil, errors.New("edge: identity claims budget must be between 1 and 6291456 bytes")
	}
	return &Adapter{handler: handler, config: cfg}, nil
}
