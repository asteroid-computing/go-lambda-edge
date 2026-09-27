package authn

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/jsontext"
	"strconv"
	"strings"
	"time"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func (v *CognitoVerifier) verify(ctx context.Context, token string) (identity.Caller, error) {
	if len(token) > v.cfg.MaxTokenBytes {
		return identity.Caller{}, ErrInvalidCredentials
	}
	headerText, rest, ok := strings.Cut(token, ".")
	if !ok {
		return identity.Caller{}, ErrInvalidCredentials
	}
	payloadText, signatureText, ok := strings.Cut(rest, ".")
	if !ok {
		return identity.Caller{}, ErrInvalidCredentials
	}
	headerBytes, err := decodeJWTPart(headerText, 4096)
	if err != nil {
		return identity.Caller{}, err
	}
	header, err := jwtObject(headerBytes)
	if err != nil || jwtText(header, "alg") != "RS256" || !validKID(jwtText(header, "kid")) {
		return identity.Caller{}, ErrInvalidCredentials
	}
	if _, present := header["typ"]; present && jwtText(header, "typ") != "JWT" {
		return identity.Caller{}, ErrInvalidCredentials
	}
	for _, name := range []string{"crit", "b64", "jku", "jwk", "x5u", "x5c", "zip", "cty"} {
		if _, present := header[name]; present {
			return identity.Caller{}, ErrInvalidCredentials
		}
	}
	payload, err := decodeJWTPart(payloadText, v.cfg.MaxTokenBytes)
	if err != nil {
		return identity.Caller{}, err
	}
	signature, err := decodeJWTPart(signatureText, 512)
	if err != nil || len(signature) < 256 {
		return identity.Caller{}, ErrInvalidCredentials
	}
	claims, err := identity.ParseClaims(jsontext.Value(payload), identity.WithClaimsBudget(v.cfg.ClaimsBudget))
	if err != nil || claimText(claims, "iss") != v.cfg.Issuer || claimText(claims, "token_use") != "access" {
		return identity.Caller{}, ErrInvalidCredentials
	}
	client, allowed := v.clients[claimText(claims, "client_id")]
	if !allowed {
		return identity.Caller{}, ErrInvalidCredentials
	}
	if aud, present := claims.Lookup("aud"); present {
		text, ok := aud.Text()
		if !ok || text == "" || text != client.Audience {
			return identity.Caller{}, ErrInvalidCredentials
		}
	} else if !client.AllowUnbound {
		return identity.Caller{}, ErrInvalidCredentials
	}
	dates, err := readTokenDates(claims)
	if err != nil || !dates.valid(time.Now(), v.cfg.ClockSkew) {
		return identity.Caller{}, ErrInvalidCredentials
	}
	if err := ctx.Err(); err != nil {
		return identity.Caller{}, err
	}
	key, err := v.key(ctx, jwtText(header, "kid"))
	if err != nil {
		return identity.Caller{}, err
	}
	if !verifyRS256(key.public, token[:len(headerText)+1+len(payloadText)], signature) || !dates.valid(time.Now(), v.cfg.ClockSkew) {
		return identity.Caller{}, ErrInvalidCredentials
	}
	caller, err := identity.NewJWT(claims, identity.SourceLocallyVerifiedToken)
	if err != nil {
		return identity.Caller{}, ErrInvalidCredentials
	}
	// A process pause after key lookup must not allow a now-expired key or token to establish an identity on resumption.
	now := time.Now()
	if !dates.valid(now, v.cfg.ClockSkew) {
		return identity.Caller{}, ErrInvalidCredentials
	}
	if !now.Before(key.expires) {
		return identity.Caller{}, ErrUnavailable
	}
	return caller, nil
}

func verifyRS256(key *rsa.PublicKey, input string, signature []byte) bool {
	if len(signature) != key.Size() {
		return false
	}
	digest := sha256.Sum256([]byte(input))
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) == nil
}

func decodeJWTPart(text string, limit int) ([]byte, error) {
	if text == "" || len(text) > base64.RawURLEncoding.EncodedLen(limit) {
		return nil, ErrInvalidCredentials
	}
	// Strict decoding checks unused bits but still ignores CR/LF.
	// Check the alphabet first so whitespace, padding and extra segments cannot disappear.
	for i := range len(text) {
		c := text[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return nil, ErrInvalidCredentials
		}
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(text)
	if err != nil || len(b) > limit {
		return nil, ErrInvalidCredentials
	}
	return b, nil
}

func claimText(claims identity.Claims, name string) string {
	v, _ := claims.Lookup(name)
	s, _ := v.Text()
	return s
}

type tokenDates struct {
	exp time.Time
	iat time.Time
	nbf time.Time
}

func readTokenDates(claims identity.Claims) (tokenDates, error) {
	var dates tokenDates
	for _, field := range []struct {
		name string
		dest *time.Time
	}{{"exp", &dates.exp}, {"iat", &dates.iat}, {"nbf", &dates.nbf}} {
		value, present := claims.Lookup(field.name)
		if !present && field.name == "nbf" {
			continue
		}
		text, ok := value.NumberText()
		if !ok || len(text) == 0 || len(text) > 12 {
			return tokenDates{}, ErrInvalidCredentials
		}
		for i := range len(text) {
			if text[i] < '0' || text[i] > '9' {
				return tokenDates{}, ErrInvalidCredentials
			}
		}
		seconds, err := strconv.ParseInt(text, 10, 64)
		if err != nil || seconds > 253402300799 {
			return tokenDates{}, ErrInvalidCredentials
		}
		*field.dest = time.Unix(seconds, 0)
	}
	if !dates.iat.Before(dates.exp) || !dates.nbf.IsZero() && !dates.nbf.Before(dates.exp) {
		return tokenDates{}, ErrInvalidCredentials
	}
	return dates, nil
}

func (d tokenDates) valid(now time.Time, skew time.Duration) bool {
	return now.Before(d.exp.Add(skew)) && !d.iat.After(now.Add(skew)) && (d.nbf.IsZero() || !d.nbf.After(now.Add(skew)))
}
