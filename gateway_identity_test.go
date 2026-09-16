package edge_test

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	edge "github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// These are independently authored synthetic shape fixtures, not live captures.
// docs/gateway-fixtures.md records sources and the limits of their evidence.
func gatewayFixture(v2 bool, authorizer, iam string) []byte {
	if authorizer == "" {
		authorizer = "null"
	}
	if iam == "" {
		iam = `{}`
	}
	if v2 {
		return []byte(`{"version":"2.0","rawPath":"/orders","headers":{"authorization":"Bearer unverified-secret"},"requestContext":{"apiId":"api","accountId":"999999999999","http":{"method":"POST"},"authorizer":` + authorizer + `}}`)
	}
	return []byte(`{"version":"1.0","path":"/orders","httpMethod":"POST","headers":{"authorization":"Bearer unverified-secret"},"requestContext":{"apiId":"api","accountId":"999999999999","identity":` + iam + `,"authorizer":` + authorizer + `}}`)
}

func TestGatewayRecognitionMatrix(t *testing.T) {
	jwt := `{"claims":{"iss":"issuer","sub":"subject","scope":"read write"},"scopes":["write","read"]}`
	iam := `{"userArn":"arn:aws:iam::123456789012:user/Alice","accountId":"123456789012","user":"effective","caller":"signer"}`
	iam2 := `{"iam":{"userArn":"arn:aws:sts::123456789012:assumed-role/Worker/session","accountId":"123456789012","userId":"effective","callerId":"signer"}}`
	for _, tc := range []struct {
		name      string
		v2        bool
		auth, iam string
		kind      identity.Kind
		failure   error
		rawOnly   bool
	}{
		{name: "v1_absent"},
		{name: "v1_placeholder", auth: `{"claims":null,"scopes":null}`},
		{name: "v1_metadata", iam: `{"accountId":"123456789012","accessKey":"not-a-proof","cognitoIdentityId":"pool-id","sourceIp":"192.0.2.1"}`},
		{name: "v1_iam", iam: iam, kind: identity.KindIAM},
		{name: "v1_jwt", auth: jwt, kind: identity.KindJWT},
		{name: "v1_iam_missing_arn", iam: `{"caller":"signer"}`, failure: identity.ErrInvalidCaller},
		{name: "v1_account_conflict", iam: `{"userArn":"arn:aws:iam::123456789012:root","accountId":"000000000000"}`, failure: identity.ErrInvalidCaller},
		{name: "v1_empty_claims", auth: `{"claims":{}}`, failure: identity.ErrInvalidCaller},
		{name: "v1_scopes_only", auth: `{"claims":null,"scopes":[]}`, failure: identity.ErrInvalidCaller},
		{name: "v1_scope_conflict", auth: `{"claims":{"iss":"issuer","sub":"s","scope":"read"},"scopes":["write"]}`, failure: identity.ErrInvalidCaller},
		{name: "v1_custom", auth: `{"principalId":"custom"}`, failure: errors.ErrUnsupported},
		{name: "v1_unknown", auth: `{"other":null}`, failure: errors.ErrUnsupported},
		{name: "v1_iam_unknown_authorizer", auth: `{"other":"custom"}`, iam: iam, failure: errors.ErrUnsupported},
		{name: "v1_dual_native", auth: jwt, iam: iam, failure: identity.ErrConflict},
		{name: "v1_native_custom", auth: `{"claims":{},"principalId":"custom"}`, failure: identity.ErrConflict},
		{name: "v1_unknown_metadata", auth: `{"claims":{"iss":"issuer","sub":"s"},"latency":10}`, kind: identity.KindJWT},
		{name: "v2_absent", v2: true},
		{name: "v2_null_members", v2: true, auth: `{"jwt":null,"iam":null,"lambda":null}`},
		{name: "v2_jwt", v2: true, auth: `{"jwt":` + jwt + `}`, kind: identity.KindJWT},
		{name: "v2_iam", v2: true, auth: iam2, kind: identity.KindIAM},
		{name: "v2_empty_jwt", v2: true, auth: `{"jwt":{}}`, failure: identity.ErrInvalidCaller},
		{name: "v2_jwt_null_facts", v2: true, auth: `{"jwt":{"claims":null,"scopes":null}}`, failure: identity.ErrInvalidCaller},
		{name: "v2_empty_iam", v2: true, auth: `{"iam":{}}`, failure: identity.ErrInvalidCaller},
		{name: "v2_empty_custom", v2: true, auth: `{"lambda":{}}`, failure: errors.ErrUnsupported},
		{name: "v2_conflict", v2: true, auth: `{"jwt":{},"iam":{}}`, failure: identity.ErrConflict},
		{name: "v2_custom_conflict", v2: true, auth: `{"iam":{},"lambda":{}}`, failure: identity.ErrConflict},
		{name: "v2_unknown", v2: true, auth: `{"other":"new"}`, failure: errors.ErrUnsupported, rawOnly: true},
		{name: "v2_bad_shape", v2: true, auth: `{"jwt":"invalid"}`, failure: identity.ErrInvalidCaller, rawOnly: true},
		{name: "v2_bad_shape_conflict", v2: true, auth: `{"jwt":"invalid","iam":{}}`, failure: identity.ErrConflict, rawOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, typed := range []bool{false, true} {
				if typed && tc.rawOnly {
					continue
				}
				for _, enabled := range []bool{false, true} {
					var got identity.Caller
					ran := false
					a, err := edge.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						ran = true
						got = identity.FromContext(r.Context())
						w.WriteHeader(204)
					}), edge.WithGatewayIdentity(enabled))
					if err != nil {
						t.Fatal(err)
					}
					payload := gatewayFixture(tc.v2, tc.auth, tc.iam)
					if !typed {
						out, invokeErr := a.Invoke(t.Context(), payload)
						err = invokeErr
						if err != nil && out != nil {
							t.Error("failed raw invocation returned a usable result")
						}
					} else if tc.v2 {
						var event events.APIGatewayV2HTTPRequest
						if err := json.Unmarshal(payload, &event); err != nil {
							t.Fatal(err)
						}
						out, invokeErr := a.HandleV2(t.Context(), event)
						err = invokeErr
						if err != nil && out.StatusCode != 0 {
							t.Error("failed typed invocation returned a usable result")
						}
					} else {
						var event events.APIGatewayProxyRequest
						if err := json.Unmarshal(payload, &event); err != nil {
							t.Fatal(err)
						}
						_, err = a.HandleV1(t.Context(), event)
					}
					if enabled && tc.failure != nil {
						if ran || !errors.Is(err, edge.ErrIdentity) || !errors.Is(err, tc.failure) {
							t.Errorf("typed=%t enabled=%t ran=%t err=%v; want %v before handler", typed, enabled, ran, err, tc.failure)
						}
						continue
					}
					want := tc.kind
					if !enabled {
						want = identity.KindAnonymous
					}
					if err != nil || !ran || got.Kind() != want {
						t.Fatalf("typed=%t enabled=%t ran=%t caller=%v err=%v; want kind %d", typed, enabled, ran, got, err, want)
					}
					if i, ok := got.IAM(); ok {
						id, _ := i.PrincipalID()
						if i.AccountID() != "123456789012" || id != "effective" {
							t.Errorf("IAM used API-owner/signing principal: account=%q id=%q", i.AccountID(), id)
						}
					}
				}
			}
		})
	}
}

