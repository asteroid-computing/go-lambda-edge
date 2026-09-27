package identity_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func TestIAMCallerForms(t *testing.T) {
	for _, tc := range []struct {
		name, arn, partition, service string
		form                          identity.IAMPrincipalType
	}{
		{name: "root", arn: "arn:aws:iam::123456789012:root", partition: "aws", service: "iam", form: identity.IAMPrincipalRoot},
		{name: "user", arn: "arn:aws:iam::123456789012:user/engineering/Alice", partition: "aws", service: "iam", form: identity.IAMPrincipalUser},
		{name: "literal_path", arn: "arn:aws:iam::123456789012:user/team*?:x/%2F/Alice", partition: "aws", service: "iam", form: identity.IAMPrincipalUser},
		{name: "gov_session", arn: "arn:aws-us-gov:sts::123456789012:assumed-role/Operator/alice@example.com", partition: "aws-us-gov", service: "sts", form: identity.IAMPrincipalAssumedRole},
		{name: "china_federation", arn: "arn:aws-cn:sts::123456789012:federated-user/Bob", partition: "aws-cn", service: "sts", form: identity.IAMPrincipalFederatedUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, source := range []identity.Source{identity.SourceGatewayAssertion, identity.SourceCustomAssertion, identity.SourceVerifiedIAMProof} {
				c, err := identity.NewIAM(tc.arn, source, identity.WithIAMAccountID("123456789012"), identity.WithIAMPrincipalID("opaque:session"))
				if err != nil {
					t.Fatal(err)
				}
				i, ok := c.IAM()
				if !ok || c.Kind() != identity.KindIAM || c.Source() != source {
					t.Fatalf("IAM caller = %v, IAM present = %t", c, ok)
				}
				if _, ok := c.JWT(); ok {
					t.Error("IAM caller also contains JWT")
				}
				if i.PrincipalARN() != tc.arn || i.Partition() != tc.partition || i.Service() != tc.service || i.AccountID() != "123456789012" || i.PrincipalType() != tc.form {
					t.Error("IAM accessors did not preserve the supplied caller facts")
				}
				if id, ok := i.PrincipalID(); !ok || id != "opaque:session" {
					t.Errorf("PrincipalID() = %q, %t", id, ok)
				}
			}
		})
	}
}

func TestIAMRejectsInvalidCallers(t *testing.T) {
	for _, resource := range []string{
		"role/Operator",
		"user/*",
		"user/A?ice",
		"user/",
		"user//Alice",
		"user/team name/Alice",
		"user/team\x7f/Alice",
		"user/Alice/",
		"user/" + strings.Repeat("a", 65),
		"user/" + strings.Repeat("a", 511) + "/Alice",
		"assumed-role/Operator/a",
		"assumed-role/Operator/session/extra",
		"assumed-role/*/session",
		"assumed-role/Operator/" + strings.Repeat("x", 65),
		"federated-user/x",
		"federated-user/" + strings.Repeat("x", 33),
	} {
		for _, service := range []string{"iam", "sts"} {
			c, err := identity.NewIAM("arn:aws:"+service+"::123456789012:"+resource, identity.SourceGatewayAssertion)
			if !errors.Is(err, identity.ErrInvalidCaller) || c.Kind() != identity.KindAnonymous {
				t.Errorf("NewIAM(%q, %q) = %v, %v; want anonymous/invalid", service, resource, c, err)
			}
		}
	}
	for _, arn := range []string{"", "123456789012", "arn:aws:iam:eu-west-2:123456789012:root", "arn:AWS:iam::123456789012:root", "arn:aws:iam::*:root", "arn:aws:iam::12345678901x:root", "arn:aws:iam::123456789012:root:extra"} {
		if _, err := identity.NewIAM(arn, identity.SourceGatewayAssertion); !errors.Is(err, identity.ErrInvalidCaller) {
			t.Errorf("NewIAM(%q) = %v, want invalid", arn, err)
		}
	}
	for _, opts := range [][]identity.IAMOption{
		{nil},
		{identity.WithIAMAccountID("000000000000")},
		{identity.WithIAMPrincipalID("")},
		{identity.WithIAMPrincipalID("\xff")},
		{identity.WithIAMPrincipalID("id"), identity.WithIAMPrincipalID("id")},
		{identity.WithIAMAccountID("123456789012"), identity.WithIAMAccountID("123456789012")},
	} {
		if _, err := identity.NewIAM("arn:aws:iam::123456789012:root", identity.SourceGatewayAssertion, opts...); !errors.Is(err, identity.ErrInvalidCaller) {
			t.Errorf("NewIAM with invalid/duplicate options = %v", err)
		}
	}
}

