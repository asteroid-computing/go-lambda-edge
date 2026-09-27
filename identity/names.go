package identity

import "strconv"

var (
	kindNames = [...]string{
		KindAnonymous: "anonymous",
		KindJWT:       "jwt",
		KindIAM:       "iam",
	}
	sourceNames = [...]string{
		SourceNone:                 "none",
		SourceGatewayAssertion:     "gateway_assertion",
		SourceLocallyVerifiedToken: "locally_verified_token",
		SourceCustomAssertion:      "custom_assertion",
		SourceVerifiedIAMProof:     "verified_iam_proof",
	}
	claimKindNames = [...]string{
		ClaimInvalid: "invalid",
		ClaimNull:    "null",
		ClaimBoolean: "boolean",
		ClaimString:  "string",
		ClaimNumber:  "number",
		ClaimArray:   "array",
		ClaimObject:  "object",
	}
	representationNames = [...]string{
		RepresentationUnknown:     "unknown",
		RepresentationJSON:        "json",
		RepresentationDecoded:     "decoded",
		RepresentationGatewayText: "gateway_text",
	}
	iamPrincipalTypeNames = [...]string{
		IAMPrincipalUnknown:       "unknown",
		IAMPrincipalRoot:          "root",
		IAMPrincipalUser:          "user",
		IAMPrincipalAssumedRole:   "assumed_role",
		IAMPrincipalFederatedUser: "federated_user",
	}
)

// enumName returns names[v], or typeName(v) for a value outside the declared set.
func enumName(names []string, typeName string, v uint8) string {
	if int(v) < len(names) {
		return names[v]
	}
	return typeName + "(" + strconv.Itoa(int(v)) + ")"
}

// String returns a stable lowercase name such as "jwt", or "Kind(n)" for an undeclared value.
func (k Kind) String() string { return enumName(kindNames[:], "Kind", uint8(k)) }

// String returns a stable lowercase name such as "verified_iam_proof", or "Source(n)" for an undeclared value.
func (s Source) String() string { return enumName(sourceNames[:], "Source", uint8(s)) }

// String returns a stable lowercase name such as "number", or "ClaimKind(n)" for an undeclared value.
func (k ClaimKind) String() string { return enumName(claimKindNames[:], "ClaimKind", uint8(k)) }

// String returns a stable lowercase name such as "gateway_text", or "Representation(n)" for an undeclared value.
func (r Representation) String() string {
	return enumName(representationNames[:], "Representation", uint8(r))
}

// String returns a stable lowercase name such as "assumed_role", or "IAMPrincipalType(n)" for an undeclared value.
func (t IAMPrincipalType) String() string {
	return enumName(iamPrincipalTypeNames[:], "IAMPrincipalType", uint8(t))
}
