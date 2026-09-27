package authn

import (
	"errors"
	"net/http"
	"slices"
)

// Inspectable failure categories never contain credentials or provider errors.
// Match them with [errors.Is].
// Request-time failures are wrapped in an [*Error] whose [Error.StatusCode] is noted below.
var (
	// ErrInvalidConfiguration means a constructor rejected its configuration, or a method was called on a nil or zero value or with a nil context.
	// It is returned directly, not wrapped in an [*Error].
	ErrInvalidConfiguration = errors.New("authn: invalid configuration")

	// ErrMissingCredentials means the request has no Authorization field (401 with all configured challenges).
	ErrMissingCredentials = errors.New("authn: missing credentials")

	// ErrMalformedCredentials means the Authorization field is repeated, comma-combined, empty or not valid scheme framing (400).
	ErrMalformedCredentials = errors.New("authn: malformed credentials")

	// ErrUnsupportedScheme means the credential uses a well-formed scheme that is not configured (401 with all configured challenges).
	ErrUnsupportedScheme = errors.New("authn: unsupported scheme")

	// ErrHeaderTooLarge means the Authorization field exceeds [Config.MaxAuthorizationBytes] (431, without a challenge).
	ErrHeaderTooLarge = errors.New("authn: authorization header too large")

	// ErrInvalidCredentials means the selected verifier definitely rejected the credential (401 with that scheme's challenge).
	// A [VerifyFunc] returns it to classify a rejection.
	ErrInvalidCredentials = errors.New("authn: invalid credentials")

	// ErrUnavailable means verification could not complete, for example because a dependency failed or a verifier returned an unclassified error (503, without a challenge).
	ErrUnavailable = errors.New("authn: verification unavailable")

	// ErrVerifierContract means a verifier reported success with an unacceptable caller, such as the wrong kind or source (500).
	// It indicates a composition fault, not a client error.
	ErrVerifierContract = errors.New("authn: invalid verifier result")
)

// Error describes a sanitized HTTP authentication failure.
// Inspect its category with errors.Is and its HTTP treatment with errors.AsType[*Error].
// Its private state never retains a supplied credential or an underlying provider error.
// The zero value describes an internal failure, without a challenge.
type Error struct {
	category   error
	status     int
	challenges []string
}

// Error returns a diagnostic containing only the sanitized category.
func (e *Error) Error() string { return e.Unwrap().Error() }

// Unwrap exposes only a package category or [identity.ErrConflict].
func (e *Error) Unwrap() error {
	if e == nil || e.category == nil {
		return ErrVerifierContract
	}
	return e.category
}

// StatusCode returns the recommended HTTP status, or 500 for a zero [Error].
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

// GoString returns the same sanitized diagnostic used by [Error.Error].
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
