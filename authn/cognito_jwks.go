package authn

import (
	"bytes"
	"crypto/rsa"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"math/big"
	"unicode/utf8"
)

// jwtObject validates all JSON, including ignored extension values, before
// allocating the member map. JSON v2 rejects duplicate names and invalid UTF-8.
func jwtObject(data []byte) (map[string]jsontext.Value, error) {
	d := jsontext.NewDecoder(bytes.NewReader(data))
	if d.PeekKind() != '{' {
		return nil, ErrInvalidCredentials
	}
	for {
		if _, err := d.ReadToken(); err != nil || d.StackDepth() > 64 {
			return nil, ErrInvalidCredentials
		}
		if d.StackDepth() == 0 {
			break
		}
	}
	if _, err := d.ReadToken(); err != io.EOF {
		return nil, ErrInvalidCredentials
	}
	var object map[string]jsontext.Value
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, ErrInvalidCredentials
	}
	return object, nil
}

func jwtText(object map[string]jsontext.Value, name string) string {
	var s string
	if err := json.Unmarshal(object[name], &s); err != nil {
		return ""
	}
	return s
}

func validKID(kid string) bool { return kid != "" && len(kid) <= 256 && utf8.ValidString(kid) }

func parseJWKS(data []byte) (map[string]*rsa.PublicKey, error) {
	if len(data) > 64*1024 {
		return nil, ErrUnavailable
	}
	object, err := jwtObject(data)
	if err != nil {
		return nil, ErrUnavailable
	}
	var entries []map[string]jsontext.Value
	if err := json.Unmarshal(object["keys"], &entries); err != nil || len(entries) == 0 || len(entries) > 32 {
		return nil, ErrUnavailable
	}
	keys := make(map[string]*rsa.PublicKey)
	seen := make(map[string]bool)
	for _, entry := range entries {
		kid := jwtText(entry, "kid")
		if kid != "" {
			if seen[kid] {
				return nil, ErrUnavailable
			}
			seen[kid] = true
		}
		kty := jwtText(entry, "kty")
		if kty == "" {
			return nil, ErrUnavailable
		}
		if kty != "RSA" {
			continue
		}
		unsupported := false
		for _, field := range []struct{ name, want string }{{"alg", "RS256"}, {"use", "sig"}} {
			if _, present := entry[field.name]; present {
				text := jwtText(entry, field.name)
				if text == "" {
					return nil, ErrUnavailable
				}
				unsupported = unsupported || text != field.want
			}
		}
		if unsupported {
			continue
		}
		if !validKID(kid) {
			return nil, ErrUnavailable
		}
		if raw, present := entry["key_ops"]; present {
			var operations []string
			if err := json.Unmarshal(raw, &operations); err != nil || len(operations) != 1 || operations[0] != "verify" {
				return nil, ErrUnavailable
			}
		}
		for _, private := range []string{"d", "p", "q", "dp", "dq", "qi", "oth"} {
			if _, present := entry[private]; present {
				return nil, ErrUnavailable
			}
		}
		n, err := decodeJWTPart(jwtText(entry, "n"), 512)
		if err != nil || len(n) < 256 || n[0] == 0 || n[len(n)-1]&1 == 0 {
			return nil, ErrUnavailable
		}
		modulus := new(big.Int).SetBytes(n)
		if modulus.BitLen() < 2048 || modulus.BitLen() > 4096 {
			return nil, ErrUnavailable
		}
		e, err := decodeJWTPart(jwtText(entry, "e"), 4)
		if err != nil || len(e) == 0 || e[0] == 0 {
			return nil, ErrUnavailable
		}
		var exponent int64
		for _, b := range e {
			exponent = exponent<<8 | int64(b)
		}
		if exponent < 3 || exponent > 1<<31-1 || exponent&1 == 0 {
			return nil, ErrUnavailable
		}
		keys[kid] = &rsa.PublicKey{N: modulus, E: int(exponent)}
	}
	if len(keys) == 0 {
		return nil, ErrUnavailable
	}
	return keys, nil
}
