package edge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func prepareEmptyStream(context.Context, *invocation) error { return nil }

func TestStreamHandoffBackpressureAndCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		body := &trackedBody{Reader: http.NoBody}
		var scope context.Context
		prepare := func(ctx context.Context, inv *invocation) error {
			scope = ctx
			inv.ownRequest(&http.Request{Body: body})
			return nil
		}
		prefix := []byte("prefix")
		published := make(chan struct{})
		written := make(chan struct{})
		payload := bytes.Repeat([]byte("x"), 128*1024)
		s, err := startStream(t.Context(), prepare, func(ctx context.Context, out *streamOutput) error {
			if err := out.publish(prefix); err != nil {
				return err
			}
			clear(prefix) // The consumer must own its copy before publish returns.
			close(published)
			_, err := out.body.Write(payload)
			close(written)
			return err
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		<-published
		synctest.Wait()
		select {
		case <-written:
			t.Error("producer completed body write without a consumer")
		default:
		}
		if body.closed || scope.Err() != nil {
			t.Errorf("after handoff: body closed=%v, context=%v", body.closed, scope.Err())
		}
		first := make([]byte, len(prefix))
		if _, err := io.ReadFull(s, first); err != nil || string(first) != "prefix" {
			t.Fatalf("initial Read = %q, %v; want owned prefix", first, err)
		}
		// A small read cannot drain a large write into hidden adapter buffering.
		if _, err := io.ReadFull(s, make([]byte, 7)); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case <-written:
			t.Error("producer completed a large write after only seven body bytes were read")
		default:
		}
		rest, err := io.ReadAll(s)
		if err != nil || !bytes.Equal(rest, payload[7:]) {
			t.Errorf("remaining body: bytes=%d err=%v, want %d bytes", len(rest), err, len(payload)-7)
		}
		if !body.closed || scope.Err() != context.Canceled {
			t.Errorf("after EOF: body closed=%v, context=%v", body.closed, scope.Err())
		}
		if err := s.Close(); err != nil {
			t.Errorf("Close after EOF = %v, want nil", err)
		}
		if s.ContentType() != "application/vnd.awslambda.http-integration-response" {
			t.Errorf("ContentType = %q", s.ContentType())
		}
		if data, err := s.MarshalJSON(); err == nil || len(data) != 0 {
			t.Errorf("MarshalJSON = %q, %v; want rejection", data, err)
		}
	})
}

type streamCloseBody struct {
	io.Reader
	close func() error
}

func (b *streamCloseBody) Close() error { return b.close() }

func TestStreamCloseCancelsUnblocksAndJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		allowCleanup := make(chan struct{})
		var scope context.Context
		cleaned := false
		reports := 0
		body := &streamCloseBody{Reader: http.NoBody, close: func() error {
			<-allowCleanup
			cleaned = true
			return nil
		}}
		prepare := func(ctx context.Context, inv *invocation) error {
			scope = ctx
			inv.ownRequest(&http.Request{Body: body})
			return nil
		}
		s, err := startStream(t.Context(), prepare, func(ctx context.Context, out *streamOutput) error {
			if err := out.publish([]byte("prefix")); err != nil {
				return err
			}
			_, err := out.body.Write([]byte("blocked body"))
			return err
		}, func(ctx context.Context, err error) {
			reports++
			if !cleaned || ctx.Err() != context.Canceled || !errors.Is(err, context.Canceled) {
				t.Errorf("report: cleaned=%v context=%v err=%v", cleaned, ctx.Err(), err)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		closed := make(chan error, 1)
		go func() { closed <- s.Close() }()
		synctest.Wait()
		if scope.Err() != context.Canceled {
			t.Errorf("Close did not cancel request: %v", scope.Err())
		}
		select {
		case err := <-closed:
			t.Fatalf("Close returned before cleanup: %v", err)
		default:
		}
		read := make(chan error, 1)
		go func() {
			_, err := io.ReadAll(s)
			read <- err
		}()
		synctest.Wait()
		select {
		case err := <-read:
			t.Fatalf("Read exposed terminal error before cleanup: %v", err)
		default:
		}
		close(allowCleanup)
		if err := <-closed; !errors.Is(err, context.Canceled) {
			t.Errorf("Close = %v, want canceled", err)
		}
		if err := <-read; !errors.Is(err, context.Canceled) {
			t.Errorf("Read = %v, want canceled", err)
		}
		if err := s.Close(); !errors.Is(err, context.Canceled) || reports != 1 {
			t.Errorf("repeated Close = %v, reports=%d; want canceled and one report", err, reports)
		}
	})
}