func TestGatewayFidelityAndOwnership(t *testing.T) {
	var caller identity.Caller
	a, err := edge.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { caller = identity.FromContext(r.Context()) }), edge.WithGatewayIdentity(true))
	if err != nil {
		t.Fatal(err)
	}
	payload := gatewayFixture(true, `{"jwt":{"claims":{"iss":"issuer","sub":"subject","aud":["client"],"cognito:groups":["Ops"],"custom":9007199254740993}}}`, "")
	if _, err := a.Invoke(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	clear(payload)
	j, _ := caller.JWT()
	number, _ := j.Claims().Lookup("custom")
	if exact, ok := number.NumberText(); !ok || exact != "9007199254740993" {
		t.Errorf("raw number = %q, %t", exact, ok)
	}
	if groups, known := j.CognitoGroups(); !known || !slices.Equal(groups, []string{"Ops"}) {
		t.Errorf("raw groups = %v, %t", groups, known)
	}
	event := events.APIGatewayV2HTTPRequest{
		RawPath: "/", RequestContext: events.APIGatewayV2HTTPRequestContext{
			APIID: "api", HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "GET"},
			Authorizer: &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{
				Claims: map[string]string{"iss": "issuer", "sub": "subject", "aud": "[client]", "cognito:groups": "[Ops]", "custom": "9007199254740993"},
			}},
		},
	}
	if _, err := a.HandleV2(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	event.RequestContext.Authorizer.JWT.Claims["sub"] = "changed"
	j, _ = caller.JWT()
	if sub, _ := j.Subject(); sub != "subject" {
		t.Error("typed caller retained mutable SDK claims")
	}
	if _, known := j.CognitoGroups(); known {
		t.Error("flattened groups became known")
	}
	number, _ = j.Claims().Lookup("custom")
	if _, exact := number.NumberText(); exact || number.Representation() != identity.RepresentationGatewayText {
		t.Error("typed V2 text became an exact number")
	}
}

