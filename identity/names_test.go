package identity_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func TestEnumerationStrings(t *testing.T) {
	for _, tt := range []struct {
		value fmt.Stringer
		want  string
	}{
		{identity.KindAnonymous, "anonymous"},
		{identity.KindJWT, "jwt"},
		{identity.KindIAM, "iam"},
		{identity.Kind(3), "Kind(3)"},
		{identity.SourceNone, "none"},
		{identity.SourceGatewayAssertion, "gateway_assertion"},
		{identity.SourceLocallyVerifiedToken, "locally_verified_token"},
		{identity.SourceCustomAssertion, "custom_assertion"},
		{identity.SourceVerifiedIAMProof, "verified_iam_proof"},
		{identity.Source(255), "Source(255)"},
		{identity.ClaimInvalid, "invalid"},
		{identity.ClaimNull, "null"},
		{identity.ClaimBoolean, "boolean"},
		{identity.ClaimString, "string"},
		{identity.ClaimNumber, "number"},
		{identity.ClaimArray, "array"},
		{identity.ClaimObject, "object"},
		{identity.ClaimKind(7), "ClaimKind(7)"},
		{identity.RepresentationUnknown, "unknown"},
		{identity.RepresentationJSON, "json"},
		{identity.RepresentationDecoded, "decoded"},
		{identity.RepresentationGatewayText, "gateway_text"},
		{identity.Representation(4), "Representation(4)"},
		{identity.IAMPrincipalUnknown, "unknown"},
		{identity.IAMPrincipalRoot, "root"},
		{identity.IAMPrincipalUser, "user"},
		{identity.IAMPrincipalAssumedRole, "assumed_role"},
		{identity.IAMPrincipalFederatedUser, "federated_user"},
		{identity.IAMPrincipalType(5), "IAMPrincipalType(5)"},
	} {
		if got := tt.value.String(); got != tt.want {
			t.Errorf("%T(%d).String() = %q, want %q", tt.value, tt.value, got, tt.want)
		}
		if got := fmt.Sprintf("%v", tt.value); got != tt.want {
			t.Errorf("fmt %%v of %T = %q, want %q", tt.value, got, tt.want)
		}
	}
}

func TestCallerStringNamesKindAndSourceOnly(t *testing.T) {
	caller, err := identity.NewIAM("arn:aws:iam::123456789012:user/Alice", identity.SourceVerifiedIAMProof)
	if err != nil {
		t.Fatal(err)
	}
	const want = "identity.Caller{kind:iam,source:verified_iam_proof}"
	if got := caller.String(); got != want {
		t.Errorf("Caller.String() = %q, want %q", got, want)
	}
	if got := fmt.Sprintf("%#v", caller); got != want || strings.Contains(got, "Alice") || strings.Contains(got, "123456789012") {
		t.Errorf("Caller GoString = %q, want %q without identifiers", got, want)
	}
	if got, want := (identity.Caller{}).String(), "identity.Caller{kind:anonymous,source:none}"; got != want {
		t.Errorf("anonymous Caller.String() = %q, want %q", got, want)
	}
}
