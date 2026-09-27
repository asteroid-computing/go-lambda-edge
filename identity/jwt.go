package identity

import (
	"errors"
	"slices"
	"strings"
)

// JWT is an immutable view of normalized JWT facts and their owned claims.
// It makes no assertions about token validity, flow, expiry or permissions.
// Its zero value contains no identity facts.
type JWT struct {
	claims   Claims
	issuer   string
	subject  string
	clientID string
	tokenUse string
	audience stringSet
	scopes   stringSet
	groups   stringSet
}

type stringSet struct {
	values []string
	known  bool
}

// JWTOption supplies a separate assertion.
// Nil or duplicate options are invalid.
type JWTOption func(*jwtConfig) error

type jwtConfig struct {
	scopes    []string
	scopesSet bool
}

// WithGatewayScopes supplies API Gateway's dedicated scopes collection.
// It is valid only with SourceGatewayAssertion.
// Nil means unavailable;
// a nonnil empty slice means known empty.
// Interpretable claim scopes must agree as exact sets.
// The supplied collection consumes the claims' remaining weighted budget before copying, even when its values duplicate a claim.
// Do not mutate it during NewJWT.
func WithGatewayScopes(scopes []string) JWTOption {
	return func(c *jwtConfig) error {
		if c.scopesSet {
			return ErrInvalidCaller
		}
		c.scopesSet, c.scopes = true, scopes
		return nil
	}
}

// NewJWT validates normalized facts and retains immutable claims.
// It requires a nonempty issuer and a subject or client_id, without conflating those fields.
// Allowed sources are gateway, locally verified token and custom assertion.
// It does not verify signatures or expiry.
// Invalid inputs match ErrInvalidCaller;
// a dedicated-scope budget failure additionally matches ErrClaimsLimit.
func NewJWT(claims Claims, source Source, opts ...JWTOption) (Caller, error) {
	if source != SourceGatewayAssertion && source != SourceLocallyVerifiedToken && source != SourceCustomAssertion {
		return Caller{}, ErrInvalidCaller
	}
	var cfg jwtConfig
	for _, opt := range opts {
		if opt == nil {
			return Caller{}, ErrInvalidCaller
		}
		if err := opt(&cfg); err != nil {
			return Caller{}, err
		}
	}
	if cfg.scopesSet && source != SourceGatewayAssertion {
		return Caller{}, ErrInvalidCaller
	}
	j := JWT{claims: claims}
	for _, fact := range []struct {
		name string
		dest *string
	}{{"iss", &j.issuer}, {"sub", &j.subject}, {"client_id", &j.clientID}, {"token_use", &j.tokenUse}} {
		v, present := claims.Lookup(fact.name)
		if !present {
			continue
		}
		s, ok := v.Text()
		if !ok || s == "" {
			return Caller{}, ErrInvalidCaller
		}
		*fact.dest = s
	}
	if j.issuer == "" || j.subject == "" && j.clientID == "" {
		return Caller{}, ErrInvalidCaller
	}
	var err error
	j.audience, err = claimStrings(claims, "aud", source, true, false)
	if err != nil {
		return Caller{}, err
	}
	j.groups, err = claimStrings(claims, "cognito:groups", source, false, false)
	if err != nil {
		return Caller{}, err
	}
	j.scopes, err = claimStrings(claims, "scp", source, false, true)
	if err != nil {
		return Caller{}, err
	}
	if v, present := claims.Lookup("scope"); present {
		s, ok := v.Text()
		if !ok || s == "" {
			return Caller{}, ErrInvalidCaller
		}
		// Validate before allocating;
		// whitespace is specifically ASCII SP.
		for part := range strings.SplitSeq(s, " ") {
			if !validScope(part) {
				return Caller{}, ErrInvalidCaller
			}
		}
		parsed := normalizedSet(strings.Split(s, " "))
		j.scopes, err = consistentScopes(j.scopes, parsed)
		if err != nil {
			return Caller{}, err
		}
	}
	if cfg.scopes != nil {
		budget := claimBudget{maximum: claims.budget, remaining: claims.budget - claims.charge}
		if err := budget.take(claimCharge); err != nil {
			return Caller{}, errors.Join(ErrInvalidCaller, err)
		}
		if len(cfg.scopes) > budget.remaining/claimCharge {
			return Caller{}, errors.Join(ErrInvalidCaller, &ClaimsLimitError{maximum: int64(budget.maximum)})
		}
		for _, scope := range cfg.scopes {
			if err := budget.take(claimCharge); err != nil {
				return Caller{}, errors.Join(ErrInvalidCaller, err)
			}
			if err := budget.take(len(scope)); err != nil {
				return Caller{}, errors.Join(ErrInvalidCaller, err)
			}
			if !validScope(scope) {
				return Caller{}, ErrInvalidCaller
			}
		}
		scopes := make([]string, len(cfg.scopes))
		for i, scope := range cfg.scopes {
			scopes[i] = strings.Clone(scope)
		}
		j.scopes, err = consistentScopes(j.scopes, normalizedSet(scopes))
		if err != nil {
			return Caller{}, err
		}
	}
	return Caller{source: source, jwt: &j}, nil
}

