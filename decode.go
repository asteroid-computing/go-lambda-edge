package edge

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"

	"github.com/aws/aws-lambda-go/events"
)

// decodedEvent contains exactly one request on success.
// Authorizer JSON belongs to this value, independently of the input buffer;
// it is not decoded into the SDK's string/float64 claim representations.
// No identity is established here.
type decodedEvent struct {
	version    string
	v1         *events.APIGatewayProxyRequest
	v2         *events.APIGatewayV2HTTPRequest
	authorizer jsontext.Value
}

func decodeEvent(payload []byte) (decodedEvent, error) {
	// This small first pass validates all JSON syntax, including unknown and opaque fields, and retains presence where SDK scalar zero values cannot.
	var envelope *struct {
		Version         jsontext.Value `json:"version"`
		IsBase64Encoded jsontext.Value `json:"isBase64Encoded"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		// JSON errors can contain object names and input values.
		// Do not expose them, even through an unwrap chain, at this credential-bearing boundary.
		return decodedEvent{}, invocationError("decode", ErrInvalidEvent, "invalid event JSON")
	}
	if envelope == nil {
		return decodedEvent{}, invocationError("decode", ErrInvalidEvent, "event must be a JSON object")
	}
	var version string
	if len(envelope.Version) != 0 {
		if err := json.Unmarshal(envelope.Version, &version); err != nil {
			return decodedEvent{}, invocationError("decode", ErrInvalidEvent, "invalid payload version")
		}
		if version != "1.0" && version != "2.0" {
			return decodedEvent{}, invocationError("decode", ErrUnsupportedEvent, "unsupported payload version")
		}
	}
	if len(envelope.IsBase64Encoded) != 0 && envelope.IsBase64Encoded.Kind() != 't' && envelope.IsBase64Encoded.Kind() != 'f' {
		return decodedEvent{}, invocationError("decode", ErrInvalidEvent, "isBase64Encoded must be a boolean")
	}

	if version == "2.0" {
		var wire struct {
			events.APIGatewayV2HTTPRequest
			RequestContext struct {
				events.APIGatewayV2HTTPRequestContext
				Authorizer jsontext.Value `json:"authorizer"`
			} `json:"requestContext"`
		}
		if err := json.Unmarshal(payload, &wire, eventStringOptions); err != nil {
			return decodedEvent{}, invocationError("decode", ErrInvalidEvent, "invalid v2 event fields")
		}
		event := wire.APIGatewayV2HTTPRequest
		event.RequestContext = wire.RequestContext.APIGatewayV2HTTPRequestContext
		if err := validateV2(event); err != nil {
			return decodedEvent{}, err
		}
		return decodedEvent{version: version, v2: &event, authorizer: wire.RequestContext.Authorizer}, nil
	}

	var wire struct {
		events.APIGatewayProxyRequest
		RequestContext struct {
			events.APIGatewayProxyRequestContext
			Authorizer jsontext.Value `json:"authorizer"`
		} `json:"requestContext"`
	}
	if err := json.Unmarshal(payload, &wire, eventStringOptions); err != nil {
		return decodedEvent{}, invocationError("decode", ErrInvalidEvent, "invalid v1 event fields")
	}
	event := wire.APIGatewayProxyRequest
	event.RequestContext = wire.RequestContext.APIGatewayProxyRequestContext
	if err := validateV1(event); err != nil {
		return decodedEvent{}, err
	}
	return decodedEvent{version: version, v1: &event, authorizer: wire.RequestContext.Authorizer}, nil
}

func validateV1(event events.APIGatewayProxyRequest) error {
	if event.RequestContext.APIID == "" {
		return invocationError("validate", ErrInvalidEvent, "missing requestContext.apiId")
	}
	if event.HTTPMethod == "" {
		return invocationError("validate", ErrInvalidEvent, "missing httpMethod")
	}
	if event.Path == "" {
		return invocationError("validate", ErrInvalidEvent, "missing path")
	}
	return nil
}

func validateV2(event events.APIGatewayV2HTTPRequest) error {
	if event.Version != "" && event.Version != "2.0" {
		return invocationError("validate", ErrInvalidEvent, "conflicting v2 payload version")
	}
	if event.RequestContext.APIID == "" {
		return invocationError("validate", ErrInvalidEvent, "missing requestContext.apiId")
	}
	if event.RequestContext.HTTP.Method == "" {
		return invocationError("validate", ErrInvalidEvent, "missing requestContext.http.method")
	}
	if event.RawPath == "" {
		return invocationError("validate", ErrInvalidEvent, "missing rawPath")
	}
	return nil
}

// Optional collections may be null, but their string elements must be strings.
// Scope this rule to collections: AWS also sends null optional scalar metadata and body fields.
// Options and unmarshaler lists are immutable after creation.
var eventStringOptions = json.WithUnmarshalers(json.JoinUnmarshalers(
	json.UnmarshalFunc(func(data []byte, out *map[string]string) error {
		return json.Unmarshal(data, out, nonNullStringOptions)
	}),
	json.UnmarshalFunc(func(data []byte, out *map[string][]string) error {
		return json.Unmarshal(data, out, nonNullStringOptions)
	}),
	json.UnmarshalFunc(func(data []byte, out *[]string) error {
		return json.Unmarshal(data, out, nonNullStringOptions)
	}),
))

var nonNullStringOptions = json.WithUnmarshalers(json.UnmarshalFunc(func(data []byte, out *string) error {
	if jsontext.Value(data).Kind() != '"' {
		return errors.New("expected a JSON string")
	}
	return json.Unmarshal(data, out)
}))
