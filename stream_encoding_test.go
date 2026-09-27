package edge

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestStreamPrefixMetadata(t *testing.T) {
	h := mustSnapshot(t, http.Header{
		"Custom":     {"first", "second"},
		"Set-Cookie": {"a=1", "b=2"},
		"Quoted":     {"a\t\"b"},
		"Suppressed": nil,
	}, defaultResponseHeaderBudget)
	prefix, err := encodeStreamPrefix(201, h)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(prefix[len(prefix)-8:], make([]byte, 8)) || bytes.ContainsRune(prefix[:len(prefix)-8], 0) {
		t.Fatalf("stream prefix %q has invalid delimiter placement", prefix)
	}
	var metadata struct {
		StatusCode        int                 `json:"statusCode"`
		MultiValueHeaders map[string][]string `json:"multiValueHeaders"`
		Headers           map[string]string   `json:"headers"`
		Cookies           []string            `json:"cookies"`
	}
	if err := json.Unmarshal(prefix[:len(prefix)-8], &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.StatusCode != 201 || metadata.Headers != nil || metadata.Cookies != nil {
		t.Errorf("metadata = %+v; want status 201 and only multivalue headers", metadata)
	}
	for _, name := range []string{"Custom", "Set-Cookie", "Quoted"} {
		if !slices.Equal(metadata.MultiValueHeaders[name], h.fields[name]) {
			t.Errorf("stream field %s = %v; want %v", name, metadata.MultiValueHeaders[name], h.fields[name])
		}
	}
	if _, present := metadata.MultiValueHeaders["Suppressed"]; present {
		t.Error("stream metadata emitted suppression marker")
	}
	if cap(prefix) != len(prefix) {
		t.Errorf("prefix retains capacity %d for length %d", cap(prefix), len(prefix))
	}
}

func TestStreamPrefixExactLimit(t *testing.T) {
	base, err := encodeStreamPrefix(200, mustSnapshot(t, http.Header{"A": {""}}, defaultResponseHeaderBudget))
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range []int{-1, 0, 1} {
		value := strings.Repeat("x", maxStreamPrefixBytes-len(base)+delta)
		h := mustSnapshot(t, http.Header{"A": {value}}, defaultResponseHeaderBudget)
		prefix, err := encodeStreamPrefix(200, h)
		if delta > 0 {
			if prefix != nil || !errors.Is(err, errStreamPrefixTooLarge) {
				t.Errorf("over-limit prefix = %d bytes, %v; want nil and size error", len(prefix), err)
			}
			continue
		}
		if err != nil || len(prefix) != maxStreamPrefixBytes+delta {
			t.Errorf("prefix with delta %d = %d bytes, %v; want %d bytes", delta, len(prefix), err, maxStreamPrefixBytes+delta)
		}
	}
	for _, value := range []string{strings.Repeat(`"`, 8000), "a" + strings.Repeat("\t", 8000) + "b"} {
		h := mustSnapshot(t, http.Header{"A": {value}}, defaultResponseHeaderBudget)
		prefix, err := encodeStreamPrefix(200, h)
		if prefix != nil || !errors.Is(err, errStreamPrefixTooLarge) {
			t.Errorf("escaping prefix = %d bytes, %v; want encoded-size failure", len(prefix), err)
		}
	}
	for _, status := range []int{0, 101, 199, 600, 999} {
		prefix, err := encodeStreamPrefix(status, mustSnapshot(t, nil, defaultResponseHeaderBudget))
		if prefix != nil || err == nil {
			t.Errorf("prefix status %d = %q, %v; want rejection", status, prefix, err)
		}
	}
}

func TestStreamPrefixBridgePublication(t *testing.T) {
	for _, large := range []bool{false, true} {
		value := "value"
		if large {
			value = strings.Repeat("x", maxStreamPrefixBytes)
		}
		h := mustSnapshot(t, http.Header{"Custom": {value}}, defaultResponseHeaderBudget)
		wroteBody := false
		s, err := startStream(t.Context(), prepareEmptyStream, func(ctx context.Context, out *streamOutput) error {
			prefix, err := encodeStreamPrefix(200, h)
			if err != nil {
				return err
			}
			if err := out.publish(prefix); err != nil {
				return err
			}
			_, err = io.WriteString(out.body, "body")
			wroteBody = true
			return err
		}, nil)
		if large {
			if s != nil || !errors.Is(err, errStreamPrefixTooLarge) || wroteBody {
				t.Errorf("oversize handoff = %v, %v, body=%t; want no publication", s, err, wroteBody)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(s)
		closeErr := s.Close()
		if err != nil || closeErr != nil || !wroteBody || !bytes.HasSuffix(data, append(make([]byte, 8), []byte("body")...)) {
			t.Errorf("framed stream = %q, %v; Close=%v, wroteBody=%t", data, err, closeErr, wroteBody)
		}
	}
}