func claimStrings(claims Claims, name string, source Source, allowString, scope bool) (stringSet, error) {
	v, present := claims.Lookup(name)
	if !present {
		return stringSet{}, nil
	}
	if s, ok := v.Text(); ok {
		if source == SourceGatewayAssertion || v.Representation() == RepresentationGatewayText {
			return stringSet{}, nil
		}
		if allowString && s != "" {
			return stringSet{values: []string{s}, known: true}, nil
		}
		return stringSet{}, ErrInvalidCaller
	}
	if v.Kind() != ClaimArray {
		return stringSet{}, ErrInvalidCaller
	}
	values := make([]string, len(v.node.array))
	for i, n := range v.node.array {
		if n.kind != ClaimString || n.text == "" || scope && !validScope(n.text) {
			return stringSet{}, ErrInvalidCaller
		}
		values[i] = n.text
	}
	return normalizedSet(values), nil
}

func normalizedSet(values []string) stringSet {
	slices.Sort(values)
	return stringSet{values: slices.Compact(values), known: true}
}

func consistentScopes(a, b stringSet) (stringSet, error) {
	if a.known && !slices.Equal(a.values, b.values) {
		return stringSet{}, ErrInvalidCaller
	}
	return b, nil
}

// RFC 6749 scope-token = 1*(%x21 / %x23-5B / %x5D-7E).
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

// Claims returns an immutable view of the original supplied claims.
func (j JWT) Claims() Claims { return j.claims }

// Issuer returns the exact issuer, or empty for a zero JWT.
func (j JWT) Issuer() string { return j.issuer }

// Subject reports whether a nonempty subject was supplied.
func (j JWT) Subject() (string, bool) { return j.subject, j.subject != "" }

// ClientID reports whether a nonempty client_id was supplied.
func (j JWT) ClientID() (string, bool) { return j.clientID, j.clientID != "" }

// TokenUse reports whether a nonempty token_use was supplied.
func (j JWT) TokenUse() (string, bool) { return j.tokenUse, j.tokenUse != "" }

// Audience returns a copied sorted set and reports whether its meaning is known.
// Gateway string audiences remain available in Claims but are not split or parsed.
func (j JWT) Audience() ([]string, bool) { return slices.Clone(j.audience.values), j.audience.known }

// Scopes returns a copied sorted set, distinguishing known empty from unavailable.
func (j JWT) Scopes() ([]string, bool) { return slices.Clone(j.scopes.values), j.scopes.known }

// CognitoGroups returns a copied sorted set of cognito:groups when interpretable.
// It does not promote arbitrary groups or roles claims into Cognito groups.
func (j JWT) CognitoGroups() ([]string, bool) { return slices.Clone(j.groups.values), j.groups.known }

// String returns a diagnostic description without identity facts.
func (j JWT) String() string { return "identity.JWT" }

// GoString returns the same sanitized description as String.
func (j JWT) GoString() string { return j.String() }
