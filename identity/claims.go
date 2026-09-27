// Package identity provides immutable callers, owned claims and context transport.
// Construction validates representation;
// producers authenticate, and application policy authorizes.
// The package performs no network or signature verification.
package identity

import (
	"errors"
	"maps"
	"slices"
	"strconv"
)

const (
	defaultClaimsBudget = 256 * 1024
	maxClaimsBudget     = 6 * 1024 * 1024
	claimCharge         = 64
	maxClaimDepth       = 64
)

// ClaimKind describes a claim's available semantic type.
type ClaimKind uint8

// Claim kinds distinguish a missing/zero view from a present null.
const (
	ClaimInvalid ClaimKind = iota
	ClaimNull
	ClaimBoolean
	ClaimString
	ClaimNumber
	ClaimArray
	ClaimObject
)

// Representation describes the immediate input representation, not its original token encoding or whether an authenticating producer verified it.
type Representation uint8

// Claim representations retain the distinction between JSON, decoded values, and gateway text that may have lost its original JSON type.
const (
	RepresentationUnknown Representation = iota
	RepresentationJSON
	RepresentationDecoded
	RepresentationGatewayText
)

// Claims is an immutable object of named claims.
// Its zero value is empty.
// Views may share owned backing storage and may be read concurrently.
// Use Lookup and its checked accessors to inspect values;
// diagnostic formatting does not reveal claim names or contents.
// There is no implicit JSON export.
type Claims struct {
	members map[string]claimNode
	budget  int
	charge  int
}

// Claim is an immutable view of a value.
// The zero value has kind ClaimInvalid.
// Its representation describes the available input, not authentication strength.
type Claim struct {
	node   claimNode
	budget int
}

// cost includes this node's entire subtree.
// It permits an object view to retain the original budget while accounting only for that object's contents.
type claimNode struct {
	kind           ClaimKind
	representation Representation
	cost           uint32
	text           string
	number         float64
	array          []claimNode
	object         map[string]claimNode
}

// ClaimsOption configures claims construction.
// A nil option is invalid.
// Scalar settings apply in order, with the last assignment winning.
type ClaimsOption func(*claimsConfig)

type claimsConfig struct {
	budget int
}

// WithClaimsBudget sets the weighted resource allowance in bytes, defaulting to 256 KiB.
// Constructors reject a final setting outside 1..6 MiB.
// Each value costs 64 bytes plus object-name, string and exact-number text bytes.
// ParseClaims also limits the original JSON byte length to this maximum.
// The allowance is not an AWS quota or a heap cap.
// Nesting is limited separately to 64 containers, including the root, and cannot be disabled.
func WithClaimsBudget(bytes int) ClaimsOption {
	return func(c *claimsConfig) { c.budget = bytes }
}

func claimsBudget(opts []ClaimsOption) (int, error) {
	c := claimsConfig{budget: defaultClaimsBudget}
	for _, opt := range opts {
		if opt == nil {
			return 0, errors.New("identity: nil claims option")
		}
		opt(&c)
	}
	if c.budget <= 0 || c.budget > maxClaimsBudget {
		return 0, errors.New("identity: claims budget must be between 1 and 6291456 bytes")
	}
	return c.budget, nil
}

// Len returns the number of members in the object.
func (c Claims) Len() int { return len(c.members) }

// Names returns an independently owned, sorted slice of member names.
func (c Claims) Names() []string { return slices.Sorted(maps.Keys(c.members)) }

// Lookup reports whether name is present, including a present null value.
func (c Claims) Lookup(name string) (Claim, bool) {
	n, ok := c.members[name]
	if !ok {
		return Claim{}, false
	}
	return Claim{node: n, budget: c.budget}, true
}

// String returns a diagnostic description without claim names or values.
func (c Claims) String() string { return "identity.Claims" }

// GoString returns the same sanitized description as String.
func (c Claims) GoString() string { return c.String() }

// Kind returns the available semantic type.
func (c Claim) Kind() ClaimKind { return c.node.kind }

// Representation returns the immediate input representation.
func (c Claim) Representation() Representation { return c.node.representation }

// Text reports whether the claim is a string.
// It never converts other types.
func (c Claim) Text() (string, bool) {
	if c.node.kind != ClaimString {
		return "", false
	}
	return c.node.text, true
}

// Bool reports whether the claim is a boolean.
func (c Claim) Bool() (bool, bool) {
	if c.node.kind != ClaimBoolean {
		return false, false
	}
	return c.node.number != 0, true
}

// Array reports whether the claim is an array and returns a new slice of views.
// Replacing elements cannot mutate the original claims.
func (c Claim) Array() ([]Claim, bool) {
	if c.node.kind != ClaimArray {
		return nil, false
	}
	out := make([]Claim, len(c.node.array))
	for i, n := range c.node.array {
		out[i] = Claim{node: n, budget: c.budget}
	}
	return out, true
}

// Object reports whether the claim is an object and returns an immutable view.
func (c Claim) Object() (Claims, bool) {
	if c.node.kind != ClaimObject {
		return Claims{}, false
	}
	return Claims{members: c.node.object, budget: c.budget, charge: int(c.node.cost)}, true
}

// NumberText reports whether an exact numeric representation is available.
// JSON and json.Number preserve their supplied literal;
// native integers use decimal text.
// Supplied float32/float64 and all strings return false, even for integral values.
// Exactness describes the supplied value, not lost upstream data.
func (c Claim) NumberText() (string, bool) {
	if c.node.kind != ClaimNumber || c.node.text == "" {
		return "", false
	}
	return c.node.text, true
}

// Float64 explicitly converts a number to its nearest float64 representation.
// It reports false for nonnumbers or a conversion error;
// ordinary rounding is permitted.
// For supplied floating-point values it returns their stored value.
// Use NumberText and an exact parser when rounding is unacceptable.
func (c Claim) Float64() (float64, bool) {
	if c.node.kind != ClaimNumber {
		return 0, false
	}
	if c.node.text == "" {
		return c.node.number, true
	}
	n, err := strconv.ParseFloat(c.node.text, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// String returns a diagnostic description without claim contents.
func (c Claim) String() string { return "identity.Claim" }

// GoString returns the same sanitized description as String.
func (c Claim) GoString() string { return c.String() }
