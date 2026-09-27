package iamproof_test

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/asteroid-computing/go-lambda-edge/iamproof"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

const (
	testAudience = "orders.production"
	testARN      = "arn:aws:sts::123456789012:assumed-role/Operator/alice@example.com"
	namespace    = "https://sts.amazonaws.com/doc/2011-06-15/"
)

var testCredentials = aws.Credentials{
	AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "synthetic-secret-not-valid-in-AWS",
	SessionToken: "synthetic+/=session-token",
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func generator(t testing.TB, region string, creds aws.Credentials) *iamproof.Generator {
	t.Helper()
	g, err := iamproof.NewGenerator(region, testAudience, aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return creds, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func generate(t testing.TB) iamproof.Token {
	t.Helper()
	token, err := generator(t, "eu-west-2", testCredentials).Generate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func verifier(t testing.TB, transport http.RoundTripper, opts ...iamproof.VerifierOption) *iamproof.Verifier {
	t.Helper()
	opts = append([]iamproof.VerifierOption{iamproof.WithTransport(transport)}, opts...)
	v, err := iamproof.NewVerifier("eu-west-2", testAudience, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func successXML(arn string) string {
	return `<GetCallerIdentityResponse xmlns="` + namespace + `"><GetCallerIdentityResult><Arn>` + arn + `</Arn><UserId>opaque:session</UserId><Account>123456789012</Account></GetCallerIdentityResult><ResponseMetadata><RequestId>synthetic</RequestId></ResponseMetadata></GetCallerIdentityResponse>`
}

func errorXML(code string) string {
	return `<ErrorResponse xmlns="` + namespace + `"><Error><Type>Sender</Type><Code>` + code + `</Code><Message>sensitive upstream diagnostic</Message></Error><RequestId>synthetic</RequestId></ErrorResponse>`
}

func unpack(t testing.TB, token string) map[string]string {
	t.Helper()
	wire, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "v1."))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]string
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func pack(t testing.TB, fields map[string]string) string {
	t.Helper()
	wire, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return rawToken(string(wire))
}

func rawToken(wire string) string { return "v1." + base64.RawURLEncoding.EncodeToString([]byte(wire)) }

func assertFailure(t testing.TB, caller identity.Caller, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || caller.Kind() != identity.KindAnonymous {
		t.Fatalf("Verify = %v, %v; want anonymous, %v", caller, err, want)
	}
}

// The transport recomputes a signature with synthetic credentials. This checks
// request reconstruction and tampering locally; it is not an AWS acceptance test.
func checkingTransport(t testing.TB, region, host, arn string, creds aws.Credentials) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Scheme != "https" || req.URL.Host != host || req.URL.Path != "/" || req.Body != nil {
			t.Error("verification changed the fixed STS destination, method or body")
		}
		q := req.URL.Query()
		if strings.Contains(req.URL.RawQuery, "+") {
			t.Error("reconstructed SigV4 query used form-encoded spaces")
		}
		if q.Get("Action") != "GetCallerIdentity" || q.Get("Version") != "2011-06-15" || q.Get("X-Amz-Expires") != "60" || q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || q.Get("X-Amz-SignedHeaders") != "host;x-edge-iam-audience" {
			t.Error("verification changed fixed STS query fields")
		}
		if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" || req.Header.Get("X-Edge-IAM-Audience") != testAudience {
			t.Error("unexpected STS request headers")
		}
		when, err := time.Parse("20060102T150405Z", q.Get("X-Amz-Date"))
		if err != nil {
			t.Error(err)
			return response(403, errorXML("SignatureDoesNotMatch")), nil
		}
		unsigned := req.Clone(req.Context())
		unsigned.URL.RawQuery = url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}, "X-Amz-Expires": {"60"}}.Encode()
		signed, _, err := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableHeaderHoisting = true }).PresignHTTP(req.Context(), creds, unsigned,
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "sts", region, when)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := url.Parse(signed)
		if err != nil {
			t.Fatal(err)
		}
		if expected.Query().Encode() != q.Encode() {
			return response(403, errorXML("SignatureDoesNotMatch")), nil
		}
		return response(200, successXML(arn)), nil
	})
}

