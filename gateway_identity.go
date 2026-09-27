package edge

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"

	"github.com/aws/aws-lambda-go/events"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// Candidate discovery precedes construction, so malformed competing producers cannot disappear through validation or establish precedence by field order.
type gatewayCandidates struct {
	iam     func() (identity.Caller, error)
	jwt     func() (identity.Caller, error)
	custom  bool
	unknown bool
}

func (c gatewayCandidates) caller() (identity.Caller, error) {
	count := 0
	for _, present := range []bool{c.iam != nil, c.jwt != nil, c.custom} {
		if present {
			count++
		}
	}
	if count > 1 {
		return identity.Caller{}, invocationError(OperationIdentity, ErrIdentity, "conflicting gateway assertions", identity.ErrConflict)
	}
	if c.custom || c.unknown {
		return identity.Caller{}, invocationError(OperationIdentity, ErrIdentity, "unsupported gateway assertion", errors.ErrUnsupported)
	}
	var caller identity.Caller
	var err error
	switch {
	case c.iam != nil:
		caller, err = c.iam()
	case c.jwt != nil:
		caller, err = c.jwt()
	}
	if err != nil {
		if errors.Is(err, ErrLimitExceeded) {
			return identity.Caller{}, err // A package-owned preflight limit.
		}
		if errors.Is(err, identity.ErrInvalidCaller) && !errors.Is(err, identity.ErrClaimsLimit) {
			return identity.Caller{}, invocationError(OperationIdentity, ErrIdentity, "invalid gateway caller", identity.ErrInvalidCaller)
		}
		return identity.Caller{}, claimsError(err)
	}
	return caller, nil
}

func gatewayIAM(arn, account, user string) (identity.Caller, error) {
	var opts []identity.IAMOption
	if account != "" {
		opts = append(opts, identity.WithIAMAccountID(account))
	}
	if user != "" {
		opts = append(opts, identity.WithIAMPrincipalID(user))
	}
	return identity.NewIAM(arn, identity.SourceGatewayAssertion, opts...)
}

func v1IAMCandidate(c *gatewayCandidates, v events.APIGatewayRequestIdentity) {
	if v.UserArn != "" || v.User != "" || v.Caller != "" {
		c.iam = func() (identity.Caller, error) { return gatewayIAM(v.UserArn, v.AccountID, v.User) }
	}
}

func rawObject(raw jsontext.Value) (map[string]jsontext.Value, error) {
	if len(raw) == 0 || raw.Kind() == 'n' {
		return nil, nil
	}
	if raw.Kind() != '{' {
		return nil, identity.ErrInvalidCaller
	}
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, identity.ErrInvalidCaller
	}
	return fields, nil
}

func nonnull(raw jsontext.Value) bool { return len(raw) != 0 && raw.Kind() != 'n' }

func rawGatewayCaller(event decodedEvent, budget int) (identity.Caller, error) {
	fields, err := rawObject(event.authorizer)
	if err != nil {
		return identity.Caller{}, invocationError(OperationIdentity, ErrIdentity, "invalid authorizer object", identity.ErrInvalidCaller)
	}
	var candidates gatewayCandidates
	if event.v1 != nil {
		v1IAMCandidate(&candidates, event.v1.RequestContext.Identity)
		candidates.custom = nonnull(fields["principalId"])
		if nonnull(fields["claims"]) || nonnull(fields["scopes"]) {
			candidates.jwt = func() (identity.Caller, error) { return rawGatewayJWT(fields, budget) }
		}
		for key := range fields {
			if key != "claims" && key != "scopes" && key != "principalId" && candidates.jwt == nil {
				candidates.unknown = true
			}
		}
	} else {
		candidates.custom = nonnull(fields["lambda"])
		if nonnull(fields["jwt"]) {
			candidates.jwt = func() (identity.Caller, error) {
				jwt, err := rawObject(fields["jwt"])
				if err != nil {
					return identity.Caller{}, err
				}
				return rawGatewayJWT(jwt, budget)
			}
		}
		if nonnull(fields["iam"]) {
			candidates.iam = func() (identity.Caller, error) {
				var facts struct {
					ARN     string `json:"userArn"`
					Account string `json:"accountId"`
					User    string `json:"userId"`
				}
				if fields["iam"].Kind() != '{' {
					return identity.Caller{}, identity.ErrInvalidCaller
				}
				if err := json.Unmarshal(fields["iam"], &facts); err != nil {
					return identity.Caller{}, identity.ErrInvalidCaller
				}
				return gatewayIAM(facts.ARN, facts.Account, facts.User)
			}
		}
		for key := range fields {
			if key != "jwt" && key != "iam" && key != "lambda" && candidates.jwt == nil && candidates.iam == nil {
				candidates.unknown = true
			}
		}
	}
	return candidates.caller()
}

func rawGatewayJWT(fields map[string]jsontext.Value, budget int) (identity.Caller, error) {
	if !nonnull(fields["claims"]) || fields["claims"].Kind() != '{' {
		return identity.Caller{}, identity.ErrInvalidCaller
	}
	claims, err := identity.ParseClaims(fields["claims"], identity.WithClaimsBudget(budget))
	if err != nil {
		return identity.Caller{}, err
	}
	scopes, err := rawGatewayScopes(fields["scopes"], budget)
	if err != nil {
		return identity.Caller{}, err
	}
	return identity.NewJWT(claims, identity.SourceGatewayAssertion, identity.WithGatewayScopes(scopes))
}

