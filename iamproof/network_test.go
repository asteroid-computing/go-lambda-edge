package iamproof_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/asteroid-computing/go-lambda-edge/iamproof"
)

type observedBody struct {
	read   func([]byte) (int, error)
	bytes  int
	closed bool
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.read(p)
	b.bytes += n
	return n, err
}

func (b *observedBody) Close() error {
	b.closed = true
	return nil
}

func TestResponseReadBoundAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want error
	}{
		{"success", successXML(testARN), nil},
		{"exact_limit", successXML(testARN) + strings.Repeat(" ", 65536-len(successXML(testARN))), nil},
		{"oversized", strings.Repeat(" ", 1<<20), iamproof.ErrUnavailable},
		{"malformed", "broken", iamproof.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &observedBody{read: strings.NewReader(tc.data).Read}
			v := verifier(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: body}, nil
			}))
			_, err := v.Verify(t.Context(), generate(t).Value())
			if !errors.Is(err, tc.want) || !body.closed || body.bytes > 65537 {
				t.Fatalf("err=%v, closed=%v, bytes=%d; want %v, true, <=65537", err, body.closed, body.bytes, tc.want)
			}
		})
	}
	body := &observedBody{read: func([]byte) (int, error) { return 0, errors.New("sensitive body error") }}
	v := verifier(t, roundTripFunc(func(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: 200, Body: body}, nil }))
	caller, err := v.Verify(t.Context(), generate(t).Value())
	assertFailure(t, caller, err, iamproof.ErrUnavailable)
	if !body.closed || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("body read failure was not sanitized and closed")
	}
}

func TestResponseBodyDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var body *observedBody
		v := verifier(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body = &observedBody{read: func([]byte) (int, error) {
				<-r.Context().Done()
				return 0, r.Context().Err()
			}}
			return &http.Response{StatusCode: 200, Body: body}, nil
		}), iamproof.WithTimeout(time.Second))
		start := time.Now()
		caller, err := v.Verify(t.Context(), generate(t).Value())
		assertFailure(t, caller, err, iamproof.ErrUnavailable)
		if time.Since(start) != time.Second || !body.closed {
			t.Fatal("network deadline did not cover the response body")
		}
	})
}

func TestGeneratorCallerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, err := iamproof.NewGenerator("eu-west-2", testAudience, aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			<-ctx.Done()
			return aws.Credentials{}, errors.New("sensitive provider context error")
		}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		token, err := g.Generate(ctx)
		if !errors.Is(err, context.DeadlineExceeded) || token.Value() != "" {
			t.Fatalf("canceled generation: %v, %v", token, err)
		}
	})
}

func TestHTTPTransportAndRedirect(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			if r.Method != "GET" || r.URL.Path != "/" || r.URL.Query().Get("X-Amz-Signature") == "" || r.Header.Get("X-Edge-IAM-Audience") != testAudience {
				t.Error("HTTP transport lost signed request fields")
			}
			w.Header().Set("Set-Cookie", "untrusted=must-not-return")
			io.WriteString(w, successXML(testARN))
			return
		}
		if r.Header.Get("Cookie") != "" {
			t.Error("verifier retained STS cookies")
		}
		w.Header().Set("Location", "https://attacker.invalid/stolen")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	local, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport
	v := verifier(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "sts.eu-west-2.amazonaws.com" {
			t.Error("redirect reached the transport")
			return nil, errors.New("unexpected destination")
		}
		// This trusted test transport routes STS to a local TLS fixture only.
		localRequest := req.Clone(req.Context())
		localRequest.URL.Host = local.Host
		return transport.RoundTrip(localRequest)
	}))
	token := generate(t)
	if _, err := v.Verify(t.Context(), token.Value()); err != nil {
		t.Fatal(err)
	}
	caller, err := v.Verify(t.Context(), token.Value())
	assertFailure(t, caller, err, iamproof.ErrUnavailable)
	if calls.Load() != 2 {
		t.Fatalf("HTTP calls=%d; want 2", calls.Load())
	}
}
