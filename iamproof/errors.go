package iamproof

import "errors"

// Package errors contain no credentials, signed URLs or dependency diagnostics.
// Match them with [errors.Is].
var (
	// ErrInvalidConfiguration means [NewGenerator] or [NewVerifier] rejected its arguments or options, or a method was called on a nil or zero value or with a nil context.
	ErrInvalidConfiguration = errors.New("iamproof: invalid configuration")

	// ErrInvalidProof means [Verifier.Verify] rejected a malformed, expired, misdirected or STS-rejected proof.
	// When wiring the verifier into authn, map it to authn.ErrInvalidCredentials.
	ErrInvalidProof = errors.New("iamproof: invalid proof")

	// ErrUnavailable means [Verifier.Verify] could not reach a verdict because of a dependency failure, an internal timeout or an unexpected STS response.
	ErrUnavailable = errors.New("iamproof: verification unavailable")

	// ErrCredentialsUnavailable means [Generator.Generate] could not obtain usable credentials from its provider or could not presign with them.
	ErrCredentialsUnavailable = errors.New("iamproof: credentials unavailable")

	// ErrProofTooLarge means [Generator.Generate] produced a proof larger than the protocol's 8 KiB bound.
	// Credentials are never truncated to fit.
	ErrProofTooLarge = errors.New("iamproof: proof too large")
)