// Bound scope expansion before building the slice.
// NewJWT then enforces the combined claims/scopes charge and owns strings.
// Raw scopes use no extra wire allowance: whitespace does not change their weighted collection cost.
func rawGatewayScopes(raw jsontext.Value, maximum int) ([]string, error) {
	if !nonnull(raw) {
		return nil, nil
	}
	if raw.Kind() != '[' {
		return nil, identity.ErrInvalidCaller
	}
	d := jsontext.NewDecoder(bytes.NewReader(raw))
	_, err := d.ReadToken()
	if err != nil {
		return nil, identity.ErrInvalidCaller
	}
	remaining, count := maximum-64, 0
	for d.PeekKind() != ']' {
		token, err := d.ReadToken()
		if err != nil || token.Kind() != '"' {
			return nil, identity.ErrInvalidCaller
		}
		if remaining < 64 || len(token.String()) > remaining-64 {
			return nil, gatewayScopeLimit(maximum)
		}
		remaining -= 64 + len(token.String())
		count++
	}
	if remaining < 0 {
		return nil, gatewayScopeLimit(maximum)
	}
	// The envelope decoder already checked syntax, duplicates and trailing data.
	scopes := make([]string, 0, count)
	if err := json.Unmarshal(raw, &scopes); err != nil {
		return nil, identity.ErrInvalidCaller
	}
	return scopes, nil
}

func gatewayScopeLimit(maximum int) error {
	// Keep byte-limit metadata without constructing identity's opaque error type.
	return limitError(OperationIdentity, ResourceIdentityClaims, int64(maximum), errors.Join(ErrIdentity, identity.ErrClaimsLimit))
}

func typedGatewayCaller(event decodedEvent, budget int) (identity.Caller, error) {
	var candidates gatewayCandidates
	if event.v1 != nil {
		v1IAMCandidate(&candidates, event.v1.RequestContext.Identity)
		fields := event.v1.RequestContext.Authorizer
		candidates.custom = fields["principalId"] != nil
		if fields["claims"] != nil || typedScopesPresent(fields["scopes"]) {
			candidates.jwt = func() (identity.Caller, error) {
				claims, err := typedGatewayClaims(fields["claims"], budget)
				if err != nil {
					return identity.Caller{}, err
				}
				scopes, err := typedGatewayScopes(fields["scopes"], budget)
				if err != nil {
					return identity.Caller{}, err
				}
				return identity.NewJWT(claims, identity.SourceGatewayAssertion, identity.WithGatewayScopes(scopes))
			}
		}
		for key := range fields {
			if key != "claims" && key != "scopes" && key != "principalId" && candidates.jwt == nil {
				candidates.unknown = true
			}
		}
	} else if auth := event.v2.RequestContext.Authorizer; auth != nil {
		candidates.custom = auth.Lambda != nil
		if auth.IAM != nil {
			candidates.iam = func() (identity.Caller, error) {
				return gatewayIAM(auth.IAM.UserARN, auth.IAM.AccountID, auth.IAM.UserID)
			}
		}
		if auth.JWT != nil {
			candidates.jwt = func() (identity.Caller, error) {
				claims, err := identity.NewTextClaims(auth.JWT.Claims, identity.WithClaimsBudget(budget))
				if err != nil {
					return identity.Caller{}, err
				}
				return identity.NewJWT(claims, identity.SourceGatewayAssertion, identity.WithGatewayScopes(auth.JWT.Scopes))
			}
		}
	}
	return candidates.caller()
}

func typedGatewayClaims(input any, maximum int) (identity.Claims, error) {
	switch input := input.(type) {
	case nil:
		return identity.Claims{}, identity.ErrInvalidCaller
	case map[string]any:
		return identity.NewClaims(input, identity.WithClaimsBudget(maximum))
	case map[string]string:
		// V1 callers may supply a concrete string map.
		// Bound the temporary conversion before allocating it, and retain decoded-value provenance.
		remaining := maximum - 64
		if len(input) > max(remaining, 0)/64 {
			return identity.Claims{}, gatewayScopeLimit(maximum)
		}
		for key, value := range input {
			if remaining < 64 || len(key) > remaining-64 || len(value) > remaining-64-len(key) {
				return identity.Claims{}, gatewayScopeLimit(maximum)
			}
			remaining -= 64 + len(key) + len(value)
		}
		values := make(map[string]any, len(input))
		for key, value := range input {
			values[key] = value
		}
		return identity.NewClaims(values, identity.WithClaimsBudget(maximum))
	default:
		return identity.Claims{}, identity.ErrInvalidCaller
	}
}

func typedGatewayScopes(input any, maximum int) ([]string, error) {
	switch input := input.(type) {
	case nil:
		return nil, nil
	case []string:
		return input, nil // NewJWT preflights and owns this collection.
	case []any:
		if input == nil {
			return nil, nil
		}
		remaining := maximum - 64
		if len(input) > max(remaining, 0)/64 {
			return nil, gatewayScopeLimit(maximum)
		}
		for _, value := range input {
			s, ok := value.(string)
			if !ok {
				return nil, identity.ErrInvalidCaller
			}
			if remaining < 64 || len(s) > remaining-64 {
				return nil, gatewayScopeLimit(maximum)
			}
			remaining -= 64 + len(s)
		}
		scopes := make([]string, len(input))
		for i, value := range input {
			scopes[i], _ = value.(string)
		}
		return scopes, nil
	default:
		return nil, identity.ErrInvalidCaller
	}
}

func typedScopesPresent(input any) bool {
	switch scopes := input.(type) {
	case nil:
		return false
	case []string:
		return scopes != nil
	case []any:
		return scopes != nil
	default:
		return true // A nonnull wrong-shaped field is still a candidate.
	}
}
