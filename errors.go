package edge

import (
	"errors"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// Invocation failure categories support errors.Is through wrapping and joined
// errors. Application HTTP responses, including 4xx and 5xx, are not invocation
// failures when the transport completes successfully.
var (
	// ErrInvalidInvocation indicates an invalid direct invocation argument or adapter.
	ErrInvalidInvocation = errors.New("edge: invalid invocation")
	// ErrInvalidEvent indicates a malformed event or failed HTTP request conversion.
	ErrInvalidEvent = errors.New("edge: invalid event")
	// ErrUnsupportedEvent indicates an unsupported event family or payload version.
	ErrUnsupportedEvent = errors.New("edge: unsupported event")
	// ErrIdentity indicates failure to establish or isolate invocation identity.
	ErrIdentity = errors.New("edge: identity failure")
	// ErrResponse indicates an invalid or unrepresentable HTTP response.
	ErrResponse = errors.New("edge: response failure")
	// ErrLimitExceeded indicates an adapter-enforced resource limit.
	ErrLimitExceeded = errors.New("edge: resource limit exceeded")
	// ErrCleanup indicates failure to clean up invocation-owned request resources.
	ErrCleanup = errors.New("edge: request cleanup failure")
	// ErrStream indicates a stream lifecycle failure or a recovered late panic.
	ErrStream = errors.New("edge: stream failure")
)

// InvocationError describes a sanitized transport failure. Use errors.Is to
// inspect its category and errors.AsType[*InvocationError] for its diagnostics.
// Its text is diagnostic, not a stable identifier or an HTTP response body.
// It does not expose event values, application data, or unsafe underlying errors.
// A zero InvocationError describes an unclassified failure.
//
// Operation failures precede any joined cleanup failures. Parent cancellation
// is returned as the standard context error when there is no operation or cleanup
// failure. Application panics propagated to the runtime are not InvocationErrors.
type InvocationError struct {
	operation string
	detail    string
	cause     error
	limit     *invocationLimit
}

type invocationLimit struct {
	name    string
	maximum int64
}

// Error returns a sanitized description of the failed operation.
func (e *InvocationError) Error() string {
	if e == nil || e.operation == "" {
		return "edge: invocation failed"
	}
	return "edge: " + e.operation + ": " + e.detail
}

// Unwrap exposes the failure category and any explicitly preserved safe causes.
func (e *InvocationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Operation identifies the failed operation: validate, decode, request,
// identity, response, encode, cleanup, or stream. A zero error returns "".
func (e *InvocationError) Operation() string {
	if e == nil {
		return ""
	}
	return e.operation
}

// Limit reports the resource name and its effective maximum in bytes for a
// limit failure. Names are response_headers, buffered_body, buffered_envelope,
// stream_metadata, and identity_claims. A response header or identity claims
// budget measures the documented charge; raw claims also have a wire-length
// limit. Neither budget measures heap use. buffered_body covers base64 expansion and
// the representable HEAD byte count. Other failures return "", 0, false.
func (e *InvocationError) Limit() (name string, maximum int64, ok bool) {
	if e == nil || e.limit == nil {
		return "", 0, false
	}
	return e.limit.name, e.limit.maximum, true
}

// invocationError accepts only package-owned diagnostic strings and causes.
// Never pass parser errors, application errors, or input values here. Error()
// sanitization alone would not make an unsafe Unwrap chain safe.
func invocationError(operation string, category error, detail string, causes ...error) *InvocationError {
	cause := category
	if len(causes) != 0 {
		cause = errors.Join(append([]error{category}, causes...)...)
	}
	return &InvocationError{operation: operation, detail: detail, cause: cause}
}

// limitError has the same safe-input requirements as invocationError.
func limitError(operation, name string, maximum int64, reason error) *InvocationError {
	err := invocationError(operation, ErrLimitExceeded, "resource limit exceeded", reason)
	err.limit = &invocationLimit{name: name, maximum: maximum}
	return err
}

// claimsError translates only package-owned claim failures. It deliberately
// rebuilds a safe error tree instead of wrapping the supplied error or its text.
// Gateway preparation uses this boundary for raw and typed claim construction.
func claimsError(err error) *InvocationError {
	if limit, ok := errors.AsType[*identity.ClaimsLimitError](err); ok {
		return limitError("identity", "identity_claims", limit.Maximum(), errors.Join(ErrIdentity, identity.ErrClaimsLimit))
	}
	if errors.Is(err, identity.ErrInvalidClaims) {
		return invocationError("identity", ErrIdentity, "invalid claims", identity.ErrInvalidClaims)
	}
	return invocationError("identity", ErrIdentity, "claims construction failed")
}
