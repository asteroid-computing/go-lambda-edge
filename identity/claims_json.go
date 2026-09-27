package identity

import (
	"bytes"
	"encoding/json/jsontext"
	"io"
	"strings"
)

// ParseClaims validates exactly one JSON object and owns its semantic contents.
// It preserves numeric token text, but not whitespace, member order or string escape spelling.
// It rejects duplicate names, invalid UTF-8 and trailing values.
// Input bytes must not change during construction and are not retained afterward.
// Invalid input matches ErrInvalidClaims;
// byte exhaustion is a ClaimsLimitError.
// The representation is received JSON, not proof of original JWT fidelity.
func ParseClaims(data jsontext.Value, opts ...ClaimsOption) (Claims, error) {
	maximum, err := claimsBudget(opts)
	if err != nil {
		return Claims{}, err
	}
	if len(data) > maximum {
		return Claims{}, &ClaimsLimitError{maximum: int64(maximum)}
	}
	d := jsontext.NewDecoder(bytes.NewReader(data))
	if d.PeekKind() != '{' {
		return Claims{}, ErrInvalidClaims
	}
	budget := claimBudget{maximum: maximum, remaining: maximum}
	if _, err := readJSONClaim(d, &budget, 0, false); err != nil {
		return Claims{}, err
	}
	if _, err := d.ReadToken(); err != io.EOF {
		return Claims{}, ErrInvalidClaims
	}
	// Validation and accounting precede owned-tree allocation.
	// The decoder may allocate its own bounded wire buffer and duplicate-name tracking on pass one.
	d.Reset(bytes.NewReader(data))
	budget.remaining = maximum
	n, err := readJSONClaim(d, &budget, 0, true)
	if err != nil {
		return Claims{}, err
	}
	return Claims{members: n.object, budget: maximum, charge: int(n.cost)}, nil
}

func readJSONClaim(d *jsontext.Decoder, budget *claimBudget, depth int, build bool) (claimNode, error) {
	start := budget.remaining
	if err := budget.take(claimCharge); err != nil {
		return claimNode{}, err
	}
	token, err := d.ReadToken()
	if err != nil {
		return claimNode{}, ErrInvalidClaims
	}
	n := claimNode{representation: RepresentationJSON}
	switch token.Kind() {
	case 'n':
		n.kind = ClaimNull
	case 't', 'f':
		n.kind = ClaimBoolean
		if token.Bool() {
			n.number = 1
		}
	case '"', '0':
		n.kind = ClaimString
		if token.Kind() == '0' {
			n.kind = ClaimNumber
		}
		text := token.String()
		if err := budget.take(len(text)); err != nil {
			return claimNode{}, err
		}
		if build {
			// Token data expires at the next decoder call.
			n.text = strings.Clone(text)
		}
	case '{':
		if depth >= maxClaimDepth {
			return claimNode{}, ErrInvalidClaims
		}
		n.kind = ClaimObject
		if build {
			n.object = make(map[string]claimNode)
		}
		for d.PeekKind() != '}' {
			key, err := d.ReadToken()
			if err != nil || key.Kind() != '"' {
				return claimNode{}, ErrInvalidClaims
			}
			name := key.String()
			if err := budget.take(len(name)); err != nil {
				return claimNode{}, err
			}
			if build {
				name = strings.Clone(name)
			}
			child, err := readJSONClaim(d, budget, depth+1, build)
			if err != nil {
				return claimNode{}, err
			}
			if build {
				n.object[name] = child
			}
		}
		if _, err := d.ReadToken(); err != nil {
			return claimNode{}, ErrInvalidClaims
		}
	case '[':
		if depth >= maxClaimDepth {
			return claimNode{}, ErrInvalidClaims
		}
		n.kind = ClaimArray
		for d.PeekKind() != ']' {
			child, err := readJSONClaim(d, budget, depth+1, build)
			if err != nil {
				return claimNode{}, err
			}
			if build {
				n.array = append(n.array, child)
			}
		}
		if _, err := d.ReadToken(); err != nil {
			return claimNode{}, ErrInvalidClaims
		}
	default:
		return claimNode{}, ErrInvalidClaims
	}
	n.cost = uint32(start - budget.remaining)
	return n, nil
}
