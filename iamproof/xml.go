package iamproof

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

const stsNamespace = "https://sts.amazonaws.com/doc/2011-06-15/"

// parseResponse consumes one complete, bounded document. Only direct children
// in the expected namespace can supply identity or error fields; duplicate
// results and fields fail instead of taking the last value as xml.Unmarshal does.
// Unknown metadata is ignored, but still parsed for well-formedness.
func parseResponse(wire []byte, success bool) (map[string]string, error) {
	root, result := "GetCallerIdentityResponse", "GetCallerIdentityResult"
	fields := map[string]string{"Account": "", "Arn": "", "UserId": ""}
	if !success {
		root, result = "ErrorResponse", "Error"
		fields = map[string]string{"Code": ""}
	}
	decoder := xml.NewDecoder(bytes.NewReader(wire))
	var stack []xml.Name
	seen := make(map[string]bool)
	var rootSeen, resultSeen bool
	var field string
	var value strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, ErrUnavailable
		}
		switch token := token.(type) {
		case xml.StartElement:
			if field != "" {
				return nil, ErrUnavailable // Identity fields contain text, not markup.
			}
			switch len(stack) {
			case 0:
				if rootSeen || token.Name != (xml.Name{Space: stsNamespace, Local: root}) {
					return nil, ErrUnavailable
				}
				rootSeen = true
			case 1:
				if token.Name.Local == result {
					if resultSeen || token.Name.Space != stsNamespace {
						return nil, ErrUnavailable
					}
					resultSeen = true
				}
			case 2:
				if stack[1] == (xml.Name{Space: stsNamespace, Local: result}) {
					if _, known := fields[token.Name.Local]; known {
						if seen[token.Name.Local] || token.Name.Space != stsNamespace {
							return nil, ErrUnavailable
						}
						seen[token.Name.Local] = true
						field = token.Name.Local
						value.Reset()
					}
				}
			}
			stack = append(stack, token.Name)
		case xml.EndElement:
			if field != "" {
				fields[field] = value.String()
				field = ""
			}
			stack = stack[:len(stack)-1] // Decoder.Token checks matching/nesting.
		case xml.CharData:
			if field != "" {
				value.Write(token)
			} else if (len(stack) < 2 || len(stack) == 2 && stack[1].Local == result) && len(bytes.TrimSpace(token)) != 0 {
				return nil, ErrUnavailable
			}
		case xml.Directive:
			return nil, ErrUnavailable // No DTD or entity declarations in STS XML.
		}
	}
	if !rootSeen || !resultSeen || len(stack) != 0 {
		return nil, ErrUnavailable
	}
	for _, value := range fields {
		if value == "" {
			return nil, ErrUnavailable
		}
	}
	return fields, nil
}
