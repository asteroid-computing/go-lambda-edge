package iamproof

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const (
	maxProofBytes    = 8 * 1024
	maxResponseBytes = 64 * 1024
	proofLifetime    = time.Minute
	futureTolerance  = 30 * time.Second
	dateLayout       = "20060102T150405Z"
	audienceHeader   = "X-Edge-IAM-Audience"
	emptyHash        = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

var regionPattern = regexp.MustCompile(`^[a-z]{2,8}(-[a-z]+)+-[0-9]+$`)

type configuration struct {
	region    string
	audience  string
	endpoint  url.URL
	partition string
}

func configure(region, audience string) (configuration, error) {
	if len(region) > 63 || !regionPattern.MatchString(region) || strings.HasPrefix(region, "fips-") || !validAudience(audience) {
		return configuration{}, ErrInvalidConfiguration
	}
	endpoint, err := sts.NewDefaultEndpointResolverV2().ResolveEndpoint(context.Background(), sts.EndpointParameters{
		Region: new(region), UseGlobalEndpoint: new(false), UseFIPS: new(false), UseDualStack: new(false),
	})
	if err != nil {
		return configuration{}, ErrInvalidConfiguration
	}
	// The V2 public endpoint does not expose its partition. Obtain that metadata
	// from the SDK's public legacy resolver, and require both resolvers to agree
	// on the standard endpoint. No private SDK imports or copied partition table.
	metadata, err := sts.NewDefaultEndpointResolver().ResolveEndpoint(region, sts.EndpointResolverOptions{})
	if err != nil || metadata.PartitionID == "" || metadata.SigningRegion != region || metadata.URL != endpoint.URI.String() {
		return configuration{}, ErrInvalidConfiguration
	}
	u := endpoint.URI
	if u.Scheme != "https" || !strings.HasPrefix(u.Host, "sts."+region+".") || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return configuration{}, ErrInvalidConfiguration
	}
	u.Path = "/"
	return configuration{region: region, audience: audience, endpoint: u, partition: metadata.PartitionID}, nil
}

func validAudience(audience string) bool {
	if len(audience) == 0 || len(audience) > 256 {
		return false
	}
	for i := range len(audience) {
		if audience[i] < 0x21 || audience[i] > 0x7e {
			return false
		}
	}
	return true
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

type envelope struct {
	Audience     string `json:"audience"`
	Credential   string `json:"credential"`
	Date         string `json:"date"`
	Signature    string `json:"signature"`
	SessionToken string `json:"sessionToken,omitempty"`
}

func decodeProof(token string, cfg configuration) (envelope, time.Time, error) {
	if len(token) > maxProofBytes || !strings.HasPrefix(token, "v1.") {
		return envelope{}, time.Time{}, ErrInvalidProof
	}
	encoded := token[3:]
	wire, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(wire) != encoded {
		return envelope{}, time.Time{}, ErrInvalidProof
	}
	// Inspect raw members so explicit null and absent optional fields differ.
	// JSON v2 rejects duplicate names, invalid UTF-8 and trailing data by default.
	var members map[string]jsontext.Value
	if err := json.Unmarshal(wire, &members); err != nil || len(members) < 4 || len(members) > 5 {
		return envelope{}, time.Time{}, ErrInvalidProof
	}
	var p envelope
	for name, raw := range members {
		var target *string
		switch name {
		case "audience":
			target = &p.Audience
		case "credential":
			target = &p.Credential
		case "date":
			target = &p.Date
		case "signature":
			target = &p.Signature
		case "sessionToken":
			target = &p.SessionToken
		default:
			return envelope{}, time.Time{}, ErrInvalidProof
		}
		if raw.Kind() != '"' || json.Unmarshal(raw, target) != nil || *target == "" {
			return envelope{}, time.Time{}, ErrInvalidProof
		}
	}
	issued, err := time.Parse(dateLayout, p.Date)
	if err != nil || issued.Format(dateLayout) != p.Date || p.Audience != cfg.audience || len(p.Signature) != 64 {
		return envelope{}, time.Time{}, ErrInvalidProof
	}
	for i := range len(p.Signature) {
		b := p.Signature[i]
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return envelope{}, time.Time{}, ErrInvalidProof
		}
	}
	parts := strings.Split(p.Credential, "/")
	if len(parts) != 5 || !validAccessKey(parts[0]) || parts[1] != p.Date[:8] || parts[2] != cfg.region || parts[3] != "sts" || parts[4] != "aws4_request" {
		return envelope{}, time.Time{}, ErrInvalidProof
	}
	return p, issued, nil
}

func validAccessKey(key string) bool {
	if len(key) < 16 || len(key) > 128 {
		return false
	}
	for i := range len(key) {
		b := key[i]
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_') {
			return false
		}
	}
	return true
}

func fresh(issued, now time.Time) bool {
	return !issued.After(now.Add(futureTolerance)) && now.Before(issued.Add(proofLifetime))
}

func (cfg configuration) request(ctx context.Context) *http.Request {
	u := cfg.endpoint
	u.RawQuery = url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}, "X-Amz-Expires": {"60"}}.Encode()
	req := (&http.Request{Method: http.MethodGet, URL: &u, Header: make(http.Header)}).WithContext(ctx)
	req.Header.Set(audienceHeader, cfg.audience)
	return req
}

func (p envelope) request(ctx context.Context, cfg configuration) *http.Request {
	req := cfg.request(ctx)
	query := req.URL.Query()
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-SignedHeaders", "host;x-edge-iam-audience")
	query.Set("X-Amz-Credential", p.Credential)
	query.Set("X-Amz-Date", p.Date)
	query.Set("X-Amz-Signature", p.Signature)
	if p.SessionToken != "" {
		query.Set("X-Amz-Security-Token", p.SessionToken)
	}
	// SigV4 uses RFC 3986 space escaping, not form encoding's '+'. Preserve
	// the SDK's encoding even for an unusual opaque session-token value.
	req.URL.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
	return req
}
