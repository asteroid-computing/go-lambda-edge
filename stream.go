package edge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
)

var (
	errStreamPanic       = invocationError(OperationStream, ErrStream, "stream producer panicked")
	errStreamReporter    = invocationError(OperationStream, ErrStream, "stream error reporter panicked")
	errStreamUnpublished = invocationError(OperationStream, ErrStream, "stream producer ended without publishing a response")
)

// streamOutput belongs to the producer.
// publish accepts an already validated, bounded wire prefix and completes the handoff before body writes may block.
// Its caller must supply sanitized errors and publish exactly once.
type streamOutput struct {
	publish func([]byte) error
	body    io.Writer
}

// responseStream separates producer lifetime from the entry point's stack.
// mu protects handoff, interruption, and terminal commitment.
// done publishes the final error/panic after cleanup and reporting.
// readMu only serializes readers;
// interruption and Close must never acquire it while a Read can be blocked.
type responseStream struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	offer  chan []byte
	accept chan struct{}
	done   chan struct{}
	closed chan struct{}
	cancel context.CancelFunc

	mu         sync.Mutex
	handedOff  bool
	settled    bool
	stopErr    error
	err        error
	panicValue any

	readMu    sync.Mutex
	source    io.Reader
	pending   error
	closeOnce sync.Once
}

// startStream performs preparation synchronously, then transfers request cleanup to one producer.
// Until publication, errors return directly and producer panics are rethrown here.
// The caller must consume and Close a successful result.
// prepare and produce are private transport callbacks, not application hooks.
func startStream(parent context.Context, prepare func(context.Context, *invocation) error, produce func(context.Context, *streamOutput) error, report func(context.Context, error)) (result *responseStream, err error) {
	ctx, cancel, err := invocationContext(parent)
	if err != nil {
		return nil, err
	}
	var inv invocation
	owned := true
	defer func() {
		if owned {
			defer cancel()
			err = inv.cleanup(err)
		}
	}()
	if err := prepare(ctx, &inv); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reader, writer := io.Pipe()
	s := &responseStream{
		reader: reader,
		writer: writer,
		offer:  make(chan []byte),
		accept: make(chan struct{}),
		done:   make(chan struct{}),
		closed: make(chan struct{}),
		cancel: cancel,
	}
	watcherDone := make(chan struct{})
	stopWatcher := context.AfterFunc(ctx, func() {
		defer close(watcherDone)
		s.interrupt(ctx.Err())
	})
	output := &streamOutput{
		body: writer,
		publish: func(prefix []byte) error {
			select {
			case s.offer <- bytes.Clone(prefix):
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-s.accept:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	owned = false
	go func() {
		var producerErr error
		defer func() {
			panicValue := recover()
			if panicValue != nil {
				producerErr = errors.Join(errStreamPanic, producerErr)
			}
			// stop does not join an already running AfterFunc callback.
			if !stopWatcher() {
				<-watcherDone
			}
			s.finish(ctx, producerErr, panicValue, report)
		}()
		defer func() { producerErr = inv.cleanup(producerErr) }()
		producerErr = produce(ctx, output)
	}()

	select {
	case prefix := <-s.offer:
		s.mu.Lock()
		if !s.settled && s.stopErr == nil && ctx.Err() == nil {
			// This acknowledgement is the error boundary.
			// The producer cannot finish publication or write until the stream has an owner.
			s.handedOff = true
			s.source = io.MultiReader(bytes.NewReader(prefix), reader)
			close(s.accept)
			s.mu.Unlock()
			return s, nil
		}
		s.mu.Unlock()
	case <-s.done:
	case <-ctx.Done():
	}
	s.interrupt(ctx.Err())
	<-s.done
	cancel()
	if s.panicValue != nil {
		panic(s.panicValue)
	}
	return nil, s.err
}

// interrupt never joins the producer: it is also called by the cancellation watcher that the producer itself must join.
// Closing the reader unblocks both directions of the pipe, including writes when no consumer is reading.
func (s *responseStream) interrupt(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled || s.stopErr != nil {
		return
	}
	if err == nil {
		err = context.Canceled
	}
	s.stopErr = err
	s.cancel()
	_ = s.reader.CloseWithError(err)
}

func (s *responseStream) finish(ctx context.Context, err error, panicValue any, report func(context.Context, error)) {
	s.mu.Lock()
	s.settled = true
	// A pipe-close error is only a consequence of interruption.
	// Other producer and cleanup faults retain precedence over concurrent cancellation.
	if err == nil || err == io.ErrClosedPipe {
		if s.stopErr != nil {
			err = s.stopErr
		} else if ctx.Err() != nil {
			err = ctx.Err()
		}
	}
	if !s.handedOff {
		s.panicValue = panicValue
		if err == nil {
			err = errStreamUnpublished
		}
	}
	reportFailure := s.handedOff && err != nil && report != nil
	s.mu.Unlock()
	if reportFailure {
		err = reportStreamError(ctx, report, err)
	}
	s.err = err
	_ = s.writer.CloseWithError(err)
	close(s.done)
}

func reportStreamError(ctx context.Context, report func(context.Context, error), err error) (result error) {
	result = err
	defer func() {
		if recover() != nil {
			result = errors.Join(err, errStreamReporter)
		}
	}()
	report(ctx, err)
	return result
}

func (s *responseStream) Read(p []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	select {
	case <-s.closed:
		<-s.done
		if s.err != nil {
			return 0, s.err
		}
		return 0, io.ErrClosedPipe
	default:
	}
	if s.pending != nil {
		return 0, s.pending
	}
	n, err := s.source.Read(p)
	if err != nil {
		// Interruption can close the pipe before cleanup finishes.
		// Never expose terminal status until producer cleanup and reporting have completed.
		<-s.done
		s.cancel()
		if s.err != nil {
			err = s.err
		}
		if n > 0 {
			// aws-lambda-go v1.55.0 discards n on a non-EOF reader error.
			// Retain the terminal error for the next nonempty Read instead.
			s.pending = err
			return n, nil
		}
	}
	return n, err
}

func (s *responseStream) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.cancel()
		s.interrupt(context.Canceled)
		<-s.done
		_ = s.reader.Close()
	})
	return s.err
}

func (*responseStream) ContentType() string {
	return "application/vnd.awslambda.http-integration-response"
}

// MarshalJSON deliberately declines serialization so the SDK selects its io.Reader response path.
// Edge's framing codec uses JSON v2 separately.
func (*responseStream) MarshalJSON() ([]byte, error) {
	return nil, invocationError(OperationEncode, ErrStream, "streaming response cannot be marshaled as JSON")
}
