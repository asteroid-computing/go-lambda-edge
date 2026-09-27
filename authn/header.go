package authn

import (
	"net/http"
	"strings"
)

type scheme uint8

const (
	noScheme scheme = iota
	bearer
	iamProof
)

func (a *Authenticator) selectCredential(headers http.Header) (scheme, string, error) {
	var value string
	var found, multiple bool
	remaining := a.cfg.MaxAuthorizationBytes
	for name, values := range headers {
		if len(name) != len("Authorization") || http.CanonicalHeaderKey(name) != "Authorization" {
			continue
		}
		for _, v := range values {
			if len(v) > remaining {
				return noScheme, "", a.failure(ErrHeaderTooLarge, http.StatusRequestHeaderFieldsTooLarge, noScheme)
			}
			remaining -= len(v)
			multiple = multiple || found
			value, found = v, true
		}
	}
	if !found {
		return noScheme, "", a.failure(ErrMissingCredentials, http.StatusUnauthorized, noScheme)
	}
	if multiple || strings.Contains(value, ",") {
		return noScheme, "", a.failure(ErrMalformedCredentials, http.StatusBadRequest, noScheme)
	}
	value = strings.Trim(value, " \t")
	end := strings.IndexAny(value, " \t")
	if end < 0 {
		end = len(value)
	}
	name := value[:end]
	selected := noScheme
	switch {
	case strings.EqualFold(name, "Bearer") && a.cfg.Bearer != nil:
		selected = bearer
	case strings.EqualFold(name, "EdgeIAM") && a.cfg.IAMProof != nil:
		selected = iamProof
	}
	malformed := func() (scheme, string, error) {
		return noScheme, "", a.failure(ErrMalformedCredentials, http.StatusBadRequest, selected)
	}
	if name == "" || end == len(value) || value[end] != ' ' {
		return malformed()
	}
	for i := range len(name) {
		if !tokenChar(name[i]) {
			return malformed()
		}
	}
	credential := strings.TrimLeft(value[end:], " ")
	if credential == "" {
		return malformed()
	}
	for i := range len(credential) {
		if credential[i] <= 0x20 || credential[i] >= 0x7f {
			return malformed()
		}
	}
	if selected == noScheme {
		return noScheme, "", a.failure(ErrUnsupportedScheme, http.StatusUnauthorized, noScheme)
	}
	if !token68(credential) {
		return malformed()
	}
	return selected, credential, nil
}

func tokenChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(b))
}

func token68(value string) bool {
	content := strings.TrimRight(value, "=")
	if content == "" {
		return false
	}
	for i := range len(content) {
		b := content[i]
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~+/", rune(b))) {
			return false
		}
	}
	return true
}
