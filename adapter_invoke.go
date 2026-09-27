package edge

import (
	"context"
	"net/http"

	"github.com/aws/aws-lambda-go/events"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

// Invoke adapts a REST proxy, HTTP API 1.0 or HTTP API 2.0 event using JSON v2 directly.
// Register the Adapter object with lambda.Start(adapter), not its Invoke method as a reflected function.
// Transport/identity failures return no usable payload;
// ordinary HTTP error responses remain successful invocations.
// Panics propagate after cleanup.
// The caller must not mutate payload during use.
func (a *Adapter) Invoke(ctx context.Context, payload []byte) ([]byte, error) {
	if err := a.valid(); err != nil {
		return nil, err
	}
	var result []byte
	err := withInvocation(ctx, func(ctx context.Context, inv *invocation) error {
		event, err := decodeEvent(payload)
		if err != nil {
			return err
		}
		response, err := a.buffered(ctx, inv, event, true)
		if err != nil {
			return err
		}
		var projected any
		if event.v1 != nil {
			projected, err = response.v1()
		} else {
			projected, err = response.v2()
		}
		if err != nil {
			return err
		}
		result, err = marshalResponse(projected)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// HandleV1 adapts an already decoded REST/HTTP API 1.0 event without serializing envelopes inside edge.
// It can be registered as lambda.Start(adapter.HandleV1).
// The upstream decoder owns wire validation and numeric fidelity;
// the caller or runtime owns final response serialization and its complete envelope size.
// On failure it returns a zero response.
// Inputs must not change during the call.
func (a *Adapter) HandleV1(ctx context.Context, event events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	if err := a.valid(); err != nil {
		return events.APIGatewayProxyResponse{}, err
	}
	var result events.APIGatewayProxyResponse
	err := withInvocation(ctx, func(ctx context.Context, inv *invocation) error {
		response, err := a.buffered(ctx, inv, decodedEvent{v1: &event}, false)
		if err != nil {
			return err
		}
		result, err = response.v1()
		return err
	})
	if err != nil {
		return events.APIGatewayProxyResponse{}, err
	}
	return result, nil
}

// HandleV2 adapts an already decoded HTTP API 2.0 event.
// Its ownership, error and serialization contract matches HandleV1.
// V2 SDK string claims cannot recover arrays or exact numeric values discarded by an upstream decoder.
func (a *Adapter) HandleV2(ctx context.Context, event events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	if err := a.valid(); err != nil {
		return events.APIGatewayV2HTTPResponse{}, err
	}
	var result events.APIGatewayV2HTTPResponse
	err := withInvocation(ctx, func(ctx context.Context, inv *invocation) error {
		response, err := a.buffered(ctx, inv, decodedEvent{v2: &event}, false)
		if err != nil {
			return err
		}
		result, err = response.v2()
		return err
	})
	if err != nil {
		return events.APIGatewayV2HTTPResponse{}, err
	}
	return result, nil
}

func (a *Adapter) valid() error {
	if a == nil || a.handler == nil {
		return invocationError("validate", ErrInvalidInvocation, "uninitialized adapter")
	}
	return nil
}

func (a *Adapter) buffered(ctx context.Context, inv *invocation, event decodedEvent, raw bool) (*bufferedResponse, error) {
	request, err := prepareRequest(ctx, inv, event, raw, a.config)
	if err != nil {
		return nil, err
	}
	return serveBuffered(a.handler, request, a.config.responseHeaderBudget)
}

// prepareRequest owns the transport body before identity production can fail.
// Both invocation modes serve the same prepared request and cleanup owner.
func prepareRequest(ctx context.Context, inv *invocation, event decodedEvent, raw bool, cfg config) (*http.Request, error) {
	var request *http.Request
	var err error
	if event.v1 != nil {
		request, err = requestV1(ctx, *event.v1)
	} else {
		request, err = requestV2(ctx, *event.v2)
	}
	if err != nil {
		return nil, err
	}
	inv.ownRequest(request)
	if cfg.gatewayIdentity {
		var caller identity.Caller
		if raw {
			caller, err = rawGatewayCaller(event, cfg.identityClaimsBudget)
		} else {
			caller, err = typedGatewayCaller(event, cfg.identityClaimsBudget)
		}
		if err != nil {
			return nil, err
		}
		ctx, err = identity.NewContext(ctx, caller)
		if err != nil {
			return nil, invocationError("identity", ErrIdentity, "caller installation failed", identity.ErrConflict)
		}
		request = request.WithContext(ctx)
		// Track the actual served request for multipart cleanup, retaining the original transport body separately even if the handler replaces it.
		inv.request = request
	}
	return request, nil
}
