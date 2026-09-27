package identity

// Kind identifies the caller's mutually exclusive identity representation.
type Kind uint8

// Caller kinds include an anonymous zero value.
const (
	KindAnonymous Kind = iota
	KindJWT
	KindIAM
)

// Source records the producer's assertion, not cryptographic evidence.
// Trusted application code must authenticate inputs before attributing a source.
type Source uint8

// Sources distinguish gateway assertions, verified tokens, custom mappings and IAM proofs verified online by STS.
// None is reserved for anonymous callers.
const (
	SourceNone Source = iota
	SourceGatewayAssertion
	SourceLocallyVerifiedToken
	SourceCustomAssertion
	SourceVerifiedIAMProof
)

// Caller is an immutable identity snapshot with exactly one JWT or IAM view.
// Its zero value is anonymous.
// Constructors validate facts;
// they neither authenticate nor authorize.
// Copies may be shared concurrently.
// There is no identity equality contract or implicit JSON export.
// Diagnostics omit facts.
type Caller struct {
	source Source
	jwt    *JWT
	iam    *IAM
}

// Kind returns the identity representation, or [KindAnonymous] for the zero value.
func (c Caller) Kind() Kind {
	if c.jwt != nil {
		return KindJWT
	}
	if c.iam != nil {
		return KindIAM
	}
	return KindAnonymous
}

// Source returns the attributed producer, or [SourceNone] for an anonymous caller.
func (c Caller) Source() Source { return c.source }

// JWT reports whether the caller has a JWT identity and returns an immutable view.
func (c Caller) JWT() (JWT, bool) {
	if c.jwt == nil {
		return JWT{}, false
	}
	return *c.jwt, true
}

// IAM reports whether the caller has an IAM identity and returns an immutable view.
func (c Caller) IAM() (IAM, bool) {
	if c.iam == nil {
		return IAM{}, false
	}
	return *c.iam, true
}

// String describes kind and source without exposing identifiers or claims.
func (c Caller) String() string {
	return "identity.Caller{kind:" + c.Kind().String() + ",source:" + c.source.String() + "}"
}

// GoString returns the same sanitized description as String.
func (c Caller) GoString() string { return c.String() }