func TestStreamParentCancellationWithoutConsumer(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancel := context.WithTimeout(t.Context(), time.Hour)
				defer cancel()
				body := &trackedBody{Reader: http.NoBody}
				prepare := func(ctx context.Context, inv *invocation) error {
					inv.ownRequest(&http.Request{Body: body})
					return nil
				}
				s, err := startStream(parent, prepare, func(ctx context.Context, out *streamOutput) error {
					if err := out.publish([]byte("prefix")); err != nil {
						return err
					}
					_, err := out.body.Write([]byte("blocked"))
					return err
				}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if deadline {
					time.Sleep(time.Hour) // Virtual time inside the synctest bubble.
				} else {
					cancel()
				}
				synctest.Wait()
				if !body.closed {
					t.Error("parent cancellation did not release blocked producer and clean request")
				}
				if _, err := io.ReadAll(s); !errors.Is(err, parent.Err()) {
					t.Errorf("Read after parent cancellation = %v, want %v", err, parent.Err())
				}
			})
		})
	}
}

func TestStreamCloseReleasesBlockedRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, err := startStream(t.Context(), prepareEmptyStream, func(ctx context.Context, out *streamOutput) error {
			if err := out.publish(nil); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		read := make(chan error, 1)
		go func() {
			_, err := s.Read(make([]byte, 1))
			read <- err
		}()
		synctest.Wait()
		select {
		case err := <-read:
			t.Fatalf("Read returned before data or cancellation: %v", err)
		default:
		}
		closed := make(chan error, 2)
		for range 2 {
			go func() { closed <- s.Close() }()
		}
		for range 2 {
			if err := <-closed; !errors.Is(err, context.Canceled) {
				t.Errorf("concurrent Close = %v, want canceled", err)
			}
		}
		if err := <-read; !errors.Is(err, context.Canceled) {
			t.Errorf("blocked Read = %v, want canceled", err)
		}
	})
}

func TestStreamEOFWaitsForCleanupAndKeepsScopeUntilConsumption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		allowCleanup := make(chan struct{})
		var scope context.Context
		cleaned := false
		prepare := func(ctx context.Context, inv *invocation) error {
			scope = ctx
			inv.ownRequest(&http.Request{Body: &streamCloseBody{Reader: http.NoBody, close: func() error {
				<-allowCleanup
				cleaned = true
				return nil
			}}})
			return nil
		}
		s, err := startStream(t.Context(), prepare, func(ctx context.Context, out *streamOutput) error {
			return out.publish([]byte("prefix"))
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if _, err := io.ReadFull(s, make([]byte, len("prefix"))); err != nil {
			t.Fatal(err)
		}
		read := make(chan error, 1)
		go func() {
			_, err := s.Read(make([]byte, 1))
			read <- err
		}()
		synctest.Wait()
		select {
		case err := <-read:
			t.Fatalf("terminal Read completed before cleanup: %v", err)
		default:
		}
		if scope.Err() != nil {
			t.Errorf("scope canceled before completion: %v", scope.Err())
		}
		close(allowCleanup)
		if err := <-read; err != io.EOF || !cleaned || scope.Err() != context.Canceled {
			t.Errorf("terminal Read = %v, cleaned=%v context=%v", err, cleaned, scope.Err())
		}
	})
}

func TestStreamFailuresBeforeHandoff(t *testing.T) {
	failure := errors.New("edge: preparation or production failed")
	panicValue := new("private application panic")
	for _, stage := range []string{"prepare_error", "prepare_panic", "producer_error", "producer_panic", "no_publication", "canceled"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancel := context.WithCancel(t.Context())
				defer cancel()
				body := &trackedBody{Reader: http.NoBody}
				var scope context.Context
				prepare := func(ctx context.Context, inv *invocation) error {
					scope = ctx
					inv.ownRequest(&http.Request{Body: body})
					if stage == "prepare_error" {
						return failure
					}
					if stage == "prepare_panic" {
						panic(panicValue)
					}
					return nil
				}
				var recovered any
				var result *responseStream
				var err error
				reports := 0
				func() {
					defer func() { recovered = recover() }()
					result, err = startStream(parent, prepare, func(ctx context.Context, out *streamOutput) error {
						switch stage {
						case "producer_error":
							return failure
						case "producer_panic":
							panic(panicValue)
						case "canceled":
							cancel()
							<-ctx.Done()
							return ctx.Err()
						}
						return nil
					}, func(context.Context, error) { reports++ })
				}()
				if result != nil || !body.closed || scope.Err() != context.Canceled || reports != 0 {
					t.Errorf("failed entry: result=%v closed=%v context=%v reports=%d", result, body.closed, scope.Err(), reports)
				}
				if strings.HasSuffix(stage, "panic") {
					if recovered != panicValue {
						t.Errorf("panic = %v, want original value", recovered)
					}
					return
				}
				want := failure
				if stage == "no_publication" {
					want = errStreamUnpublished
				} else if stage == "canceled" {
					want = context.Canceled
				}
				if recovered != nil || !errors.Is(err, want) {
					t.Errorf("entry error=%v panic=%v, want %v and no panic", err, recovered, want)
				}
			})
		})
	}
}