func TestRoundTripPartitionsAndCallerForms(t *testing.T) {
	for _, tc := range []struct{ region, host, partition string }{
		{"eu-west-2", "sts.eu-west-2.amazonaws.com", "aws"},
		{"us-east-1", "sts.us-east-1.amazonaws.com", "aws"},
		{"us-gov-west-1", "sts.us-gov-west-1.amazonaws.com", "aws-us-gov"},
		{"cn-north-1", "sts.cn-north-1.amazonaws.com.cn", "aws-cn"},
		{"us-iso-east-1", "sts.us-iso-east-1.c2s.ic.gov", "aws-iso"},
		{"us-isob-east-1", "sts.us-isob-east-1.sc2s.sgov.gov", "aws-iso-b"},
		{"eu-isoe-west-1", "sts.eu-isoe-west-1.cloud.adc-e.uk", "aws-iso-e"},
		{"us-isof-south-1", "sts.us-isof-south-1.csp.hci.ic.gov", "aws-iso-f"},
		{"eusc-de-east-1", "sts.eusc-de-east-1.amazonaws.eu", "aws-eusc"},
	} {
		t.Run(tc.region, func(t *testing.T) {
			for _, resource := range []string{"iam::123456789012:root", "iam::123456789012:user/team*?/Alice", "sts::123456789012:assumed-role/Operator/alice@example.com", "sts::123456789012:federated-user/Bob"} {
				for _, session := range []string{"", testCredentials.SessionToken, strings.Repeat("+/=a", 1024), "opaque space/&?%é"} {
					creds := testCredentials
					creds.SessionToken = session
					arn := "arn:" + tc.partition + ":" + resource
					g := generator(t, tc.region, creds)
					token, err := g.Generate(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					v, err := iamproof.NewVerifier(tc.region, testAudience, iamproof.WithTransport(checkingTransport(t, tc.region, tc.host, arn, creds)))
					if err != nil {
						t.Fatal(err)
					}
					caller, err := v.Verify(t.Context(), token.Value())
					if err != nil {
						t.Fatal(err)
					}
					iam, ok := caller.IAM()
					id, idOK := iam.PrincipalID()
					if !ok || iam.PrincipalARN() != arn || iam.AccountID() != "123456789012" || !idOK || id != "opaque:session" || caller.Source() != identity.SourceVerifiedIAMProof {
						t.Fatalf("wrong verified caller: %v", caller)
					}
					if identity.FromContext(t.Context()).Kind() != identity.KindAnonymous {
						t.Fatal("Verify installed a caller")
					}
				}
			}
		})
	}
}

func TestMalformedProofsDoNotReachSTS(t *testing.T) {
	token := generate(t).Value()
	base, err := base64.RawURLEncoding.DecodeString(token[3:])
	if err != nil {
		t.Fatal(err)
	}
	v := verifier(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("malformed proof reached STS")
		return response(200, successXML(testARN)), nil
	}))
	cases := map[string]string{
		"empty": "", "scheme": "EdgeIAM " + token, "version": "v2." + token[3:],
		"padding": token + "=", "newline": token[:10] + "\n" + token[10:],
		"oversize": strings.Repeat("a", 8193), "null": rawToken("null"), "array": rawToken("[]"),
		"trailing":  rawToken(string(base) + "{}"),
		"duplicate": rawToken(`{"audience":"other",` + string(base[1:])),
		"utf8":      rawToken(strings.Replace(string(base), testAudience, "\xff", 1)),
		"nested":    rawToken(`{"audience":{}}`),
	}
	for _, field := range []string{"audience", "credential", "date", "signature", "sessionToken"} {
		fields := unpack(t, token)
		fields[field] = ""
		cases[field+"_empty"] = pack(t, fields)
		wire, err := json.Marshal(unpack(t, token))
		if err != nil {
			t.Fatal(err)
		}
		value, err := json.Marshal(unpack(t, token)[field])
		if err != nil {
			t.Fatal(err)
		}
		cases[field+"_null"] = rawToken(strings.Replace(string(wire), `"`+field+`":`+string(value), `"`+field+`":null`, 1))
		if field != "sessionToken" {
			delete(fields, field)
			cases[field+"_missing"] = pack(t, fields)
		}
	}
	for name, change := range map[string]func(map[string]string){
		"audience_case": func(p map[string]string) { p["audience"] = "Orders.production" },
		"unknown":       func(p map[string]string) { p["endpoint"] = "https://attacker.invalid" },
		"region": func(p map[string]string) {
			p["credential"] = strings.Replace(p["credential"], "eu-west-2", "us-east-1", 1)
		},
		"scope_date": func(p map[string]string) {
			p["credential"] = strings.Replace(p["credential"], p["date"][:8], "20000101", 1)
		},
		"service":  func(p map[string]string) { p["credential"] = strings.Replace(p["credential"], "/sts/", "/iam/", 1) },
		"terminal": func(p map[string]string) { p["credential"] += "/extra" },
		"key": func(p map[string]string) {
			p["credential"] = strings.Replace(p["credential"], testCredentials.AccessKeyID, "bad-key", 1)
		},
		"signature":   func(p map[string]string) { p["signature"] = strings.Repeat("A", 64) },
		"short_sig":   func(p map[string]string) { p["signature"] = "a" },
		"date_format": func(p map[string]string) { p["date"] = "2026-09-16T12:00:00Z" },
	} {
		p := unpack(t, token)
		change(p)
		cases[name] = pack(t, p)
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			caller, err := v.Verify(t.Context(), token)
			assertFailure(t, caller, err, iamproof.ErrInvalidProof)
		})
	}
}

