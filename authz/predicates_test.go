package authz_test

import (
	"context"
	"errors"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/authz"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

const issuer = "https://issuer.example/pool"

func jwtCaller(t testing.TB, values map[string]any, source identity.Source) identity.Caller {
	t.Helper()
	claims, err := identity.NewClaims(values)
	if err != nil {
		t.Fatal(err)
	}
	c, err := identity.NewJWT(claims, source)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestJWTPredicates(t *testing.T) {
	makeRule := func(name string) authz.Rule {
		t.Helper()
		var r authz.Rule
		var err error
		switch name {
		case "subject":
			r, err = authz.JWTSubject(issuer, "alice")
		case "client":
			r, err = authz.JWTClient(issuer, "app")
		case "scopes":
			r, err = authz.JWTScopes(issuer, "orders/read", "orders/write", "orders/read")
		case "groups":
			r, err = authz.CognitoGroups(issuer, "operators", "tenant-A")
		}
		return rule(t, r, err)
	}
	for _, source := range []identity.Source{identity.SourceCustomAssertion, identity.SourceGatewayAssertion, identity.SourceLocallyVerifiedToken} {
		for _, tc := range []struct {
			name    string
			claims  map[string]any
			allowed []string
		}{
			{"all", map[string]any{"iss": issuer, "sub": "alice", "client_id": "app", "scope": "orders/write orders/read", "cognito:groups": []string{"tenant-A", "operators"}}, []string{"subject", "client", "scopes", "groups"}},
			{"issuer_collision", map[string]any{"iss": issuer + "/other", "sub": "alice", "client_id": "app", "scope": "orders/read orders/write", "cognito:groups": []string{"operators", "tenant-A"}}, nil},
			{"unavailable", map[string]any{"iss": issuer, "sub": "alice"}, []string{"subject"}},
			{"client_not_subject", map[string]any{"iss": issuer, "client_id": "alice", "aud": []string{"app"}}, nil},
			{"client_only", map[string]any{"iss": issuer, "client_id": "app"}, []string{"client"}},
			{"subject_not_client", map[string]any{"iss": issuer, "sub": "app"}, nil},
			{"partial_sets", map[string]any{"iss": issuer, "sub": "bob", "scope": "orders/read", "cognito:groups": []string{"operators"}}, nil},
			{"empty_sets", map[string]any{"iss": issuer, "sub": "bob", "scp": []string{}, "cognito:groups": []string{}}, nil},
			{"wrong_case", map[string]any{"iss": issuer, "sub": "Alice", "client_id": "App", "scope": "Orders/read orders/write", "cognito:groups": []string{"Operators", "tenant-A"}}, nil},
			{"unrelated_groups", map[string]any{"iss": issuer, "sub": "bob", "groups": []string{"operators", "tenant-A"}, "roles": []string{"operators"}}, nil},
		} {
			t.Run(tc.name+"/"+sourceName(source), func(t *testing.T) {
				caller := jwtCaller(t, tc.claims, source)
				for _, name := range []string{"subject", "client", "scopes", "groups"} {
					want := authz.ErrDenied
					for _, allowed := range tc.allowed {
						if name == allowed {
							want = nil
						}
					}
					if err := makeRule(name).Authorize(t.Context(), authz.Request{Caller: caller, Action: "orders.read"}); !errors.Is(err, want) {
						t.Errorf("%s = %v; want %v", name, err, want)
					}
				}
			})
		}
	}
	for _, name := range []string{"subject", "client", "scopes", "groups"} {
		if err := makeRule(name).Authorize(t.Context(), request(t)); !errors.Is(err, authz.ErrDenied) {
			t.Error(err)
		}
	}
}

func sourceName(s identity.Source) string {
	switch s {
	case identity.SourceGatewayAssertion:
		return "gateway"
	case identity.SourceCustomAssertion:
		return "custom"
	case identity.SourceLocallyVerifiedToken:
		return "local"
	default:
		return "iam"
	}
}

func TestFlattenedCollectionsAndLiteralSets(t *testing.T) {
	claims, err := identity.NewTextClaims(map[string]string{"iss": issuer, "sub": "alice", "scp": `["orders/*"]`, "cognito:groups": "[operators,tenant-A]"})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := identity.NewJWT(claims, identity.SourceGatewayAssertion)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := authz.JWTScopes(issuer, "orders/*")
	scope = rule(t, scope, err)
	group, err := authz.CognitoGroups(issuer, "operators")
	group = rule(t, group, err)
	for _, r := range []authz.Rule{scope, group} {
		if err := r.Authorize(t.Context(), authz.Request{Caller: caller, Action: "read"}); !errors.Is(err, authz.ErrDenied) {
			t.Error(err)
		}
	}
	// A separate dedicated gateway collection is usable without parsing text.
	caller, err = identity.NewJWT(claims, identity.SourceGatewayAssertion, identity.WithGatewayScopes([]string{"orders/*"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.Authorize(t.Context(), authz.Request{Caller: caller, Action: "read"}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"orders/*", "orders/read"} {
		caller := jwtCaller(t, map[string]any{"iss": issuer, "sub": "alice", "scope": value}, identity.SourceCustomAssertion)
		want := authz.ErrDenied
		if value == "orders/*" {
			want = nil
		}
		if err := scope.Authorize(t.Context(), authz.Request{Caller: caller, Action: "read"}); !errors.Is(err, want) {
			t.Errorf("scope %q = %v", value, err)
		}
	}
}

func TestIAMExactAndRoleFamilies(t *testing.T) {
	const account = "123456789012"
	for _, arn := range []string{
		"arn:aws:iam::" + account + ":root",
		"arn:aws:iam::" + account + ":user/team*?/Alice",
		"arn:aws:sts::" + account + ":assumed-role/Reader/session-one",
		"arn:aws:sts::" + account + ":federated-user/Alice",
	} {
		r, err := authz.IAMPrincipal(arn)
		r = rule(t, r, err)
		for _, got := range []string{arn, "arn:aws:iam::" + account + ":user/Alice", "arn:aws:iam::" + account + ":user/teamAB/Alice"} {
			want := authz.ErrDenied
			if got == arn {
				want = nil
			}
			if err := r.Authorize(t.Context(), authz.Request{Caller: iamCaller(t, got), Action: "read"}); !errors.Is(err, want) {
				t.Errorf("principal %q caller %q = %v", arn, got, err)
			}
		}
	}
	family, err := authz.IAMRoleSessions("aws", account, "Reader")
	family = rule(t, family, err)
	exact, err := authz.IAMPrincipal("arn:aws:sts::" + account + ":assumed-role/Reader/session-one")
	exact = rule(t, exact, err)
	for _, tc := range []struct {
		arn           string
		family, exact bool
	}{
		{"arn:aws:sts::" + account + ":assumed-role/Reader/session-one", true, true},
		{"arn:aws:sts::" + account + ":assumed-role/Reader/session-two", true, false},
		{"arn:aws-cn:sts::" + account + ":assumed-role/Reader/session-one", false, false},
		{"arn:aws:sts::000000000000:assumed-role/Reader/session-one", false, false},
		{"arn:aws:sts::" + account + ":assumed-role/Readers/session-one", false, false},
		{"arn:aws:sts::" + account + ":assumed-role/reader/session-one", false, false},
		{"arn:aws:iam::" + account + ":user/Reader", false, false},
		{"arn:aws:iam::" + account + ":root", false, false},
	} {
		for _, principalID := range []string{"OLDROLEID:session-one", "NEWROLEID:session-one"} {
			caller := iamCaller(t, tc.arn, identity.WithIAMPrincipalID(principalID))
			for _, test := range []struct {
				rule  authz.Rule
				allow bool
			}{{family, tc.family}, {exact, tc.exact}} {
				want := authz.ErrDenied
				if test.allow {
					want = nil
				}
				if err := test.rule.Authorize(t.Context(), authz.Request{Caller: caller, Action: "read"}); !errors.Is(err, want) {
					t.Errorf("caller %q = %v; want %v", tc.arn, err, want)
				}
			}
		}
	}
	jwt := jwtCaller(t, map[string]any{"iss": issuer, "sub": "alice"}, identity.SourceCustomAssertion)
	if err := family.Authorize(t.Context(), authz.Request{Caller: jwt, Action: "read"}); !errors.Is(err, authz.ErrDenied) {
		t.Error(err)
	}
}

func TestPredicateConfigurationAndSliceOwnership(t *testing.T) {
	for _, construct := range []func() (authz.Rule, error){
		func() (authz.Rule, error) { return authz.Sources() },
		func() (authz.Rule, error) { return authz.Sources(identity.SourceNone) },
		func() (authz.Rule, error) { return authz.Sources(identity.Source(255)) },
		func() (authz.Rule, error) { return authz.JWTSubject("", "alice") },
		func() (authz.Rule, error) { return authz.JWTSubject(issuer, "\xff") },
		func() (authz.Rule, error) { return authz.JWTClient(issuer, "") },
		func() (authz.Rule, error) { return authz.JWTClient("\xff", "app") },
		func() (authz.Rule, error) { return authz.JWTScopes(issuer) },
		func() (authz.Rule, error) { return authz.CognitoGroups(issuer) },
		func() (authz.Rule, error) { return authz.CognitoGroups(issuer, "") },
		func() (authz.Rule, error) { return authz.CognitoGroups("", "operators") },
		func() (authz.Rule, error) { return authz.CognitoGroups(issuer, "\xff") },
		func() (authz.Rule, error) { return authz.IAMPrincipal("arn:aws:iam::123456789012:role/Reader") },
		func() (authz.Rule, error) { return authz.IAMRoleSessions("aws", "*", "Reader") },
		func() (authz.Rule, error) { return authz.IAMRoleSessions("aws", "123456789012", "Read*") },
		func() (authz.Rule, error) { return authz.IAMRoleSessions("aws", "123456789012", "path/Reader") },
		func() (authz.Rule, error) { return authz.IAMRoleSessions("*", "123456789012", "Reader") },
		func() (authz.Rule, error) { return authz.IAMRoleSessions("aws:sts", "123456789012", "Reader") },
	} {
		if _, err := construct(); !errors.Is(err, authz.ErrInvalidConfiguration) {
			t.Errorf("constructor = %v", err)
		}
	}
	for _, scope := range []string{"", "two scopes", "with\ttab", "quote\"", "slash\\", "\x7f", "日本語"} {
		if _, err := authz.JWTScopes(issuer, scope); !errors.Is(err, authz.ErrInvalidConfiguration) {
			t.Errorf("scope %q = %v", scope, err)
		}
	}
	values := []string{"operators"}
	r, err := authz.CognitoGroups(issuer, values...)
	r = rule(t, r, err)
	values[0] = "admins"
	sources := []identity.Source{identity.SourceCustomAssertion}
	sourceRule, err := authz.Sources(sources...)
	sourceRule = rule(t, sourceRule, err)
	sources[0] = identity.SourceGatewayAssertion
	for _, source := range []identity.Source{identity.SourceCustomAssertion, identity.SourceGatewayAssertion, identity.SourceLocallyVerifiedToken} {
		caller := jwtCaller(t, map[string]any{"iss": issuer, "sub": "alice", "cognito:groups": []string{"operators"}}, source)
		if err := r.Authorize(t.Context(), authz.Request{Caller: caller, Action: "read"}); err != nil {
			t.Error(err)
		}
		want := authz.ErrDenied
		if source == identity.SourceCustomAssertion {
			want = nil
		}
		if err := sourceRule.Authorize(t.Context(), authz.Request{Caller: caller, Action: "read"}); !errors.Is(err, want) {
			t.Error(err)
		}
	}
}

func TestMandatoryGuardAndInapplicableBranch(t *testing.T) {
	var calls int
	provider := checked(t, func(context.Context, authz.Request) (bool, error) { calls++; return true, nil })
	jwtOnly, err := authz.JWTSubject(issuer, "alice")
	jwtOnly = rule(t, jwtOnly, err)
	jwtPath, err := authz.All(jwtOnly, provider)
	jwtPath = rule(t, jwtPath, err)
	iamPath, err := authz.IAMPrincipal("arn:aws:iam::123456789012:user/Alice")
	iamPath = rule(t, iamPath, err)
	paths, err := authz.Any(jwtPath, iamPath)
	paths = rule(t, paths, err)
	if err := paths.Authorize(t.Context(), request(t)); err != nil || calls != 0 {
		t.Fatalf("inapplicable lookup: error=%v calls=%d", err, calls)
	}
	guard := checked(t, func(context.Context, authz.Request) (bool, error) { return false, nil })
	guarded, err := authz.All(guard, paths)
	guarded = rule(t, guarded, err)
	if err := guarded.Authorize(t.Context(), request(t)); !errors.Is(err, authz.ErrDenied) {
		t.Fatal(err)
	}
}

func FuzzPolicyInputs(f *testing.F) {
	for _, s := range []string{"", "orders/*", "日本語", "arn:aws:iam::123456789012:user/a*?/Alice", "arn:aws:sts::123456789012:assumed-role/Reader/session"} {
		f.Add(s, "aws", "123456789012")
	}
	f.Fuzz(func(t *testing.T, value, partition, account string) {
		for _, construct := range []func() (authz.Rule, error){
			func() (authz.Rule, error) { return authz.JWTSubject(issuer, value) },
			func() (authz.Rule, error) { return authz.JWTClient(issuer, value) },
			func() (authz.Rule, error) { return authz.JWTScopes(issuer, value) },
			func() (authz.Rule, error) { return authz.CognitoGroups(issuer, value) },
			func() (authz.Rule, error) { return authz.IAMPrincipal(value) },
			func() (authz.Rule, error) { return authz.IAMRoleSessions(partition, account, value) },
		} {
			r, err := construct()
			if err != nil {
				if !errors.Is(err, authz.ErrInvalidConfiguration) {
					t.Fatal(err)
				}
				continue
			}
			if err := r.Authorize(t.Context(), authz.Request{Action: "read"}); !errors.Is(err, authz.ErrUnauthenticated) {
				t.Fatalf("anonymous allowed: %v", err)
			}
		}
		caller, callerErr := identity.NewIAM(value, identity.SourceCustomAssertion)
		principal, policyErr := authz.IAMPrincipal(value)
		if (callerErr == nil) != (policyErr == nil) {
			t.Fatal("principal policy disagrees with identity grammar")
		}
		if policyErr == nil {
			if err := principal.Authorize(t.Context(), authz.Request{Caller: caller, Action: "read"}); err != nil {
				t.Fatal(err)
			}
		}
		family, err := authz.IAMRoleSessions(partition, account, value)
		if err == nil {
			arn := "arn:" + partition + ":sts::" + account + ":assumed-role/" + value + "/session"
			caller := iamCaller(t, arn)
			if err := family.Authorize(t.Context(), authz.Request{Caller: caller, Action: "read"}); err != nil {
				t.Fatal(err)
			}
		}
	})
}
