package edge

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"io"
	"net/http"

	"github.com/aws/aws-lambda-go/events"
)

// StreamingAdapter serves REST API proxy events through an ordinary HTTP handler and returns a metadata-prefixed body stream.
// Construct it with NewStreaming;
// its zero value is unusable.
// Configuration is immutable;
// the application owns handler concurrency safety.
// HTTP APIs and request streaming are unsupported.
//
// Deployment requires a REST STREAM integration using InvokeWithResponseStream.
// SDK Runtime API compatibility remains unverified against live AWS;
// see the module's streaming guide for the documented SDK/header/connection qualification.
type StreamingAdapter struct {
	handler http.Handler
	config  config
}

// WithStreamErrorReporter reports a sanitized terminal failure once after producer cleanup and before stream completion.
// It applies only to NewStreaming;
// New rejects it.
// A final nil callback is invalid.
// The callback receives the invocation context, possibly canceled, and must return promptly.
// Pre-handoff failures are returned directly and not reported.
// A callback panic is recovered and adds a sanitized ErrStream fault without replacing the original failure.
// It must not read or close the same stream: reporting precedes its completion.
func WithStreamErrorReporter(report func(context.Context, error)) Option {
	return func(c *config) {
		c.streamReporter, c.streamReporterSet = report, true
	}
}

// NewStreaming validates handler and options without starting I/O or goroutines.
// It shares New's handler, header-budget, identity-budget and gateway defaults.
// Streaming never infers Content-Length;
// supply it explicitly when needed.
// Authenticate and authorize before writing or flushing any response.
func NewStreaming(handler http.Handler, opts ...Option) (*StreamingAdapter, error) {
	cfg, err := configureAdapter(handler, opts)
	if err != nil {
		return nil, err
	}
	if cfg.streamReporterSet && cfg.streamReporter == nil {
		return nil, errors.New("edge: nil stream error reporter")
	}
	return &StreamingAdapter{handler: handler, config: cfg}, nil
}

// Handle validates raw REST event JSON with JSON v2 and returns a stream that callers must consume and Close.
// Register lambda.Start(adapter.Handle), not the adapter object or the buffered Handler.Invoke boundary.
// The SDK owns its initial jsontext.Value capture;
// edge owns strict event decoding and prefix encoding.
// Explicit HTTP API version envelopes are rejected.
// Do not mutate event during the call.
// Request resources remain owned until producer cleanup completes.
//
// Pre-handoff failures return a nil stream.
// Early producer panics propagate after cleanup;
// late panics become sanitized terminal errors.
// After handoff, failures cannot replace status or bytes.
// Close cancels and joins the producer;
// a handler ignoring cancellation/write errors can delay Close until invocation timeout.
func (a *StreamingAdapter) Handle(ctx context.Context, event jsontext.Value) (io.ReadCloser, error) {
	if err := a.valid(); err != nil {
		return nil, err
	}
	return a.start(ctx, func() (decodedEvent, error) {
		decoded, err := decodeEvent(event)
		if err != nil {
			return decodedEvent{}, err
		}
		if decoded.version != "" || decoded.v1 == nil {
			return decodedEvent{}, invocationError("validate", ErrUnsupportedEvent, "streaming requires a REST proxy event")
		}
		return decoded, nil
	}, true)
}

// HandleV1 accepts an already typed REST event, avoiding request envelope JSON work inside edge.
// Register lambda.Start(adapter.HandleV1).
// The caller/upstream codec owns wire validation and numeric fidelity.
// The V1 type also represents HTTP API 1.0;
// it cannot prove the deployment is a REST streaming integration.
// Its stream ownership and error contract match Handle.
// Inputs must not change during the call.
// Streaming metadata still requires direct JSON v2 encoding.
func (a *StreamingAdapter) HandleV1(ctx context.Context, event events.APIGatewayProxyRequest) (io.ReadCloser, error) {
	if err := a.valid(); err != nil {
		return nil, err
	}
	return a.start(ctx, func() (decodedEvent, error) { return decodedEvent{v1: &event}, nil }, false)
}

func (a *StreamingAdapter) valid() error {
	if a == nil || a.handler == nil {
		return invocationError("validate", ErrInvalidInvocation, "uninitialized streaming adapter")
	}
	return nil
}

func (a *StreamingAdapter) start(ctx context.Context, decode func() (decodedEvent, error), raw bool) (io.ReadCloser, error) {
	var request *http.Request
	stream, err := startStream(ctx, func(ctx context.Context, inv *invocation) error {
		event, err := decode()
		if err != nil {
			return err
		}
		request, err = prepareRequest(ctx, inv, event, raw, a.config)
		return err
	}, func(ctx context.Context, out *streamOutput) error {
		w := newStreamingWriter(ctx, request.Method, a.config.responseHeaderBudget, out)
		a.handler.ServeHTTP(w, request)
		return w.finish()
	}, a.config.streamReporter)
	if err != nil {
		return nil, err // Do not put a typed nil stream in the returned interface.
	}
	return stream, nil
}
