package edge

import (
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Lambda's documented MB unit is binary. This is the complete buffered payload
// budget, not an allowance for a body plus an uncounted JSON envelope.
const maxResponseBytes = 6 * 1024 * 1024

var errResponseTooLarge = errors.New("edge: response exceeds buffered payload limit")

// marshalResponse is only for the raw invocation path. Typed entry points leave
// envelope serialization, and its exact final size, to their caller.
func marshalResponse(response any) ([]byte, error) {
	var out responseBuffer
	if err := json.MarshalWrite(&out, response); err != nil {
		if errors.Is(err, errResponseTooLarge) {
			return nil, limitError("encode", "buffered_envelope", maxResponseBytes, errResponseTooLarge)
		}
		// Codec errors can include application values. Do not expose that chain.
		return nil, invocationError("encode", ErrResponse, "response JSON encoding failed")
	}
	return out.data, nil
}

// responseBuffer bounds both length and backing capacity. MarshalWrite can still
// allocate temporary codec storage; this is not a total-memory guarantee.
type responseBuffer struct {
	data []byte
	// A zero limit selects the buffered-response ceiling.
	limit int
}

func (b *responseBuffer) Write(p []byte) (int, error) {
	limit := b.limit
	if limit == 0 {
		limit = maxResponseBytes
	}
	if len(p) > limit-len(b.data) {
		return 0, errResponseTooLarge
	}
	n := len(b.data) + len(p)
	if n > cap(b.data) {
		capacity := min(limit, max(n, 2*cap(b.data), 512))
		data := make([]byte, len(b.data), capacity)
		copy(data, b.data)
		b.data = data
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

// encodeResponseBody receives the finalized, canonical response headers. It
// changes only the gateway envelope representation, never the content bytes.
func encodeResponseBody(body []byte, header http.Header) (string, bool, error) {
	if len(body) > maxResponseBytes {
		return "", false, limitError("response", "buffered_body", maxResponseBytes, errResponseTooLarge)
	}
	if len(body) == 0 {
		return "", false, nil
	}
	if textualResponse(header) && utf8.Valid(body) {
		return string(body), false, nil
	}
	if base64.StdEncoding.EncodedLen(len(body)) > maxResponseBytes {
		return "", false, limitError("response", "buffered_body", maxResponseBytes, errResponseTooLarge)
	}
	return base64.StdEncoding.EncodeToString(body), true, nil
}

func textualResponse(header http.Header) bool {
	encodings := header.Values("Content-Encoding")
	if len(encodings) != 0 && (len(encodings) != 1 || !strings.EqualFold(strings.TrimSpace(encodings[0]), "identity")) {
		return false
	}
	types := header.Values("Content-Type")
	if len(types) != 1 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(types[0])
	if err != nil {
		return false
	}
	// ParseMediaType also accepts a bare token (useful for Content-Disposition).
	// A response media type needs both a type and a subtype.
	if !strings.Contains(mediaType, "/") {
		return false
	}
	if strings.HasPrefix(mediaType, "text/") || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml") {
		return true
	}
	switch mediaType {
	case "application/json", "application/xml", "application/javascript", "application/x-www-form-urlencoded":
		return true
	default:
		return false
	}
}
