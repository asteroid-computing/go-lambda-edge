package edge

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const defaultResponseHeaderBudget = 256 * 1024

var (
	errResponseHeaderBudget = errors.New("edge: response header budget exceeded")
	errResponseHeaders      = invocationError("response", ErrResponse, "invalid response headers")
	errResponseTrailers     = invocationError("response", ErrResponse, "response trailers are unsupported", http.ErrNotSupported)
	errResponseUpgrade      = invocationError("response", ErrResponse, "response upgrade is unsupported", http.ErrNotSupported)
	errResponseHeaderRepeat = invocationError("response", ErrResponse, "repeated response field cannot be represented in payload 2.0")
)

// responseHeaders owns its map and value slices. Strings are immutable and may
// share storage with the input. One writer owns the snapshot until completion.
// remaining never refunds bytes trimmed, removed or suppressed at commitment.
type responseHeaders struct {
	fields          http.Header
	remaining       int
	budget          int
	noContentType   bool
	noContentLength bool
}

// canSniffType uses the committed, trimmed fields. A later nonblank encoding
// value still suppresses detection if an earlier repeated value was empty.
func (h *responseHeaders) canSniffType() bool {
	if _, present := h.fields["Content-Type"]; present || h.noContentType {
		return false
	}
	for _, encoding := range h.fields["Content-Encoding"] {
		if encoding != "" {
			return false
		}
	}
	return true
}

func responseTrailers(fields http.Header) error {
	for name, values := range fields {
		declared := len(name) == len("Trailer") && strings.EqualFold(name, "Trailer") && len(values) != 0
		if strings.HasPrefix(name, http.TrailerPrefix) || declared {
			return errResponseTrailers
		}
	}
	return nil
}

// chargeResponseHeaders bounds work and copying before sorting or allocation.
// Empty slices count as one entry so suppression markers also have a cost.
func chargeResponseHeaders(fields http.Header, remaining int) (int, error) {
	for name, values := range fields {
		for i := range max(1, len(values)) {
			for _, size := range []int{len(name), 32} {
				if size > remaining {
					return 0, errResponseHeaderBudget
				}
				remaining -= size
			}
			if len(values) != 0 {
				if len(values[i]) > remaining {
					return 0, errResponseHeaderBudget
				}
				remaining -= len(values[i])
			}
		}
	}
	return remaining, nil
}

