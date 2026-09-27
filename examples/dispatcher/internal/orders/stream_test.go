package orders_test

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/asteroid-computing/go-lambda-edge"
	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func TestStreamLifecycle(t *testing.T) {
	for _, format := range []string{"stream_raw", "stream_typed"} {
		for _, outcome := range []string{"complete", "early_close", "late_failure"} {
			t.Run(format+"/"+outcome, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					cfg, credentials := providerFixtures(t)
					cfg.Streaming = true
					cfg.Grants = grantFunc(func(_ context.Context, caller identity.Caller, action, target string) (bool, error) {
						return caller.Kind() != identity.KindAnonymous && action == "orders.watch" && target == resource, nil
					})
					continueProduction := make(chan struct{})
					finished := make(chan struct{})
					cfg.Store = &spyStore{events: func(ctx context.Context, target string, yield func(string) error) error {
						defer close(finished)
						if target != resource {
							return errors.New("incorrect execution target")
						}
						if err := yield("ready"); err != nil {
							return err
						}
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-continueProduction:
						}
						if outcome == "late_failure" {
							return errors.New("private database diagnostic")
						}
						return yield("done")
					}}
					headers := http.Header{"Authorization": {credentials["iam"]}, "Action": {"orders.watch"}}
					stream := openStream(t, application(t, cfg), format, resource, headers)
					defer stream.Close()
					reader := bufio.NewReader(stream)
					metadata := readPrefix(t, reader)
					if metadata.Status != 200 || metadata.Headers.Get("Content-Type") != "application/x-ndjson" {
						t.Fatalf("stream metadata = %+v", metadata)
					}
					line, err := reader.ReadString('\n')
					if err != nil {
						t.Fatal(err)
					}
					var record map[string]string
					if err := json.Unmarshal([]byte(line), &record); err != nil {
						t.Fatal(err)
					}
					if record["resource"] != resource || record["state"] != "ready" {
						t.Errorf("first record = %v", record)
					}
					// The first record arrived before production was allowed to finish.
					synctest.Wait()
					select {
					case <-finished:
						t.Fatal("producer finished before release")
					default:
					}
					if outcome == "early_close" {
						_ = stream.Close()
						select {
						case <-finished:
						default:
							t.Error("Close returned before producer stopped")
						}
						return
					}
					close(continueProduction)
					synctest.Wait()
					if outcome == "complete" {
						// No consumer has read the second record.
						// The unbuffered bridge must hold the writer here instead of collecting the body.
						select {
						case <-finished:
							t.Fatal("slow reader did not apply backpressure")
						default:
						}
					}
					rest, err := io.ReadAll(reader)
					if outcome == "late_failure" {
						if !errors.Is(err, edge.ErrStream) || len(rest) != 0 || strings.Contains(err.Error(), "database") {
							t.Errorf("late failure = %q, %v; want sanitized terminal error", rest, err)
						}
					} else {
						if err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(rest, &record); err != nil {
							t.Fatal(err)
						}
						if record["state"] != "done" {
							t.Errorf("last record = %v", record)
						}
					}
					select {
					case <-finished:
					default:
						t.Error("terminal read returned before producer stopped")
					}
				})
			})
		}
	}
}

type streamMetadata struct {
	Status  int         `json:"statusCode"`
	Headers http.Header `json:"multiValueHeaders"`
}

func readPrefix(t *testing.T, reader *bufio.Reader) streamMetadata {
	t.Helper()
	var prefix []byte
	zeros := 0
	for len(prefix) < 16000 {
		b, err := reader.ReadByte()
		if err != nil {
			t.Fatal(err)
		}
		prefix = append(prefix, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
		if zeros == 8 {
			var metadata streamMetadata
			if err := json.Unmarshal(prefix[:len(prefix)-8], &metadata); err != nil {
				t.Fatal(err)
			}
			return metadata
		}
	}
	t.Fatal("stream metadata exceeds prefix limit")
	return streamMetadata{}
}

func TestWatchAuthorizationBeforeStreaming(t *testing.T) {
	cfg, credentials := providerFixtures(t)
	cfg.Streaming = true
	for _, kind := range []string{"bearer", "iam"} {
		for _, format := range []string{"stream_raw", "stream_typed"} {
			t.Run(kind+"/"+format, func(t *testing.T) {
				cfg := cfg
				store := new(spyStore)
				cfg.Store = store
				cfg.Grants = grantFunc(func(context.Context, identity.Caller, string, string) (bool, error) { return false, nil })
				headers := http.Header{"Authorization": {credentials[kind]}, "Action": {"orders.watch"}, "Origin": {origin}}
				out := serve(t, application(t, cfg), format, resource, headers)
				if out.status != 403 || out.headers.Get("Content-Type") != "application/json" || out.headers.Get("Access-Control-Allow-Origin") != origin || store.reads.Load() != 0 {
					t.Errorf("denied watch = %+v, executions=%d", out, store.reads.Load())
				}
			})
		}
	}
}
