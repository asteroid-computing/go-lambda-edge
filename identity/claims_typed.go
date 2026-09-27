package identity

import (
	"encoding/json" // Only the Number data type; all JSON processing uses jsontext.
	"encoding/json/jsontext"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// NewClaims validates and owns already decoded values without a JSON round trip.
// Supported recursive values are nil, bool, string, built-in integers except uintptr, finite float32/float64, json.Number, map[string]any, map[string]string, []any and []string.
// Other defined types, pointers, structs and byte slices are rejected without calling user methods.
// A nil map is an empty object;
// a typed nil nested map/slice retains its object/array kind.
// A nil interface is null.
// Inputs must not change during construction;
// afterward they may be mutated.
// Invalid input matches [ErrInvalidClaims];
// byte exhaustion is a [ClaimsLimitError].
func NewClaims(values map[string]any, opts ...ClaimsOption) (Claims, error) {
	return newTypedClaims(values, RepresentationDecoded, opts)
}

// NewTextClaims owns gateway text without parsing embedded JSON or numbers.
// All values retain [RepresentationGatewayText].
// Nil means an empty object.
// Ownership, options and errors follow [NewClaims].
func NewTextClaims(values map[string]string, opts ...ClaimsOption) (Claims, error) {
	return newTypedClaims(values, RepresentationGatewayText, opts)
}

func newTypedClaims(values any, representation Representation, opts []ClaimsOption) (Claims, error) {
	maximum, err := claimsBudget(opts)
	if err != nil {
		return Claims{}, err
	}
	budget := claimBudget{maximum: maximum, remaining: maximum}
	if _, err := readTypedClaim(values, representation, &budget, 0, false); err != nil {
		return Claims{}, err
	}
	budget.remaining = maximum
	n, err := readTypedClaim(values, representation, &budget, 0, true)
	if err != nil {
		return Claims{}, err
	}
	return Claims{members: n.object, budget: maximum, charge: int(n.cost)}, nil
}

// readTypedClaim preflights without copying when build is false.
// Container depth bounds cycles;
// each occurrence of a shared subtree consumes its own allowance.
func readTypedClaim(v any, representation Representation, budget *claimBudget, depth int, build bool) (claimNode, error) {
	start := budget.remaining
	if err := budget.take(claimCharge); err != nil {
		return claimNode{}, err
	}
	n := claimNode{representation: representation}
	switch v := v.(type) {
	case nil:
		n.kind = ClaimNull
	case bool:
		n.kind = ClaimBoolean
		if v {
			n.number = 1
		}
	case string:
		n.kind, n.text = ClaimString, v
	case json.Number:
		n.kind, n.text = ClaimNumber, string(v)
		if err := budget.take(len(n.text)); err != nil {
			return claimNode{}, err
		}
		if !validNumber(n.text) {
			return claimNode{}, ErrInvalidClaims
		}
		if build {
			n.text = strings.Clone(n.text)
		}
		n.cost = uint32(start - budget.remaining)
		return n, nil
	case int:
		n.kind, n.text = ClaimNumber, strconv.FormatInt(int64(v), 10)
	case int8:
		n.kind, n.text = ClaimNumber, strconv.FormatInt(int64(v), 10)
	case int16:
		n.kind, n.text = ClaimNumber, strconv.FormatInt(int64(v), 10)
	case int32:
		n.kind, n.text = ClaimNumber, strconv.FormatInt(int64(v), 10)
	case int64:
		n.kind, n.text = ClaimNumber, strconv.FormatInt(v, 10)
	case uint:
		n.kind, n.text = ClaimNumber, strconv.FormatUint(uint64(v), 10)
	case uint8:
		n.kind, n.text = ClaimNumber, strconv.FormatUint(uint64(v), 10)
	case uint16:
		n.kind, n.text = ClaimNumber, strconv.FormatUint(uint64(v), 10)
	case uint32:
		n.kind, n.text = ClaimNumber, strconv.FormatUint(uint64(v), 10)
	case uint64:
		n.kind, n.text = ClaimNumber, strconv.FormatUint(v, 10)
	case float32:
		n.kind, n.number = ClaimNumber, float64(v)
	case float64:
		n.kind, n.number = ClaimNumber, v
	case map[string]any:
		if err := typedContainerBudget(budget, depth, len(v)); err != nil {
			return claimNode{}, err
		}
		n.kind = ClaimObject
		if build {
			n.object = make(map[string]claimNode, len(v))
		}
		for name, value := range v {
			if err := claimNameBudget(budget, name); err != nil {
				return claimNode{}, err
			}
			child, err := readTypedClaim(value, representation, budget, depth+1, build)
			if err != nil {
				return claimNode{}, err
			}
			if build {
				n.object[strings.Clone(name)] = child
			}
		}
	case map[string]string:
		if err := typedContainerBudget(budget, depth, len(v)); err != nil {
			return claimNode{}, err
		}
		n.kind = ClaimObject
		if build {
			n.object = make(map[string]claimNode, len(v))
		}
		for name, value := range v {
			if err := claimNameBudget(budget, name); err != nil {
				return claimNode{}, err
			}
			child, err := readTypedClaim(value, representation, budget, depth+1, build)
			if err != nil {
				return claimNode{}, err
			}
			if build {
				n.object[strings.Clone(name)] = child
			}
		}
	case []any:
		if err := typedContainerBudget(budget, depth, len(v)); err != nil {
			return claimNode{}, err
		}
		n.kind = ClaimArray
		if build {
			n.array = make([]claimNode, len(v))
		}
		for i, value := range v {
			child, err := readTypedClaim(value, representation, budget, depth+1, build)
			if err != nil {
				return claimNode{}, err
			}
			if build {
				n.array[i] = child
			}
		}
	case []string:
		if err := typedContainerBudget(budget, depth, len(v)); err != nil {
			return claimNode{}, err
		}
		n.kind = ClaimArray
		if build {
			n.array = make([]claimNode, len(v))
		}
		for i, value := range v {
			child, err := readTypedClaim(value, representation, budget, depth+1, build)
			if err != nil {
				return claimNode{}, err
			}
			if build {
				n.array[i] = child
			}
		}
	default:
		return claimNode{}, ErrInvalidClaims
	}
	if math.IsNaN(n.number) || math.IsInf(n.number, 0) {
		return claimNode{}, ErrInvalidClaims
	}
	if err := budget.take(len(n.text)); err != nil {
		return claimNode{}, err
	}
	if !utf8.ValidString(n.text) {
		return claimNode{}, ErrInvalidClaims
	}
	if build {
		n.text = strings.Clone(n.text)
	}
	n.cost = uint32(start - budget.remaining)
	return n, nil
}

func typedContainerBudget(budget *claimBudget, depth, length int) error {
	if depth >= maxClaimDepth {
		return ErrInvalidClaims
	}
	if length > budget.remaining/claimCharge {
		return &ClaimsLimitError{maximum: int64(budget.maximum)}
	}
	return nil
}

func claimNameBudget(budget *claimBudget, name string) error {
	if err := budget.take(len(name)); err != nil {
		return err
	}
	if !utf8.ValidString(name) {
		return ErrInvalidClaims
	}
	return nil
}

func validNumber(s string) bool {
	d := jsontext.NewDecoder(strings.NewReader(s))
	token, err := d.ReadToken()
	if err != nil || token.Kind() != '0' || token.String() != s {
		return false
	}
	_, err = d.ReadToken()
	return err == io.EOF
}
