package edge_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	edge "github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

var _ lambda.Handler = (*edge.Adapter)(nil)

func TestAdapterSDKRegistration(t *testing.T) {
	a, err := edge.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if !bytes.Equal(body, []byte{0, 255}) || r.URL.Path != "/orders" {
			t.Errorf("request body=%v path=%q", body, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Add("Vary", "Accept")
		w.Header().Add("Vary", "Origin")
		http.SetCookie(w, &http.Cookie{Name: "a", Value: "1"})
		http.SetCookie(w, &http.Cookie{Name: "b", Value: "2"})
		w.WriteHeader(400)
		_, _ = w.Write(body)
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		handler any
		v2      bool
		legacy  bool
	}{
		{name: "raw_rest", handler: a, legacy: true},
		{name: "raw_http1", handler: a},
		{name: "raw_http2", handler: a, v2: true},
		{name: "typed_v1", handler: a.HandleV1},
		{name: "typed_v2", handler: a.HandleV2, v2: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := gatewayFixture(tc.v2, "", "")
			payload = append([]byte(`{"body":"AP8=","isBase64Encoded":true,`), payload[1:]...)
			if tc.legacy {
				payload = bytes.Replace(payload, []byte(`"version":"1.0",`), nil, 1)
			}
			out, err := lambda.NewHandler(tc.handler).Invoke(t.Context(), payload)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				StatusCode        int                 `json:"statusCode"`
				Body              string              `json:"body"`
				Binary            bool                `json:"isBase64Encoded"`
				Cookies           []string            `json:"cookies"`
				MultiValueHeaders map[string][]string `json:"multiValueHeaders"`
			}
			if err := json.Unmarshal(out, &response); err != nil {
				t.Fatalf("SDK did not return an object envelope: %v", err)
			}
			if response.StatusCode != 400 || response.Body != "AP8=" || !response.Binary {
				t.Errorf("application error/binary response = %+v", response)
			}
			cookies := response.Cookies
			if !tc.v2 {
				cookies = response.MultiValueHeaders["Set-Cookie"]
			}
			if len(cookies) != 2 {
				t.Errorf("cookie count = %d, want 2", len(cookies))
			}
		})
	}
}

func TestAdapterSDKPreservesTypedNumberOption(t *testing.T) {
	var exact bool
	a, err := edge.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwt, _ := identity.FromContext(r.Context()).JWT()
		claim, _ := jwt.Claims().Lookup("large")
		text, ok := claim.NumberText()
		exact = ok && text == "9007199254740993"
	}), edge.WithGatewayIdentity(true))
	if err != nil {
		t.Fatal(err)
	}
	payload := gatewayFixture(false, `{"claims":{"iss":"issuer","sub":"subject","large":9007199254740993}}`, "")
	for _, useNumber := range []bool{false, true} {
		_, err := lambda.NewHandlerWithOptions(a.HandleV1, lambda.WithUseNumber(useNumber)).Invoke(t.Context(), payload)
		if err != nil || exact != useNumber {
			t.Errorf("UseNumber=%t: exact=%t err=%v", useNumber, exact, err)
		}
	}
}

func TestAdapterFailuresHaveNoResult(t *testing.T) {
	var ran bool
	a, err := edge.New(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true }))
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range [][]byte{[]byte(`{`), []byte(`{}`), []byte(`{"version":"3.0"}`)} {
		out, err := a.Invoke(t.Context(), payload)
		if err == nil || out != nil || ran {
			t.Errorf("malformed invocation out=%v err=%v ran=%t", out, err, ran)
		}
	}
	for _, invalid := range []*edge.Adapter{nil, {}} {
		if _, err := invalid.Invoke(t.Context(), nil); !errors.Is(err, edge.ErrInvalidInvocation) {
			t.Errorf("uninitialized Invoke = %v", err)
		}
		if out, err := invalid.HandleV1(t.Context(), events.APIGatewayProxyRequest{}); !errors.Is(err, edge.ErrInvalidInvocation) || out.StatusCode != 0 {
			t.Errorf("uninitialized HandleV1 = %v", err)
		}
		if out, err := invalid.HandleV2(t.Context(), events.APIGatewayV2HTTPRequest{}); !errors.Is(err, edge.ErrInvalidInvocation) || out.StatusCode != 0 {
			t.Errorf("uninitialized HandleV2 = %v", err)
		}
	}
	caller, err := identity.NewIAM("arn:aws:iam::123456789012:root", identity.SourceCustomAssertion)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := identity.WithCaller(t.Context(), caller)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := a.Invoke(parent, gatewayFixture(false, "", "")); out != nil || !errors.Is(err, identity.ErrConflict) || ran {
		t.Errorf("inherited caller: err=%v ran=%t", err, ran)
	}
}

func TestAdapterRequestIsolation(t *testing.T) {
	a, err := edge.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwt, ok := identity.FromContext(r.Context()).JWT()
		if !ok {
			_, _ = io.WriteString(w, "anonymous")
			return
		}
		sub, _ := jwt.Subject()
		r.Header.Set("Shared", sub)
		_, _ = io.WriteString(w, sub)
	}), edge.WithGatewayIdentity(true))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		for _, subject := range []string{"Alice", "Bob", "anonymous"} {
			wg.Go(func() {
				auth := `{"claims":{"iss":"issuer","sub":"` + subject + `"}}`
				if subject == "anonymous" {
					auth = "null"
				}
				out, err := a.Invoke(t.Context(), gatewayFixture(false, auth, ""))
				if err != nil {
					t.Error(err)
					return
				}
				var response events.APIGatewayProxyResponse
				if err := json.Unmarshal(out, &response); err != nil || response.Body != subject {
					t.Errorf("isolated subject %q got %q, err=%v", subject, response.Body, err)
				}
			})
		}
	}
	wg.Wait()
}

