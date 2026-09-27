package edge

import (
	"encoding/json/v2"
	"errors"
)

// Edge's conservative interpretation includes the entire eight-NUL delimiter within AWS's documented first 16 KB.
// This is not a stream body limit.
const maxStreamPrefixBytes = 16000

var errStreamPrefixTooLarge = errors.New("edge: response metadata exceeds streaming prefix limit")

func encodeStreamPrefix(status int, headers *responseHeaders) ([]byte, error) {
	if status < 200 || status > 599 {
		return nil, invocationError(OperationResponse, ErrResponse, "invalid final response status")
	}
	metadata := struct {
		StatusCode        int                 `json:"statusCode"`
		MultiValueHeaders map[string][]string `json:"multiValueHeaders,omitzero"`
	}{StatusCode: status, MultiValueHeaders: headers.v1()}
	out := responseBuffer{limit: maxStreamPrefixBytes - 8}
	if err := json.MarshalWrite(&out, metadata); err != nil {
		if errors.Is(err, errResponseTooLarge) {
			return nil, limitError(OperationEncode, ResourceStreamMetadata, maxStreamPrefixBytes, errStreamPrefixTooLarge)
		}
		return nil, invocationError(OperationEncode, ErrResponse, "streaming metadata JSON encoding failed")
	}
	// Allocate exactly the framed length.
	// The final zeroed bytes are the delimiter.
	prefix := make([]byte, len(out.data)+8)
	copy(prefix, out.data)
	return prefix, nil
}