func snapshotResponseHeaders(fields http.Header, budget int) (*responseHeaders, error) {
	if budget <= 0 || budget > maxResponseBytes {
		return nil, invocationError("validate", ErrInvalidInvocation, "invalid response header budget")
	}
	remaining, err := chargeResponseHeaders(fields, budget)
	if err != nil {
		return nil, limitError("response", "response_headers", int64(budget), errResponseHeaderBudget)
	}
	keys := make([]string, 0, len(fields))
	for name, values := range fields {
		if strings.HasPrefix(name, http.TrailerPrefix) {
			return nil, errResponseTrailers
		}
		if !validToken(name) {
			return nil, errResponseHeaders
		}
		for _, value := range values {
			if !utf8.ValidString(value) || !validHeaderValue(value) {
				return nil, errResponseHeaders
			}
		}
		keys = append(keys, name)
	}
	slices.Sort(keys)
	h := &responseHeaders{fields: make(http.Header, len(fields)), remaining: remaining, budget: budget}
	for _, key := range keys {
		name := http.CanonicalHeaderKey(key)
		values := slices.Grow(h.fields[name], len(fields[key]))
		for _, value := range fields[key] {
			values = append(values, strings.Trim(value, " \t"))
		}
		h.fields[name] = values
	}
	// Check before Connection removal: a nomination must not hide an attempt
	// to declare trailers or upgrade the response protocol.
	if len(h.fields["Trailer"]) != 0 {
		return nil, errResponseTrailers
	}
	if len(h.fields["Upgrade"]) != 0 {
		return nil, errResponseUpgrade
	}
	for _, value := range h.fields["Connection"] {
		for token := range strings.SplitSeq(value, ",") {
			token = strings.Trim(token, " \t")
			// RFC 9110 §5.6.1.2 requires tolerance of empty list elements.
			// The original byte budget bounds even comma-only input.
			if token == "" {
				continue
			}
			if !validToken(token) {
				return nil, errResponseHeaders
			}
			name := http.CanonicalHeaderKey(token)
			if name == "Upgrade" {
				return nil, errResponseUpgrade
			}
			h.remove(name)
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.remove(name)
	}
	if _, _, err := responseContentLength(h.fields); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *responseHeaders) remove(name string) {
	delete(h.fields, name)
	switch name {
	case "Content-Type":
		h.noContentType = true
	case "Content-Length":
		h.noContentLength = true
	}
}

// automatic adds a canonical field only when absent and not suppressed. It is
// used for generated Content-Type/Content-Length; removing a field never creates
// new budget. A failed addition leaves both fields and remaining unchanged.
func (h *responseHeaders) automatic(name, value string) error {
	if _, present := h.fields[name]; present {
		return nil
	}
	if name == "Content-Type" && h.noContentType || name == "Content-Length" && h.noContentLength {
		return nil
	}
	remaining, err := chargeResponseHeaders(http.Header{name: {value}}, h.remaining)
	if err != nil {
		return limitError("response", "response_headers", int64(h.budget), errResponseHeaderBudget)
	}
	h.fields[name] = []string{value}
	h.remaining = remaining
	return nil
}

func responseContentLength(fields http.Header) (int64, bool, error) {
	values := fields["Content-Length"]
	if len(values) == 0 {
		return 0, false, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, false, errResponseHeaders
	}
	for i := range len(values[0]) {
		if values[0][i] < '0' || values[0][i] > '9' {
			return 0, false, errResponseHeaders
		}
	}
	n, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return 0, false, errResponseHeaders
	}
	return n, true, nil
}

// v1 returns an independent multivalue projection, omitting suppression markers.
// It is also the header representation used by REST streaming metadata.
func (h *responseHeaders) v1() map[string][]string {
	count := 0
	for _, values := range h.fields {
		if len(values) != 0 {
			count++
		}
	}
	if count == 0 {
		return nil
	}
	fields := make(map[string][]string, count)
	for name, values := range h.fields {
		if len(values) != 0 {
			fields[name] = slices.Clone(values)
		}
	}
	return fields
}

func (h *responseHeaders) v2() (map[string]string, []string, error) {
	// Validate multiplicity before allocating the projection or joining values.
	count := 0
	for name, values := range h.fields {
		if name == "Set-Cookie" || len(values) == 0 {
			continue
		}
		if len(values) > 1 && !combinableResponseField(name) {
			return nil, nil, errResponseHeaderRepeat
		}
		count++
	}
	fields := make(map[string]string, count)
	for name, values := range h.fields {
		if name == "Set-Cookie" || len(values) == 0 {
			continue
		}
		fields[name] = strings.Join(values, ", ")
	}
	return fields, slices.Clone(h.fields["Set-Cookie"]), nil
}

// The field-specific list/dictionary audit and references are in decision 0010.
// This immutable switch permits representation conversion, not semantic repair.
func combinableResponseField(name string) bool {
	switch name {
	case "Accept-Ranges", "Allow", "Content-Encoding", "Content-Language", "Vary", "Www-Authenticate", "Cache-Control":
		return true
	case "Access-Control-Allow-Headers", "Access-Control-Allow-Methods", "Access-Control-Expose-Headers":
		return true
	case "Link", "Content-Security-Policy", "Content-Security-Policy-Report-Only", "Referrer-Policy", "Server-Timing", "Accept-Patch", "Accept-Ch":
		return true
	case "Cache-Status", "Proxy-Status", "Content-Digest", "Repr-Digest", "Want-Content-Digest", "Want-Repr-Digest", "Signature", "Signature-Input":
		return true
	default:
		return false
	}
}