func TestStreamLateFailurePreservesBytesAndReportsAfterCleanup(t *testing.T) {
	for _, mode := range []string{"error", "panic", "cleanup_error", "cancel_and_error", "reporter_panic"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancel := context.WithCancel(t.Context())
				defer cancel()
				cleaned := false
				body := &streamCloseBody{Reader: http.NoBody, close: func() error {
					cleaned = true
					if mode == "cleanup_error" {
						return errors.New("private cleanup path")
					}
					return nil
				}}
				prepare := func(ctx context.Context, inv *invocation) error {
					inv.ownRequest(&http.Request{Body: body})
					return nil
				}
				reports := 0
				var reported error
				s, err := startStream(parent, prepare, func(ctx context.Context, out *streamOutput) error {
					if err := out.publish([]byte("prefix")); err != nil {
						return err
					}
					if _, err := out.body.Write([]byte("final bytes")); err != nil {
						return err
					}
					if mode == "panic" {
						panic("private panic data")
					}
					if mode == "cancel_and_error" {
						cancel()
					}
					if mode == "cleanup_error" {
						return nil
					}
					return http.ErrContentLength
				}, func(ctx context.Context, err error) {
					reports++
					reported = err
					if !cleaned {
						t.Error("reported before request cleanup")
					}
					if mode == "reporter_panic" {
						panic("private reporter data")
					}
				})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				data, err := io.ReadAll(s)
				if string(data) != "prefixfinal bytes" || err == nil || reports != 1 || reported == nil {
					t.Fatalf("stream = %q, %v; reports=%d (%v)", data, err, reports, reported)
				}
				if strings.Contains(err.Error(), "private") || strings.Contains(reported.Error(), "private") {
					t.Errorf("terminal error exposed private details: %v; reported %v", err, reported)
				}
				if mode == "panic" && !errors.Is(err, errStreamPanic) {
					t.Errorf("late panic = %v, want stream panic error", err)
				}
				if mode != "panic" && mode != "cleanup_error" && !errors.Is(err, http.ErrContentLength) {
					t.Errorf("terminal error lost primary fault: %v", err)
				}
				if mode == "cancel_and_error" && errors.Is(err, context.Canceled) {
					t.Errorf("cancellation replaced primary fault: %v", err)
				}
				if mode == "reporter_panic" && !errors.Is(err, errStreamReporter) {
					t.Errorf("reporter panic was not retained: %v", err)
				}
			})
		})
	}
}

type finalBytesReader struct {
	err error
}

func (r *finalBytesReader) Read(p []byte) (int, error) {
	return copy(p, "last"), r.err
}

func TestStreamDefersErrorAccompanyingBytes(t *testing.T) {
	for _, terminal := range []error{io.EOF, http.ErrContentLength} {
		// Inject a legal Reader result that the current SDK mishandles. This
		// protects the runtime-facing Read contract independently of io.Pipe.
		done := make(chan struct{})
		close(done)
		s := &responseStream{
			source: &finalBytesReader{err: terminal},
			done:   done,
			closed: make(chan struct{}),
			cancel: func() {},
		}
		if terminal != io.EOF {
			s.err = terminal
		}
		p := make([]byte, 8)
		if n, err := s.Read(p); n != 4 || err != nil || string(p[:n]) != "last" {
			t.Fatalf("Read bytes+%v = %q, %v; want last, nil", terminal, p[:n], err)
		}
		if n, err := s.Read(nil); n != 0 || err != nil {
			t.Errorf("empty Read = %d, %v; want 0, nil", n, err)
		}
		if n, err := s.Read(p); n != 0 || !errors.Is(err, terminal) {
			t.Errorf("terminal Read = %d, %v; want 0, %v", n, err, terminal)
		}
	}
}
