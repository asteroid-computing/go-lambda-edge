// Package iamproofprobe checks proposed protocol mechanics using synthetic credentials.
// It is not an authentication implementation or an AWS acceptance test.
package iamproofprobe

import (
	"encoding/base64"
	"encoding/json/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type envelope struct {
	Audience     string `json:"audience"`
	Credential   string `json:"credential"`
	Date         string `json:"date"`
	Signature    string `json:"signature"`
	SessionToken string `json:"sessionToken,omitempty"`
}

func TestReconstruction(t *testing.T) {
	for _, region := range []string{"eu-west-2", "us-gov-west-1", "cn-north-1"} {
		t.Run(region, func(t *testing.T) {
			for _, size := range []int{0, 1024, 4096, 6144} {
				t.Run(strconv.Itoa(size), func(t *testing.T) {
					ctx := t.Context()
					endpoint, err := sts.NewDefaultEndpointResolverV2().ResolveEndpoint(ctx, sts.EndpointParameters{
						Region:            new(region),
						UseGlobalEndpoint: new(false),
						UseFIPS:           new(false),
						UseDualStack:      new(false),
					})
					if err != nil {
						t.Fatal(err)
					}
					creds := aws.Credentials{
						AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
						SecretAccessKey: "synthetic-secret-never-valid-in-AWS",
						SessionToken:    strings.Repeat("+/=a", size/4),
					}
					when := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
					req := request(t, endpoint.URI.String(), "orders.production")
					signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableHeaderHoisting = true })
					signedURL, _, err := signer.PresignHTTP(ctx, creds, req, emptyHash, "sts", region, when)
					if err != nil {
						t.Fatal(err)
					}
					u, err := url.Parse(signedURL)
					if err != nil {
						t.Fatal(err)
					}
					q := u.Query()
					if q.Get("X-Amz-SignedHeaders") != "host;x-edge-iam-audience" {
						t.Fatal("unexpected signed headers")
					}
					wire, err := json.Marshal(envelope{
						Audience:     "orders.production",
						Credential:   q.Get("X-Amz-Credential"),
						Date:         q.Get("X-Amz-Date"),
						Signature:    q.Get("X-Amz-Signature"),
						SessionToken: q.Get("X-Amz-Security-Token"),
					})
					if err != nil {
						t.Fatal(err)
					}
					token := "v1." + base64.RawURLEncoding.EncodeToString(wire)
					decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "v1."))
					if err != nil {
						t.Fatal(err)
					}
					var proof envelope
					if err := json.Unmarshal(decoded, &proof, json.RejectUnknownMembers(true)); err != nil {
						t.Fatal(err)
					}
					rebuilt := request(t, endpoint.URI.String(), "orders.production")
					params := rebuilt.URL.Query()
					params.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
					params.Set("X-Amz-Credential", proof.Credential)
					params.Set("X-Amz-Date", proof.Date)
					params.Set("X-Amz-Signature", proof.Signature)
					params.Set("X-Amz-SignedHeaders", "host;x-edge-iam-audience")
					if proof.SessionToken != "" {
						params.Set("X-Amz-Security-Token", proof.SessionToken)
					}
					rebuilt.URL.RawQuery = params.Encode()
					// SigV4 canonicalizes query order;
					// the SDK appends the signature after signing, whereas url.Values.Encode sorts all parameters.
					u.RawQuery = q.Encode()
					if rebuilt.URL.String() != u.String() {
						t.Fatal("reconstruction changed the presigned URL")
					}
					tampered := request(t, endpoint.URI.String(), "orders.staging")
					other, _, err := signer.PresignHTTP(ctx, creds, tampered, emptyHash, "sts", region, when)
					if err != nil {
						t.Fatal(err)
					}
					otherURL, err := url.Parse(other)
					if err != nil {
						t.Fatal(err)
					}
					if otherURL.Query().Get("X-Amz-Signature") == proof.Signature {
						t.Fatal("audience did not affect signature")
					}
					t.Logf("session bytes=%d compact token bytes=%d URL token bytes=%d host=%s", size, len(token), 3+base64.RawURLEncoding.EncodedLen(len(signedURL)), u.Host)
				})
			}
		})
	}
}

func request(t *testing.T, endpoint, audience string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Edge-IAM-Audience", audience)
	q := req.URL.Query()
	q.Set("Action", "GetCallerIdentity")
	q.Set("Version", "2011-06-15")
	q.Set("X-Amz-Expires", "60")
	req.URL.RawQuery = q.Encode()
	return req
}