func TestAdapterMultipartCleanupAndPanic(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("upload", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, "temporary file content")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, panicHandler := range []bool{false, true} {
		var path string
		var served context.Context
		a, err := edge.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			served = r.Context()
			if err := r.ParseMultipartForm(0); err != nil {
				t.Error(err)
				return
			}
			file, err := r.MultipartForm.File["upload"][0].Open()
			if err != nil {
				t.Error(err)
				return
			}
			if disk, ok := file.(*os.File); ok {
				path = disk.Name()
			}
			_ = file.Close()
			if panicHandler {
				panic("expected synthetic panic")
			}
		}), edge.WithGatewayIdentity(true))
		if err != nil {
			t.Fatal(err)
		}
		event := events.APIGatewayProxyRequest{Path: "/", HTTPMethod: "POST", Body: body.String(), Headers: map[string]string{"Content-Type": writer.FormDataContentType()}, RequestContext: events.APIGatewayProxyRequestContext{APIID: "api"}}
		func() {
			defer func() {
				if caught := recover(); (caught != nil) != panicHandler {
					t.Errorf("panic propagated=%t, want %t", caught != nil, panicHandler)
				}
			}()
			if _, err := a.HandleV1(t.Context(), event); err != nil {
				t.Error(err)
			}
		}()
		if path == "" {
			t.Fatal("multipart fixture did not spill to disk")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("multipart temporary file remains: %v", err)
		}
		if !errors.Is(served.Err(), context.Canceled) {
			t.Error("served context remained live after invocation")
		}
	}
}

func TestAdapterCancellationAndStickyResponseFailure(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		a, err := edge.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cancelParent {
				cancel()
				_, _ = io.WriteString(w, "discarded")
				return
			}
			w.WriteHeader(http.StatusEarlyHints)
			_, _ = io.WriteString(w, "discarded")
		}))
		if err != nil {
			t.Fatal(err)
		}
		out, err := a.Invoke(ctx, gatewayFixture(true, "", ""))
		cancel()
		var want error = http.ErrNotSupported
		if cancelParent {
			want = context.Canceled
		}
		if out != nil || !errors.Is(err, want) {
			t.Errorf("discarded response=%s err=%v; want %v", out, err, want)
		}
	}
}

func TestAdapterStandardHandlers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /files/{name}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, r.PathValue("name"), time.Time{}, strings.NewReader("abcdef"))
	})
	a, err := edge.New(mux)
	if err != nil {
		t.Fatal(err)
	}
	for _, v2 := range []bool{false, true} {
		for _, redirect := range []bool{false, true} {
			path := "/files/demo.txt"
			if redirect {
				path = "/files//demo.txt"
			}
			var status int
			var body, location string
			if v2 {
				response, err := a.HandleV2(t.Context(), events.APIGatewayV2HTTPRequest{
					RawPath:        path,
					Headers:        map[string]string{"Range": "bytes=1-3"},
					RequestContext: events.APIGatewayV2HTTPRequestContext{APIID: "api", HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "GET"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				status, body, location = response.StatusCode, response.Body, response.Headers["Location"]
				if response.IsBase64Encoded {
					decoded, err := base64.StdEncoding.DecodeString(body)
					if err != nil {
						t.Fatal(err)
					}
					body = string(decoded)
				}
			} else {
				response, err := a.HandleV1(t.Context(), events.APIGatewayProxyRequest{
					Path:           path,
					HTTPMethod:     "GET",
					Headers:        map[string]string{"Range": "bytes=1-3"},
					RequestContext: events.APIGatewayProxyRequestContext{APIID: "api"},
				})
				if err != nil {
					t.Fatal(err)
				}
				status, body = response.StatusCode, response.Body
				location = http.Header(response.MultiValueHeaders).Get("Location")
			}
			if redirect {
				if status != http.StatusTemporaryRedirect || location != "/files/demo.txt" {
					t.Errorf("v2=%t redirect status=%d location=%q", v2, status, location)
				}
			} else if status != 206 || body != "bcd" {
				t.Errorf("v2=%t ServeContent status=%d body=%q", v2, status, body)
			}
		}
	}
}

func TestAdapterRawEnvelopeLimitAndTypedOwnership(t *testing.T) {
	body := strings.Repeat("x", 6*1024*1024)
	a, err := edge.New(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, body)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := a.Invoke(t.Context(), gatewayFixture(false, "", "")); out != nil || !errors.Is(err, edge.ErrLimitExceeded) {
		t.Fatalf("raw envelope limit: result bytes=%d err=%v", len(out), err)
	}
	response, err := a.HandleV1(t.Context(), events.APIGatewayProxyRequest{Path: "/", HTTPMethod: "GET", RequestContext: events.APIGatewayProxyRequestContext{APIID: "api"}})
	if err != nil || len(response.Body) != len(body) {
		t.Errorf("typed response should leave envelope serialization to caller: bytes=%d err=%v", len(response.Body), err)
	}
}