func TestJWTFactsAndOwnership(t *testing.T) {
	claims := parseClaims(t, `{"iss":"https://issuer/Exact","sub":"subject","client_id":"client","token_use":"access","aud":["b","a","b"],"scope":"write read read","scp":["read","write"],"cognito:groups":["Ops","Ops"],"custom":{"x":1}}`)
	caller, err := identity.NewJWT(claims, identity.SourceLocallyVerifiedToken)
	if err != nil {
		t.Fatal(err)
	}
	j, ok := caller.JWT()
	if !ok || caller.Kind() != identity.KindJWT || caller.Source() != identity.SourceLocallyVerifiedToken {
		t.Fatalf("NewJWT = %v, JWT present=%t", caller, ok)
	}
	if _, ok := caller.IAM(); ok {
		t.Error("JWT caller also contains IAM")
	}
	if j.Issuer() != "https://issuer/Exact" {
		t.Errorf("Issuer() = %q", j.Issuer())
	}
	for name, read := range map[string]func() (string, bool){"subject": j.Subject, "client": j.ClientID, "access": j.TokenUse} {
		if value, ok := read(); !ok || value != name {
			t.Errorf("scalar accessor = %q, %t; want %q, true", value, ok, name)
		}
	}
	for _, tc := range []struct {
		read func() ([]string, bool)
		want []string
	}{{j.Audience, []string{"a", "b"}}, {j.Scopes, []string{"read", "write"}}, {j.CognitoGroups, []string{"Ops"}}} {
		values, known := tc.read()
		if !known || !slices.Equal(values, tc.want) {
			t.Errorf("collection = %v, %t; want %v, true", values, known, tc.want)
		}
		values[0] = "mutated"
		again, _ := tc.read()
		if !slices.Equal(again, tc.want) {
			t.Error("returned slice mutated identity")
		}
	}
	if j.Claims().Len() != claims.Len() {
		t.Error("normalized view lost original claims")
	}
}

func TestJWTInvalidAndUnavailableFacts(t *testing.T) {
	for _, input := range []string{
		`{}`,
		`{"iss":"issuer"}`,
		`{"iss":1,"sub":"x"}`,
		`{"iss":"issuer","sub":""}`,
		`{"iss":"issuer","sub":"x","client_id":null}`,
		`{"iss":"issuer","sub":"x","token_use":false}`,
		`{"iss":"issuer","sub":"x","aud":[1]}`,
		`{"iss":"issuer","sub":"x","cognito:groups":"[Ops]"}`,
		`{"iss":"issuer","sub":"x","scope":"read  write"}`,
		`{"iss":"issuer","sub":"x","scope":"read\twrite"}`,
		`{"iss":"issuer","sub":"x","scope":"read write"}`,
		`{"iss":"issuer","sub":"x","scope":""}`,
		`{"iss":"issuer","sub":"x","scope":"read","scp":["write"]}`,
		`{"iss":"issuer","sub":"x","scp":"read"}`,
	} {
		c, err := identity.NewJWT(parseClaims(t, input), identity.SourceLocallyVerifiedToken)
		if !errors.Is(err, identity.ErrInvalidCaller) || c.Kind() != identity.KindAnonymous {
			t.Errorf("NewJWT(%s) = %v, %v; want anonymous/invalid", input, c, err)
		}
	}
	for _, source := range []identity.Source{identity.SourceNone, identity.SourceVerifiedIAMProof, 255} {
		if _, err := identity.NewJWT(parseClaims(t, `{"iss":"issuer","sub":"x"}`), source); !errors.Is(err, identity.ErrInvalidCaller) {
			t.Errorf("NewJWT source %d = %v", source, err)
		}
	}
	for _, source := range []identity.Source{identity.SourceNone, identity.SourceLocallyVerifiedToken, 255} {
		if _, err := identity.NewIAM("arn:aws:iam::123456789012:root", source); !errors.Is(err, identity.ErrInvalidCaller) {
			t.Errorf("NewIAM source %d = %v", source, err)
		}
	}
	// SourceGatewayAssertion matters even when the envelope itself is raw JSON.
	for _, claims := range []identity.Claims{
		parseClaims(t, `{"iss":"issuer","client_id":"machine","aud":"client","cognito:groups":"[Ops]","scp":"[read]"}`),
		func() identity.Claims {
			c, err := identity.NewTextClaims(map[string]string{"iss": "issuer", "client_id": "machine", "aud": "client", "cognito:groups": "[Ops]", "scp": "[read]"})
			if err != nil {
				t.Fatal(err)
			}
			return c
		}(),
	} {
		c, err := identity.NewJWT(claims, identity.SourceGatewayAssertion)
		if err != nil {
			t.Fatal(err)
		}
		j, _ := c.JWT()
		for _, read := range []func() ([]string, bool){j.Audience, j.Scopes, j.CognitoGroups} {
			if _, known := read(); known {
				t.Error("flattened gateway text became a normalized collection")
			}
		}
		if _, present := j.Subject(); present {
			t.Error("client-only identity acquired a subject")
		}
	}
}

