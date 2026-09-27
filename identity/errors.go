package identity

import "errors"

var (
	// ErrInvalidCaller indicates invalid caller facts, provenance or options.
	ErrInvalidCaller = errors.New("identity: invalid caller")
	// ErrConflict indicates an attempt to replace an established caller.
	ErrConflict = errors.New("identity: caller conflict")
	// ErrInvalidClaims indicates malformed JSON, unsupported typed values,
	// invalid UTF-8, nonfinite numbers, or excessive container nesting.
	ErrInvalidClaims = errors.New("identity: invalid claims")
	// ErrClaimsLimit indicates exhaustion of the wire or weighted byte allowance.
	ErrClaimsLimit = errors.New("identity: claims budget exceeded")
)

// ClaimsLimitError describes a claims byte allowance failure. Its diagnostics
// contain no claim names, values, or underlying parser errors.
// The zero value has an unspecified maximum of zero.
type ClaimsLimitError struct {
	maximum int64
}

// Error returns a sanitized description of the failure.
func (e *ClaimsLimitError) Error() string { return "identity: claims budget exceeded" }

// Unwrap returns ErrClaimsLimit.
func (e *ClaimsLimitError) Unwrap() error { return ErrClaimsLimit }

// Maximum returns the configured allowance in bytes, or zero for a nil error.
func (e *ClaimsLimitError) Maximum() int64 {
	if e == nil {
		return 0
	}
	return e.maximum
}

type claimBudget struct {
	maximum   int
	remaining int
}

func (b *claimBudget) take(n int) error {
	if n > b.remaining {
		return &ClaimsLimitError{maximum: int64(b.maximum)}
	}
	b.remaining -= n
	return nil
}
