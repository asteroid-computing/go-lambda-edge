package iamproof

import "errors"

// Package errors contain no credentials, signed URLs or dependency diagnostics.
var (
	ErrInvalidConfiguration   = errors.New("iamproof: invalid configuration")
	ErrInvalidProof           = errors.New("iamproof: invalid proof")
	ErrUnavailable            = errors.New("iamproof: verification unavailable")
	ErrCredentialsUnavailable = errors.New("iamproof: credentials unavailable")
	ErrProofTooLarge          = errors.New("iamproof: proof too large")
)