func TestGatewayBudgetsAndInvalidScopeTypes(t *testing.T) {
	for _, scopes := range []string{`[null]`, `42`, `["read",false]`, `[` + strings.Repeat(`"read",`, 500) + `"read"]`} {
		ran := false
		a, err := edge.New(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true }), edge.WithGatewayIdentity(true), edge.WithIdentityClaimsBudget(256))
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.Invoke(t.Context(), gatewayFixture(false, `{"claims":{"iss":"i","sub":"s"},"scopes":`+scopes+`}`, ""))
		if ran || !errors.Is(err, edge.ErrIdentity) {
			t.Errorf("scopes invoked=%t err=%v", ran, err)
		}
		if len(scopes) > 256 {
			limit, ok := errors.AsType[*edge.InvocationError](err)
			if !ok || !errors.Is(err, edge.ErrLimitExceeded) || !errors.Is(err, identity.ErrClaimsLimit) {
				t.Fatalf("scope size error = %v", err)
			}
			name, maximum, present := limit.Limit()
			if !present || name != "identity_claims" || maximum != 256 {
				t.Errorf("scope limit = %q %d %t", name, maximum, present)
			}
		}
	}
}

func TestGatewayIAMFormsAndOptionalPrincipal(t *testing.T) {
	for _, arn := range []string{
		"arn:aws:iam::123456789012:root",
		"arn:aws:iam::123456789012:user/team*?/Alice",
		"arn:aws-us-gov:sts::123456789012:assumed-role/Worker/session",
		"arn:aws-cn:sts::123456789012:federated-user/Bob",
	} {
		for _, v2 := range []bool{false, true} {
			var got identity.Caller
			a, err := edge.New(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = identity.FromContext(r.Context()) }), edge.WithGatewayIdentity(true))
			if err != nil {
				t.Fatal(err)
			}
			facts := `{"userArn":"` + arn + `","accountId":"123456789012","caller":"signer","callerId":"signer"}`
			auth, iam := "", facts
			if v2 {
				auth, iam = `{"iam":`+facts+`}`, ""
			}
			payload := gatewayFixture(v2, auth, iam)
			for _, typed := range []bool{false, true} {
				if !typed {
					_, err = a.Invoke(t.Context(), payload)
				} else if v2 {
					var event events.APIGatewayV2HTTPRequest
					if err := json.Unmarshal(payload, &event); err != nil {
						t.Fatal(err)
					}
					_, err = a.HandleV2(t.Context(), event)
				} else {
					var event events.APIGatewayProxyRequest
					if err := json.Unmarshal(payload, &event); err != nil {
						t.Fatal(err)
					}
					_, err = a.HandleV1(t.Context(), event)
				}
				if err != nil {
					t.Fatal(err)
				}
				i, ok := got.IAM()
				if !ok || i.PrincipalARN() != arn {
					t.Errorf("IAM v2=%t typed=%t did not preserve %q", v2, typed, arn)
				}
				if _, present := i.PrincipalID(); present {
					t.Error("signing caller was substituted for absent effective-user ID")
				}
			}
		}
	}
}

type forbiddenEnvelopeMarshal struct{}

func (forbiddenEnvelopeMarshal) MarshalJSON() ([]byte, error) {
	panic("typed invocation must not serialize ignored event metadata")
}

