package authz

import (
	"context"
	"errors"
	"slices"
	"unicode/utf8"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// Authorization errors contain no request facts or callback diagnostics.
var (
	ErrInvalidConfiguration = errors.New("authz: invalid configuration")
	ErrInvalidRequest       = errors.New("authz: invalid request")
	ErrUnauthenticated      = errors.New("authz: unauthenticated")
	ErrDenied               = errors.New("authz: denied")
	ErrUnavailable          = errors.New("authz: unavailable")
)

// Request supplies facts passed by value to each check. Action must
// be nonempty UTF-8; Resource is optional UTF-8. Both compare exactly without
// normalization and have application-defined meaning. A resource-aware check
// must reject an absent Resource. Caller is explicit, not read from context.
type Request struct {
	Caller   identity.Caller
	Action   string
	Resource string
}

// CheckFunc decides whether a request matches. Return false, nil for denial;
// any error becomes ErrUnavailable, even when accompanied by true. Checks run
// synchronously, must honor ctx and must be safe for concurrent use when shared.
// The caller owns dependency deadlines. Panics propagate.
type CheckFunc func(context.Context, Request) (bool, error)

// Rule holds immutable private configuration. Copies may be shared concurrently.
// The zero value is unconfigured and never allows. Construction performs no I/O
// and never invokes callbacks. A rule is limited to depth 32 (a leaf has depth 1)
// and 1,024 expanded nodes, counting each occurrence of a shared subtree.
type Rule struct {
	node *node
}

type operation uint8

const (
	opCheck operation = iota
	opAll
	opAny
	maxDepth = 32
	maxNodes = 1024
)

type node struct {
	op       operation
	check    CheckFunc
	children []Rule
	depth    int
	nodes    int
}

// Check constructs a rule from a nonnil callback. A nil callback returns
// ErrInvalidConfiguration. Reusing a check does not deduplicate its evaluation.
func Check(check CheckFunc) (Rule, error) {
	if check == nil {
		return Rule{}, ErrInvalidConfiguration
	}
	return Rule{node: &node{op: opCheck, check: check, depth: 1, nodes: 1}}, nil
}

// All requires every child to match, evaluating left to right. It stops on the
// first nonmatch or error. Empty, unconfigured or oversized trees return
// ErrInvalidConfiguration. The input slice is copied.
func All(rules ...Rule) (Rule, error) { return combine(opAll, rules) }

// Any requires one child to match, evaluating left to right. It stops on the
// first match or error; an error never falls through to another permission path.
// Empty, unconfigured or oversized trees return ErrInvalidConfiguration. The
// input slice is copied. Mandatory guards belong in an outer All.
func Any(rules ...Rule) (Rule, error) { return combine(opAny, rules) }

func combine(op operation, rules []Rule) (Rule, error) {
	if len(rules) == 0 || len(rules) >= maxNodes {
		return Rule{}, ErrInvalidConfiguration
	}
	n := &node{op: op, depth: 1, nodes: 1}
	for _, rule := range rules {
		child := rule.node
		if child == nil || child.depth >= maxDepth || child.nodes > maxNodes-n.nodes {
			return Rule{}, ErrInvalidConfiguration
		}
		n.depth = max(n.depth, child.depth+1)
		n.nodes += child.nodes
	}
	n.children = slices.Clone(rules)
	return Rule{node: n}, nil
}

// Authorize returns nil only for an explicit allow. It checks rule/context
// configuration, cancellation, request validity and a nonanonymous caller in
// that order. No check runs for an anonymous or invalid request. Nonmatches
// return ErrDenied; all callback errors are sanitized to ErrUnavailable.
// Cancellation is checked between nodes and before returning, and returns
// ctx.Err(), never context.Cause. No results are cached or installed in context.
func (r Rule) Authorize(ctx context.Context, request Request) error {
	if r.node == nil || ctx == nil {
		return ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var allowed bool
	var err error
	switch {
	case request.Action == "" || !utf8.ValidString(request.Action) || !utf8.ValidString(request.Resource):
		err = ErrInvalidRequest
	case request.Caller.Kind() == identity.KindAnonymous:
		err = ErrUnauthenticated
	default:
		allowed, err = r.node.evaluate(ctx, request)
	}
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err != nil {
		return err
	}
	if !allowed {
		return ErrDenied
	}
	return nil
}

func (n *node) evaluate(ctx context.Context, request Request) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if n.op == opCheck {
		allowed, err := n.check(ctx, request)
		if err != nil {
			return false, ErrUnavailable
		}
		return allowed, nil
	}
	for _, child := range n.children {
		allowed, err := child.node.evaluate(ctx, request)
		if err != nil {
			return false, err
		}
		if allowed == (n.op == opAny) {
			return allowed, nil
		}
	}
	return n.op == opAll, nil
}
