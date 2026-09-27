package authn

import (
	"errors"
	"net/http"
	"slices"
)

// Inspectable failure categories never contain credentials or provider errors.
var (
	ErrInvalidConfiguration = errors.New("authn: invalid configuration")
	ErrMissingCredentials   = errors.New("authn: missing credentials")
	ErrMalformedCredentials = errors.New("authn: malformed credentials")
	ErrUnsupportedScheme    = errors.New("authn: unsupported scheme")
	ErrHeaderTooLarge       = errors.New("authn: authorization header too large")
	ErrInvalidCredentials   = errors.New("authn: invalid credentials")
	ErrUnavailable          = errors.New("authn: verification unavailable")
	ErrVerifierContract     = errors.New("authn: invalid verifier result")
)

// Error describes a sanitized HTTP authentication failure. Inspect its category
// with errors.Is and its HTTP treatment with errors.AsType[*Error]. Its private
// state never retains a supplied credential or an underlying provider error.
// The zero value describes an internal failure, without a challenge.
type Error struct {
	category   error
	status     int
	challenges []string
}

// Error returns a diagnostic containing only the sanitized category.
func (e *Error) Error() string { return e.Unwrap().Error() }

// Unwrap exposes only a package category or identity.ErrConflict.
func (e *Error) Unwrap() error {
	if e == nil || e.category == nil {
		return ErrVerifierContract
	}
	return e.category
}

// StatusCode returns the recommended HTTP status, or 500 for a zero Error.
func (e *Error) StatusCode() int {
	if e == nil || e.status == 0 {
		return http.StatusInternalServerError
	}
	return e.status
}

// Challenges returns an owned copy of complete WWW-Authenticate values.
func (e *Error) Challenges() []string {
	if e == nil {
		return nil
	}
	return slices.Clone(e.challenges)
}

// GoString returns the same sanitized diagnostic used by Error.
func (e *Error) GoString() string { return e.Error() }

func (a *Authenticator) failure(category error, status int, selected scheme) *Error {
	e := &Error{category: category, status: status}
	if status != http.StatusBadRequest && status != http.StatusUnauthorized {
		return e
	}
	if category == ErrInvalidCredentials {
		if selected == bearer {
			e.challenges = []string{a.bearerChallenge + `, error="invalid_token"`}
		} else {
			e.challenges = []string{a.iamChallenge}
		}
		return e
	}
	if a.bearerChallenge != "" {
		challenge := a.bearerChallenge
		if category == ErrMalformedCredentials && selected == bearer {
			challenge += `, error="invalid_request"`
		}
		e.challenges = append(e.challenges, challenge)
	}
	if a.iamChallenge != "" {
		e.challenges = append(e.challenges, a.iamChallenge)
	}
	return e
}
