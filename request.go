package edge

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

func requestV1(ctx context.Context, event events.APIGatewayProxyRequest) (*http.Request, error) {
	if err := validateV1(event); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(event.Path, "/") {
		return nil, invocationError("request", ErrInvalidEvent, "path must begin with a slash")
	}
	query := make(url.Values)
	for key, values := range event.MultiValueQueryStringParameters {
		query[key] = slices.Clone(values)
	}
	for key, value := range event.QueryStringParameters {
		if !slices.Contains(event.MultiValueQueryStringParameters[key], value) {
			query[key] = append(query[key], value)
		}
	}
	header, err := requestHeaders(event.Headers, event.MultiValueHeaders)
	if err != nil {
		return nil, err
	}
	u := &url.URL{Path: event.Path, RawQuery: query.Encode()}
	return newRequest(ctx, &http.Request{
		Method:     event.HTTPMethod,
		URL:        u,
		Header:     header,
		Host:       event.RequestContext.DomainName,
		Proto:      event.RequestContext.Protocol,
		RemoteAddr: event.RequestContext.Identity.SourceIP,
	}, event.Body, event.IsBase64Encoded)
}

func requestV2(ctx context.Context, event events.APIGatewayV2HTTPRequest) (*http.Request, error) {
	if err := validateV2(event); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(event.RawPath, "/") {
		return nil, invocationError("request", ErrInvalidEvent, "rawPath must begin with a slash")
	}
	path, err := url.PathUnescape(event.RawPath)
	if err != nil {
		return nil, invocationError("request", ErrInvalidEvent, "invalid rawPath encoding")
	}
	u := &url.URL{Path: path, RawPath: event.RawPath, RawQuery: event.RawQueryString}
	// EscapedPath ignores an invalid RawPath hint. Reject rather than silently
	// replacing raw delimiters, whitespace, or an invalid encoded spelling.
	if u.EscapedPath() != event.RawPath {
		return nil, invocationError("request", ErrInvalidEvent, "invalid rawPath syntax")
	}
	// Query decoding remains the handler's job, but a request target cannot
	// contain literal ASCII whitespace or control bytes on an HTTP request line.
	for i := 0; i < len(event.RawQueryString); i++ {
		if event.RawQueryString[i] <= ' ' || event.RawQueryString[i] == 0x7f {
			return nil, invocationError("request", ErrInvalidEvent, "invalid raw query syntax")
		}
	}
	header, err := requestHeaders(event.Headers, nil)
	if err != nil {
		return nil, err
	}
	if len(event.Cookies) != 0 {
		cookie := strings.Join(event.Cookies, "; ")
		if !validHeaderValue(cookie) {
			return nil, invocationError("request", ErrInvalidEvent, "invalid cookie header")
		}
		header.Set("Cookie", cookie)
	}
	return newRequest(ctx, &http.Request{
		Method:     event.RequestContext.HTTP.Method,
		URL:        u,
		Header:     header,
		Host:       event.RequestContext.DomainName,
		Proto:      event.RequestContext.HTTP.Protocol,
		RemoteAddr: event.RequestContext.HTTP.SourceIP,
	}, event.Body, event.IsBase64Encoded)
}

func requestHeaders(single map[string]string, multi map[string][]string) (http.Header, error) {
	header := make(http.Header)
	for _, key := range slices.Sorted(maps.Keys(multi)) {
		if !validToken(key) {
			return nil, invocationError("request", ErrInvalidEvent, "invalid header name")
		}
		name := http.CanonicalHeaderKey(key)
		for _, value := range multi[key] {
			if !validHeaderValue(value) {
				return nil, invocationError("request", ErrInvalidEvent, "invalid header value")
			}
			header[name] = append(header[name], value)
		}
	}
	// Keep the original multivalue lists for cross-map deduplication. Repeated
	// single values under differently cased keys must not deduplicate each other.
	multiValues := header.Clone()
	for _, key := range slices.Sorted(maps.Keys(single)) {
		if !validToken(key) {
			return nil, invocationError("request", ErrInvalidEvent, "invalid header name")
		}
		value := single[key]
		if !validHeaderValue(value) {
			return nil, invocationError("request", ErrInvalidEvent, "invalid header value")
		}
		name := http.CanonicalHeaderKey(key)
		if !slices.Contains(multiValues[name], value) {
			header[name] = append(header[name], value)
		}
	}
	return header, nil
}

func newRequest(ctx context.Context, r *http.Request, body string, binary bool) (*http.Request, error) {
	if ctx == nil {
		return nil, invocationError("validate", ErrInvalidInvocation, "nil invocation context")
	}
	if !validToken(r.Method) {
		return nil, invocationError("request", ErrInvalidEvent, "invalid HTTP method")
	}
	host := r.Host
	header := r.Header
	if hosts, present := header["Host"]; present {
		if len(hosts) != 1 || hosts[0] == "" {
			return nil, invocationError("request", ErrInvalidEvent, "invalid Host header")
		}
		host = hosts[0]
	}
	if host != "" && !validAuthority(host) {
		return nil, invocationError("request", ErrInvalidEvent, "invalid request host")
	}
	delete(header, "Host")
	if r.Proto == "" {
		r.Proto = "HTTP/1.1"
	}
	major, minor, ok := http.ParseHTTPVersion(r.Proto)
	if !ok {
		return nil, invocationError("request", ErrInvalidEvent, "invalid HTTP protocol")
	}
	var data []byte
	if binary {
		var err error
		data, err = base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, invocationError("request", ErrInvalidEvent, "invalid base64 request body")
		}
	} else {
		data = []byte(body)
	}
	if _, present := header["Content-Length"]; present {
		header.Set("Content-Length", strconv.Itoa(len(data)))
	}
	delete(header, "Transfer-Encoding")
	delete(header, "Trailer")
	r.RequestURI = r.URL.RequestURI()
	r.Host = host
	r.ProtoMajor, r.ProtoMinor = major, minor
	r.Body = http.NoBody
	r.ContentLength = int64(len(data))
	if len(data) != 0 {
		r.Body = io.NopCloser(bytes.NewReader(data))
	}
	return r.WithContext(ctx), nil
}

// validToken implements the shared HTTP method/field-name token grammar.
func validToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return true
}

func validHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' && s[i] != '\t' || s[i] == 0x7f {
			return false
		}
	}
	return true
}

func validAuthority(host string) bool {
	if strings.ContainsAny(host, "/\\?#@, \t\r\n") {
		return false
	}
	u, err := url.Parse("http://" + host)
	if err != nil || u.Host != host || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if strings.HasPrefix(host, "[") {
		addr, err := netip.ParseAddr(u.Hostname())
		return err == nil && addr.Is6()
	}
	return !strings.ContainsAny(u.Hostname(), ":[]")
}
