package authz

import (
	"context"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// Sources matches any supplied nonanonymous producer.
// Empty lists, SourceNone and unknown sources return [ErrInvalidConfiguration].
// Source is trusted-code attribution, not an unforgeable credential.
// Combine this broad predicate with permission checks.
// The input slice is copied.
func Sources(allowed ...identity.Source) (Rule, error) {
	if len(allowed) == 0 {
		return Rule{}, ErrInvalidConfiguration
	}
	for _, source := range allowed {
		switch source {
		case identity.SourceGatewayAssertion, identity.SourceLocallyVerifiedToken, identity.SourceCustomAssertion, identity.SourceVerifiedIAMProof:
		default:
			return Rule{}, ErrInvalidConfiguration
		}
	}
	sources := slices.Clone(allowed)
	return Check(func(_ context.Context, r Request) (bool, error) {
		return slices.Contains(sources, r.Caller.Source()), nil
	})
}

// JWTSubject matches an exact issuer and subject.
// Both must be nonempty UTF-8 or construction returns [ErrInvalidConfiguration].
// A subject does not prove a human identity.
// Wrong kind or missing subject does not match.
func JWTSubject(issuer, subject string) (Rule, error) {
	if !validIdentifier(issuer) || !validIdentifier(subject) {
		return Rule{}, ErrInvalidConfiguration
	}
	return Check(func(_ context.Context, r Request) (bool, error) {
		j, ok := r.Caller.JWT()
		value, present := j.Subject()
		return ok && j.Issuer() == issuer && present && value == subject, nil
	})
}

// JWTClient matches an exact issuer and client_id, without interpreting audience or subject as a client ID or inferring a machine grant.
// Both arguments must be nonempty UTF-8 or construction returns [ErrInvalidConfiguration].
func JWTClient(issuer, clientID string) (Rule, error) {
	if !validIdentifier(issuer) || !validIdentifier(clientID) {
		return Rule{}, ErrInvalidConfiguration
	}
	return Check(func(_ context.Context, r Request) (bool, error) {
		j, ok := r.Caller.JWT()
		value, present := j.ClientID()
		return ok && j.Issuer() == issuer && present && value == clientID, nil
	})
}

// JWTScopes requires all supplied exact OAuth scope tokens from the exact issuer.
// It does not interpret wildcards.
// Missing or unavailable scopes do not match.
// Issuer must be nonempty UTF-8 and required must contain at least one valid RFC 6749 scope token, otherwise construction returns [ErrInvalidConfiguration].
// The input slice is copied.
// Use Any for alternative scope requirements.
func JWTScopes(issuer string, required ...string) (Rule, error) {
	return jwtSet(issuer, required, identity.JWT.Scopes, validScope)
}

// CognitoGroups requires all supplied exact cognito:groups values from the exact issuer.
// Flattened gateway strings remain unavailable and never match.
// Groups are distinct from scopes and application grants.
// Issuer and each required value must be nonempty UTF-8;
// an empty list or invalid value returns [ErrInvalidConfiguration].
// The input slice is copied.
func CognitoGroups(issuer string, required ...string) (Rule, error) {
	return jwtSet(issuer, required, identity.JWT.CognitoGroups, validIdentifier)
}

func jwtSet(issuer string, required []string, values func(identity.JWT) ([]string, bool), valid func(string) bool) (Rule, error) {
	if !validIdentifier(issuer) || len(required) == 0 {
		return Rule{}, ErrInvalidConfiguration
	}
	for _, value := range required {
		if !valid(value) {
			return Rule{}, ErrInvalidConfiguration
		}
	}
	want := slices.Clone(required)
	slices.Sort(want)
	want = slices.Compact(want)
	return Check(func(_ context.Context, r Request) (bool, error) {
		j, ok := r.Caller.JWT()
		if !ok || j.Issuer() != issuer {
			return false, nil
		}
		got, known := values(j)
		if !known {
			return false, nil
		}
		for _, value := range want {
			if _, found := slices.BinarySearch(got, value); !found {
				return false, nil
			}
		}
		return true, nil
	})
}

func validIdentifier(s string) bool { return s != "" && utf8.ValidString(s) }

// RFC 6749 scope-token = 1*(%x21 / %x23-5B / %x5D-7E), as in identity.
func validScope(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		b := s[i]
		if b < 0x21 || b > 0x7e || b == '"' || b == '\\' {
			return false
		}
	}
	return true
}

// IAMPrincipal matches the full caller ARN literally, including path and session.
// A root ARN matches only that root caller, not the account.
// Valid literal * and ? in user paths retain their meaning as characters.
// Unsupported caller forms (including bare role ARNs) return [ErrInvalidConfiguration], following NewIAM's grammar.
// This does not evaluate AWS IAM permissions or bind a unique identity across deletion and recreation of the principal.
func IAMPrincipal(principalARN string) (Rule, error) {
	if _, err := identity.NewIAM(principalARN, identity.SourceCustomAssertion); err != nil {
		return Rule{}, ErrInvalidConfiguration
	}
	return Check(func(_ context.Context, r Request) (bool, error) {
		i, ok := r.Caller.IAM()
		return ok && i.PrincipalARN() == principalARN, nil
	})
}

// IAMRoleSessions matches assumed-role callers with the exact partition, account and role name, allowing any valid session name.
// Invalid components, wildcards and role paths return [ErrInvalidConfiguration].
// This is a role-name policy: recreating the same role name can match again.
// It does not bind a unique role ID, identify the original actor, fabricate a role ARN or evaluate IAM policies.
func IAMRoleSessions(partition, accountID, roleName string) (Rule, error) {
	prefix := "arn:" + partition + ":sts::" + accountID + ":assumed-role/" + roleName + "/"
	// Validate a sample session with the identity grammar rather than maintaining a second ARN parser.
	// Cross-check components to exclude delimiter injection.
	caller, err := identity.NewIAM(prefix+"validation", identity.SourceCustomAssertion)
	i, ok := caller.IAM()
	if err != nil || !ok || i.PrincipalType() != identity.IAMPrincipalAssumedRole || i.Partition() != partition || i.AccountID() != accountID {
		return Rule{}, ErrInvalidConfiguration
	}
	return Check(func(_ context.Context, r Request) (bool, error) {
		i, ok := r.Caller.IAM()
		return ok && i.PrincipalType() == identity.IAMPrincipalAssumedRole && strings.HasPrefix(i.PrincipalARN(), prefix), nil
	})
}