func TestSignatureAndSessionTampering(t *testing.T) {
	token := generate(t).Value()
	v := verifier(t, checkingTransport(t, "eu-west-2", "sts.eu-west-2.amazonaws.com", testARN, testCredentials))
	for _, field := range []string{"signature", "sessionToken", "credential", "audience"} {
		t.Run(field, func(t *testing.T) {
			p := unpack(t, token)
			switch field {
			case "signature":
				p[field] = strings.Repeat("0", 64)
			case "credential":
				p[field] = strings.Replace(p[field], testCredentials.AccessKeyID, "AKIAOTHERKEYEXAMPLE", 1)
			default:
				p[field] += "altered"
			}
			caller, err := v.Verify(t.Context(), pack(t, p))
			assertFailure(t, caller, err, iamproof.ErrInvalidProof)
		})
	}
}

func TestAudienceCannotBeRebound(t *testing.T) {
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return testCredentials, nil })
	g, err := iamproof.NewGenerator("eu-west-2", "orders.staging", provider)
	if err != nil {
		t.Fatal(err)
	}
	token, err := g.Generate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p := unpack(t, token.Value())
	p["audience"] = testAudience // Plaintext binding now matches, signature does not.
	v := verifier(t, checkingTransport(t, "eu-west-2", "sts.eu-west-2.amazonaws.com", testARN, testCredentials))
	caller, err := v.Verify(t.Context(), pack(t, p))
	assertFailure(t, caller, err, iamproof.ErrInvalidProof)
}

func TestFreshnessBoundaries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := unpack(t, generate(t).Value())
		var requests int
		v := verifier(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return response(200, successXML(testARN)), nil
		}))
		for _, tc := range []struct {
			age   time.Duration
			valid bool
		}{{-31 * time.Second, false}, {-30 * time.Second, true}, {0, true}, {59 * time.Second, true}, {60 * time.Second, false}} {
			when := time.Now().Add(-tc.age).UTC()
			base["date"] = when.Format("20060102T150405Z")
			base["credential"] = testCredentials.AccessKeyID + "/" + when.Format("20060102") + "/eu-west-2/sts/aws4_request"
			before := requests
			caller, err := v.Verify(t.Context(), pack(t, base))
			if tc.valid {
				if err != nil || caller.Kind() != identity.KindIAM || requests != before+1 {
					t.Fatalf("age %v: caller=%v err=%v requests=%d", tc.age, caller, err, requests-before)
				}
			} else {
				assertFailure(t, caller, err, iamproof.ErrInvalidProof)
				if requests != before {
					t.Error("stale/future proof reached STS")
				}
			}
		}
		// A proof that expires while STS is answering cannot establish a caller.
		token := generate(t)
		time.Sleep(59 * time.Second)
		slow := verifier(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			time.Sleep(time.Second)
			return response(200, successXML(testARN)), nil
		}))
		caller, err := slow.Verify(t.Context(), token.Value())
		assertFailure(t, caller, err, iamproof.ErrInvalidProof)
	})
}

func TestDependencyClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"expired_token", 403, errorXML("ExpiredToken"), iamproof.ErrInvalidProof},
		{"expired", 400, errorXML("ExpiredTokenException"), iamproof.ErrInvalidProof},
		{"incomplete", 400, errorXML("IncompleteSignature"), iamproof.ErrInvalidProof},
		{"missing", 403, errorXML("MissingAuthenticationToken"), iamproof.ErrInvalidProof},
		{"request_expired", 400, errorXML("RequestExpired"), iamproof.ErrInvalidProof},
		{"unrecognized", 403, errorXML("UnrecognizedClientException"), iamproof.ErrInvalidProof},
		{"invalid_key", 403, errorXML("InvalidClientTokenId"), iamproof.ErrInvalidProof},
		{"signature", 403, errorXML("SignatureDoesNotMatch"), iamproof.ErrInvalidProof},
		{"throttle_400", 400, errorXML("ThrottlingException"), iamproof.ErrUnavailable},
		{"throttle_429", 429, errorXML("Throttling"), iamproof.ErrUnavailable},
		{"unknown", 403, errorXML("AccessDenied"), iamproof.ErrUnavailable},
		{"server", 500, errorXML("InvalidClientTokenId"), iamproof.ErrUnavailable},
		{"unexpected_success", 201, successXML(testARN), iamproof.ErrUnavailable},
		{"redirect", 307, "", iamproof.ErrUnavailable},
		{"html", 403, "<html>failed</html>", iamproof.ErrUnavailable},
		{"empty", 200, "", iamproof.ErrUnavailable},
		{"error_as_success", 200, errorXML("InvalidClientTokenId"), iamproof.ErrUnavailable},
		{"duplicate_code", 403, strings.Replace(errorXML("InvalidClientTokenId"), "</Code>", "</Code><Code>Throttling</Code>", 1), iamproof.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count int
			v := verifier(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				count++
				r := response(tc.status, tc.body)
				r.Header.Set("Location", "https://attacker.invalid/stolen")
				return r, nil
			}))
			caller, err := v.Verify(t.Context(), generate(t).Value())
			assertFailure(t, caller, err, tc.want)
			if count != 1 {
				t.Errorf("transport called %d times; want one", count)
			}
			if strings.Contains(fmt.Sprintf("%+v %#v", err, err), "sensitive") {
				t.Error("upstream response leaked in diagnostics")
			}
		})
	}
}

func TestXMLIdentityValidation(t *testing.T) {
	valid := successXML(testARN)
	for name, body := range map[string]string{
		"wrong_namespace": strings.ReplaceAll(valid, namespace, "https://attacker.invalid"),
		"no_namespace":    strings.ReplaceAll(valid, ` xmlns="`+namespace+`"`, ""),
		"foreign_arn":     strings.Replace(valid, "<Arn>", `<Arn xmlns="urn:foreign">`, 1),
		"duplicate_arn":   strings.Replace(valid, "</Arn>", "</Arn><Arn>"+testARN+"</Arn>", 1),
		"duplicate_result": strings.Replace(valid, "</GetCallerIdentityResult>",
			"</GetCallerIdentityResult><GetCallerIdentityResult/>", 1),
		"missing_account": strings.Replace(valid, "<Account>123456789012</Account>", "", 1),
		"empty_id":        strings.Replace(valid, "opaque:session", "", 1),
		"wrong_account":   strings.Replace(valid, "<Account>123456789012", "<Account>000000000000", 1),
		"wrong_partition": strings.Replace(valid, "arn:aws:", "arn:aws-cn:", 1),
		"bare_role":       successXML("arn:aws:iam::123456789012:role/Operator"),
		"nested_arn":      strings.Replace(valid, testARN, "<text>"+testARN+"</text>", 1),
		"second_doc":      valid + valid,
		"trailing":        valid + "garbage",
		"dtd":             `<!DOCTYPE foo>` + valid,
		"broken":          valid[:len(valid)-1],
		"oversized":       valid + strings.Repeat(" ", 65536),
	} {
		t.Run(name, func(t *testing.T) {
			v := verifier(t, roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, body), nil }))
			caller, err := v.Verify(t.Context(), generate(t).Value())
			assertFailure(t, caller, err, iamproof.ErrUnavailable)
		})
	}
	for _, body := range []string{
		strings.Replace(valid, "<ResponseMetadata>", "<NewMetadata>text</NewMetadata><ResponseMetadata>", 1),
		strings.Replace(valid, "<Arn>", "<!--ignored--><Arn>", 1),
		`<s:GetCallerIdentityResponse xmlns:s="` + namespace + `"><s:GetCallerIdentityResult><s:Arn>` + testARN + `</s:Arn><s:Account>123456789012</s:Account><s:UserId>opaque:session</s:UserId></s:GetCallerIdentityResult></s:GetCallerIdentityResponse>`,
	} {
		v := verifier(t, roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, body), nil }))
		if _, err := v.Verify(t.Context(), generate(t).Value()); err != nil {
			t.Fatalf("bounded extra metadata rejected: %v", err)
		}
	}
}

func TestCancellationAndTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		token := generate(t).Value()
		v := verifier(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, errors.New("sensitive signed URL in transport failure")
		}))
		start := time.Now()
		caller, err := v.Verify(t.Context(), token)
		assertFailure(t, caller, err, iamproof.ErrUnavailable)
		if time.Since(start) != 5*time.Second || errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("internal timeout leaked or did not use the five-second default")
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		caller, err = v.Verify(ctx, token)
		assertFailure(t, caller, err, context.DeadlineExceeded)
		ctx, stop := context.WithCancelCause(t.Context())
		stop(errors.New("sensitive cancellation cause"))
		caller, err = v.Verify(ctx, token)
		assertFailure(t, caller, err, context.Canceled)
		if strings.Contains(err.Error(), "sensitive") {
			t.Fatal("cancellation cause leaked")
		}
	})
}

func TestGeneratorCredentialFailuresAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		creds aws.Credentials
		err   error
		want  error
	}{
		{"provider", testCredentials, errors.New("sensitive provider diagnostic"), iamproof.ErrCredentialsUnavailable},
		{"missing", aws.Credentials{}, nil, iamproof.ErrCredentialsUnavailable},
		{"key", aws.Credentials{AccessKeyID: "bad/key", SecretAccessKey: "secret"}, nil, iamproof.ErrCredentialsUnavailable},
		{"secret", aws.Credentials{AccessKeyID: testCredentials.AccessKeyID}, nil, iamproof.ErrCredentialsUnavailable},
		{"expired", aws.Credentials{AccessKeyID: testCredentials.AccessKeyID, SecretAccessKey: "secret", CanExpire: true, Expires: time.Now().Add(-time.Second)}, nil, iamproof.ErrCredentialsUnavailable},
		{"invalid_utf8", aws.Credentials{AccessKeyID: testCredentials.AccessKeyID, SecretAccessKey: "secret", SessionToken: "\xff"}, nil, iamproof.ErrCredentialsUnavailable},
		{"oversize", aws.Credentials{AccessKeyID: testCredentials.AccessKeyID, SecretAccessKey: "secret", SessionToken: strings.Repeat("+/=a", 1536)}, nil, iamproof.ErrProofTooLarge},
		{"huge", aws.Credentials{AccessKeyID: testCredentials.AccessKeyID, SecretAccessKey: "secret", SessionToken: strings.Repeat("x", 1<<20)}, nil, iamproof.ErrProofTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := iamproof.NewGenerator("eu-west-2", testAudience, aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return tc.creds, tc.err }))
			if err != nil {
				t.Fatal(err)
			}
			token, err := g.Generate(t.Context())
			if !errors.Is(err, tc.want) || token.Value() != "" || !token.ExpiresAt().IsZero() {
				t.Fatalf("Generate = %v, %v; want zero token, %v", token, err, tc.want)
			}
			if strings.Contains(fmt.Sprintf("%+v %#v", err, err), "sensitive") {
				t.Error("provider error leaked")
			}
		})
	}
	token := generate(t)
	for _, value := range []any{token, &token} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			diagnostic := fmt.Sprintf(format, value)
			if strings.Contains(diagnostic, token.Value()) || strings.Contains(diagnostic, testCredentials.SessionToken) {
				t.Fatalf("token leaked through %s", format)
			}
		}
	}
}

func TestGeneratorRetrievalAndExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int
		creds := testCredentials
		creds.AccountID = "000000000000" // Provider metadata is not identity evidence.
		creds.CanExpire, creds.Expires = true, time.Now().Add(20*time.Second)
		g, err := iamproof.NewGenerator("eu-west-2", testAudience, aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			calls++
			return creds, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			token, err := g.Generate(t.Context())
			if err != nil || !token.ExpiresAt().Equal(creds.Expires) {
				t.Fatalf("credential expiry was not preserved: %v, %v", token.ExpiresAt(), err)
			}
		}
		if calls != 2 {
			t.Fatalf("Retrieve called %d times; want 2", calls)
		}
		time.Sleep(20 * time.Second)
		if _, err := g.Generate(t.Context()); !errors.Is(err, iamproof.ErrCredentialsUnavailable) {
			t.Fatalf("credentials accepted at their expiry: %v", err)
		}
		creds.CanExpire = false
		token, err := g.Generate(t.Context())
		if err != nil || !token.ExpiresAt().Equal(time.Now().Add(time.Minute)) {
			t.Fatalf("unexpected proof expiry: %v, %v", token.ExpiresAt(), err)
		}
	})
}

func TestInvalidConfigurationAndZeroValues(t *testing.T) {
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return testCredentials, nil })
	for _, region := range []string{"", "aws-global", "fips-us-east-1", "us-east-1-fips", "EU-west-2", "eu-west-2.attacker.invalid", "eu-west-2/", "eu-west-2:443", strings.Repeat("a", 64) + "-west-1"} {
		if g, err := iamproof.NewGenerator(region, testAudience, provider); g != nil || !errors.Is(err, iamproof.ErrInvalidConfiguration) {
			t.Fatalf("invalid region %q accepted", region)
		}
	}
	for _, audience := range []string{"", "has space", "a\n", "a\x7f", "café", strings.Repeat("a", 257)} {
		if v, err := iamproof.NewVerifier("eu-west-2", audience); v != nil || !errors.Is(err, iamproof.ErrInvalidConfiguration) {
			t.Fatalf("invalid audience %q accepted", audience)
		}
	}
	var nilProvider aws.CredentialsProviderFunc
	for _, p := range []aws.CredentialsProvider{nil, nilProvider} {
		if _, err := iamproof.NewGenerator("eu-west-2", testAudience, p); !errors.Is(err, iamproof.ErrInvalidConfiguration) {
			t.Fatal("nil provider accepted")
		}
	}
	var nilTransport roundTripFunc
	for _, opt := range []iamproof.VerifierOption{nil, iamproof.WithTransport(nil), iamproof.WithTransport(nilTransport), iamproof.WithTimeout(0), iamproof.WithTimeout(-1)} {
		if _, err := iamproof.NewVerifier("eu-west-2", testAudience, opt); !errors.Is(err, iamproof.ErrInvalidConfiguration) {
			t.Fatal("invalid option accepted")
		}
	}
	for _, g := range []*iamproof.Generator{nil, new(iamproof.Generator)} {
		if token, err := g.Generate(t.Context()); !errors.Is(err, iamproof.ErrInvalidConfiguration) || token.Value() != "" {
			t.Fatal("unconfigured generator accepted")
		}
	}
	for _, v := range []*iamproof.Verifier{nil, new(iamproof.Verifier)} {
		caller, err := v.Verify(t.Context(), "")
		assertFailure(t, caller, err, iamproof.ErrInvalidConfiguration)
	}
}

func TestConcurrentUseAndNoVerificationCache(t *testing.T) {
	g := generator(t, "eu-west-2", testCredentials)
	var calls atomic.Int64
	v := verifier(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(200, successXML(testARN)), nil
	}))
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() {
			token, err := g.Generate(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			for range 2 {
				if _, err := v.Verify(t.Context(), token.Value()); err != nil {
					t.Error(err)
				}
			}
		})
	}
	group.Wait()
	if calls.Load() != 64 {
		t.Fatalf("STS calls = %d; want 64", calls.Load())
	}
}