func TestDedicatedScopes(t *testing.T) {
	claims := parseClaims(t, `{"iss":"issuer","sub":"subject","scope":"read write"}`)
	supplied := []string{"write", "read", "read"}
	option := identity.WithGatewayScopes(supplied)
	c, err := identity.NewJWT(claims, identity.SourceGatewayAssertion, option)
	if err != nil {
		t.Fatal(err)
	}
	supplied[0] = "changed"
	j, _ := c.JWT()
	if got, known := j.Scopes(); !known || !slices.Equal(got, []string{"read", "write"}) {
		t.Errorf("owned Scopes() = %v, %t", got, known)
	}
	for _, opts := range [][]identity.JWTOption{{nil}, {option, option}, {identity.WithGatewayScopes([]string{"read"})}, {identity.WithGatewayScopes([]string{})}} {
		if _, err := identity.NewJWT(claims, identity.SourceGatewayAssertion, opts...); !errors.Is(err, identity.ErrInvalidCaller) {
			t.Errorf("invalid/conflicting scopes = %v", err)
		}
	}
	if _, err := identity.NewJWT(claims, identity.SourceLocallyVerifiedToken, identity.WithGatewayScopes(nil)); !errors.Is(err, identity.ErrInvalidCaller) {
		t.Errorf("gateway scope option on local JWT = %v", err)
	}
	for _, scopes := range [][]string{nil, {}} {
		c, err := identity.NewJWT(parseClaims(t, `{"iss":"issuer","sub":"x"}`), identity.SourceGatewayAssertion, identity.WithGatewayScopes(scopes))
		if err != nil {
			t.Fatal(err)
		}
		j, _ := c.JWT()
		if values, known := j.Scopes(); known != (scopes != nil) || len(values) != 0 {
			t.Errorf("Scopes() = %v, %t, expected availability %t", values, known, scopes != nil)
		}
	}
	// Root + two scalar values + names/text = 205;
	// array + "read" = 132.
	for _, maximum := range []int{336, 337} {
		claims, err := identity.NewClaims(map[string]any{"iss": "issuer", "sub": "x"}, identity.WithClaimsBudget(maximum))
		if err != nil {
			t.Fatal(err)
		}
		_, err = identity.NewJWT(claims, identity.SourceGatewayAssertion, identity.WithGatewayScopes([]string{"read"}))
		if maximum == 336 {
			limit, ok := errors.AsType[*identity.ClaimsLimitError](err)
			if !ok || limit.Maximum() != int64(maximum) || !errors.Is(err, identity.ErrInvalidCaller) {
				t.Errorf("scope budget failure = %v, want limit %d and invalid caller", err, maximum)
			}
		} else if err != nil {
			t.Errorf("exact scope budget rejected: %v", err)
		}
	}
}

func TestCallerContextAndDiagnostics(t *testing.T) {
	caller, err := identity.NewIAM("arn:aws:iam::123456789012:user/SecretName", identity.SourceVerifiedIAMProof)
	if err != nil {
		t.Fatal(err)
	}
	type unrelatedKey struct{}
	parent := context.WithValue(t.Context(), unrelatedKey{}, "kept")
	if got := identity.FromContext(parent); got.Kind() != identity.KindAnonymous || got.Source() != identity.SourceNone {
		t.Errorf("empty context = %v", got)
	}
	unchanged, err := identity.WithCaller(parent, identity.Caller{})
	if err != nil || unchanged != parent {
		t.Error("anonymous installation did not preserve empty context")
	}
	ctx, err := identity.WithCaller(parent, caller)
	if err != nil || ctx.Value(unrelatedKey{}) != "kept" || identity.FromContext(ctx).Kind() != identity.KindIAM {
		t.Fatalf("WithCaller lost context/caller: %v", err)
	}
	for _, next := range []identity.Caller{caller, {}} {
		if ctx, err := identity.WithCaller(ctx, next); ctx != nil || !errors.Is(err, identity.ErrConflict) {
			t.Errorf("reinstallation = %v, %v", ctx, err)
		}
	}
	if ctx, err := identity.WithCaller(nil, caller); ctx != nil || !errors.Is(err, identity.ErrInvalidCaller) {
		t.Errorf("nil context = %v, %v", ctx, err)
	}
	iam, _ := caller.IAM()
	for _, value := range []any{caller, iam} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if got := fmt.Sprintf(format, value); strings.Contains(got, "SecretName") || strings.Contains(got, "123456789012") {
				t.Errorf("diagnostic leaked identity: %q", got)
			}
		}
		wire, _ := json.Marshal(value)
		if strings.Contains(string(wire), "SecretName") {
			t.Error("JSON exported identity implicitly")
		}
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if identity.FromContext(ctx).Kind() != identity.KindIAM || identity.FromContext(parent).Kind() != identity.KindAnonymous {
				t.Error("shared contexts did not preserve isolated immutable identity")
			}
		})
	}
	wg.Wait()
}