func TestTypedGatewayDoesNotMarshalAndChecksScopeBudget(t *testing.T) {
	a, err := edge.New(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), edge.WithGatewayIdentity(true), edge.WithIdentityClaimsBudget(350))
	if err != nil {
		t.Fatal(err)
	}
	event := events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: "/", RequestContext: events.APIGatewayProxyRequestContext{
		APIID: "api", Authorizer: map[string]any{
			"claims": map[string]string{"iss": "issuer", "sub": "subject"},
			"scopes": []any{"read"}, "ignored": forbiddenEnvelopeMarshal{},
		},
	}}
	if _, err := a.HandleV1(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	event.RequestContext.Authorizer["scopes"] = []any{"read", "write"}
	if _, err := a.HandleV1(t.Context(), event); !errors.Is(err, edge.ErrLimitExceeded) || !errors.Is(err, identity.ErrClaimsLimit) {
		t.Errorf("combined typed scope budget error = %v", err)
	}
	event.RequestContext.Authorizer["scopes"] = make([]any, 1000)
	if _, err := a.HandleV1(t.Context(), event); !errors.Is(err, edge.ErrLimitExceeded) {
		t.Errorf("typed preflight budget error = %v", err)
	}
}

func TestTypedV1NilScopesAreUnavailable(t *testing.T) {
	ran := false
	a, err := edge.New(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ran = true
		if identity.FromContext(r.Context()).Kind() != identity.KindAnonymous {
			t.Error("null placeholder produced identity")
		}
	}), edge.WithGatewayIdentity(true))
	if err != nil {
		t.Fatal(err)
	}
	for _, scopes := range []any{nil, []any(nil), []string(nil)} {
		ran = false
		event := events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: "/", RequestContext: events.APIGatewayProxyRequestContext{
			APIID: "api", Authorizer: map[string]any{"claims": nil, "scopes": scopes},
		}}
		if _, err := a.HandleV1(t.Context(), event); err != nil || !ran {
			t.Errorf("nil scope placeholder %T: ran=%t err=%v", scopes, ran, err)
		}
	}
}

func TestTypedV2CannotDetectDiscardedUnknownProducer(t *testing.T) {
	ran := false
	a, err := edge.New(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ran = true
		if identity.FromContext(r.Context()).Kind() != identity.KindAnonymous {
			t.Error("empty typed SDK description unexpectedly created identity")
		}
	}), edge.WithGatewayIdentity(true))
	if err != nil {
		t.Fatal(err)
	}
	payload := gatewayFixture(true, `{"futureProducer":{"subject":"unknown"}}`, "")
	if _, err := a.Invoke(t.Context(), payload); !errors.Is(err, errors.ErrUnsupported) || ran {
		t.Errorf("raw unknown producer err=%v ran=%t", err, ran)
	}
	var typed events.APIGatewayV2HTTPRequest
	if err := json.Unmarshal(payload, &typed); err != nil {
		t.Fatal(err)
	}
	if _, err := a.HandleV2(t.Context(), typed); err != nil || !ran {
		t.Errorf("typed information-loss exception err=%v ran=%t", err, ran)
	}
}

func FuzzGatewayIdentityBoundary(f *testing.F) {
	for _, auth := range []string{
		`null`, `{}`, `{"jwt":{}}`, `{"iam":{},"jwt":{}}`, `{"lambda":{}}`,
		`{"jwt":{"claims":{"iss":"issuer","sub":"s"},"scopes":[]}}`,
		`{"claims":{"iss":"issuer","sub":"s"},"scopes":["read"]}`,
		`{"iam":{"userArn":"arn:aws:iam::123456789012:root"}}`,
	} {
		f.Add(true, auth)
		f.Add(false, auth)
	}
	f.Fuzz(func(t *testing.T, v2 bool, auth string) {
		ran := false
		a, err := edge.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ran = true
			c := identity.FromContext(r.Context())
			_, iam := c.IAM()
			_, jwt := c.JWT()
			if iam && jwt || c.Kind() != identity.KindAnonymous && c.Source() != identity.SourceGatewayAssertion {
				t.Error("gateway produced an inconsistent caller")
			}
			w.WriteHeader(204)
		}), edge.WithGatewayIdentity(true), edge.WithIdentityClaimsBudget(4096))
		if err != nil {
			t.Fatal(err)
		}
		out, err := a.Invoke(t.Context(), gatewayFixture(v2, auth, ""))
		if err != nil && (ran || out != nil) {
			t.Fatal("invalid gateway input reached handler or returned usable output")
		}
	})
}
