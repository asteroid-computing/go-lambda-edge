package authn

import (
	"encoding/base64"
	"testing"
)

// Public key, encoded signing input and signature from RFC 7515 Appendix A.2:
// https://www.rfc-editor.org/rfc/rfc7515.html#appendix-A.2
// This independently published vector tests the primitive, not our Cognito
// profile: the original RFC token has neither a Cognito issuer nor access claims.
func TestRS256RFC7515Vector(t *testing.T) {
	const n = "ofgWCuLjybRlzo0tZWJjNiuSfb4p4fAkd_wWJcyQoTbji9k0l8W26mPddx" +
		"HmfHQp-Vaw-4qPCJrcS2mJPMEzP1Pt0Bm4d4QlL-yRT-SFd2lZS-pCgNMs" +
		"D1W_YpRPEwOWvG6b32690r2jZ47soMZo9wGzjb_7OMg0LOL-bSf63kpaSH" +
		"SXndS5z5rexMdbBYUsLA9e-KXBdQOS-UTo7WTBEMa2R2CapHg665xsmtdV" +
		"MTBQY4uDZlxvb3qCo5ZwKh9kG4LT6_I5IhlJH7aGhyxXFvUK-DWNmoudF8" +
		"NAco9_h9iaGNj8q2ethFkMLs91kzk2PAcDTW9gb54h4FRWyuXpoQ"
	const input = "eyJhbGciOiJSUzI1NiJ9." +
		"eyJpc3MiOiJqb2UiLA0KICJleHAiOjEzMDA4MTkzODAsDQogImh0dHA6Ly9leGFt" +
		"cGxlLmNvbS9pc19yb290Ijp0cnVlfQ"
	const signature = "cC4hiUPoj9Eetdgtv3hF80EGrhuB__dzERat0XF9g2VtQgr9PJbu3XOiZj5RZmh7" +
		"AAuHIm4Bh-0Qc_lF5YKt_O8W2Fp5jujGbds9uJdbF9CUAr7t1dnZcAcQjbKBYNX4" +
		"BAynRFdiuB--f_nZLgrnbyTyWzO75vRK5h6xBArLIARNPvkSjtQBMHlb1L07Qe7K" +
		"0GarZRmB_eSN9383LcOLn6_dO--xi12jzDwusC-eOkHWEsqtFZESc6BfI7noOPqv" +
		"hJ1phCnvWh6IeYI2w9QOYEUipUTI8np6LbgGY9Fs98rqVt5AXLIhWkWywlVmtVrB" +
		"p0igcN_IoypGlUPQGe77Rw"
	keys, err := parseJWKS([]byte(`{"keys":[{"kty":"RSA","kid":"rfc","n":"` + n + `","e":"AQAB"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyRS256(keys["rfc"], input, sig) {
		t.Fatal("independent RS256 known-answer vector rejected")
	}
	if verifyRS256(keys["rfc"], input+"A", sig) || verifyRS256(keys["rfc"], input, sig[1:]) {
		t.Fatal("altered RFC vector accepted")
	}
	sig[10] ^= 1
	if verifyRS256(keys["rfc"], input, sig) {
		t.Fatal("altered signature accepted")
	}
}
