// Package actionheader selects one application action from a configured HTTP request header.
// It uses only the standard library, so native HTTP servers and Lambda adapters can share the same selection rules.
// Consumers own action metadata, registry lookup, authorization and HTTP responses.
package actionheader

import (
	"errors"
	"net/http"
	"strings"
)

var (
	// ErrActionMissing means the configured header has no represented values.
	// An absent header and a nil or empty value slice are all missing.
	ErrActionMissing = errors.New("actionheader: missing action")

	// ErrActionAmbiguous means the header has multiple represented values or a value containing a comma.
	// Gateway payload 2.0 combines duplicates with commas;
	// a literal comma cannot be distinguished from combined values.
	ErrActionAmbiguous = errors.New("actionheader: ambiguous action")

	// ErrActionInvalid means the single action value is empty after trimming outer spaces and tabs, or does not satisfy the HTTP token grammar.
	ErrActionInvalid = errors.New("actionheader: invalid action")
)

// Selector selects an action from a configured request header.
// Construct it with [NewSelector];
// its zero value is unconfigured.
// A Selector is immutable, safe to copy, and safe for concurrent use.
type Selector struct {
	name string
}

// NewSelector validates name as an HTTP field name and returns a selector.
// The name is required and matched case-insensitively;
// no default is inferred.
// On error, the returned selector is the zero value.
// Configuration errors are separate from [ErrActionMissing], [ErrActionAmbiguous] and [ErrActionInvalid].
func NewSelector(name string) (Selector, error) {
	if !validToken(name) {
		return Selector{}, errors.New("actionheader: invalid header name")
	}
	return Selector{name: http.CanonicalHeaderKey(name)}, nil
}

// Parse returns the single action in headers, trimming only outer SP/HTAB and preserving case.
// An action must be a nonempty HTTP token (RFC 9110 §5.6.2).
// Parse inspects all case-insensitive aliases, including noncanonical map keys.
// Multiple values are ambiguous even if identical;
// a comma is also ambiguous.
// Multiplicity and comma checks take precedence over token validation.
// Duplicates already lost by a gateway or earlier proxy cannot be detected.
//
// On failure Parse returns an empty string and [ErrActionMissing], [ErrActionAmbiguous] or [ErrActionInvalid], matchable with [errors.Is].
// An unconfigured selector instead returns a configuration error.
// Errors do not include supplied header values.
// Parse does not modify headers or write an HTTP response.
// Callers must not mutate headers concurrently with Parse.
// Select once and use that same action for authorization and execution.
func (s Selector) Parse(headers http.Header) (string, error) {
	if s.name == "" {
		return "", errors.New("actionheader: unconfigured selector")
	}
	var value string
	found := false
	for name, values := range headers {
		// CanonicalHeaderKey folds only valid ASCII field names.
		// In particular, Unicode case equivalents must not alias the configured HTTP name.
		if http.CanonicalHeaderKey(name) != s.name || len(values) == 0 {
			continue
		}
		if found || len(values) > 1 {
			return "", ErrActionAmbiguous
		}
		value, found = values[0], true
	}
	if !found {
		return "", ErrActionMissing
	}
	if strings.Contains(value, ",") {
		return "", ErrActionAmbiguous
	}
	value = strings.Trim(value, " \t")
	if !validToken(value) {
		return "", ErrActionInvalid
	}
	return value, nil
}

// validToken reports whether s is a nonempty HTTP token (RFC 9110 §5.6.2).
func validToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return true
}
