package edge_test

import (
	"context"
	"testing"

	"github.com/asteroid-computing/go-lambda-edge/identity"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

func TestSDKTypedClaimsFidelity(t *testing.T) {
	const payload = `{"requestContext":{"authorizer":{"claims":{"counter":9007199254740993,"groups":["staff"]}}}}`
	for _, useNumber := range []bool{false, true} {
		var claims identity.Claims
		handler := lambda.NewHandlerWithOptions(func(ctx context.Context, event events.APIGatewayProxyRequest) (bool, error) {
			var err error
			claims, err = identity.NewClaims(event.RequestContext.Authorizer["claims"].(map[string]any))
			return err == nil, err
		}, lambda.WithUseNumber(useNumber))
		if _, err := handler.Invoke(t.Context(), []byte(payload)); err != nil {
			t.Fatal(err)
		}
		number, present := claims.Lookup("counter")
		text, exact := number.NumberText()
		if !present || exact != useNumber || useNumber && text != "9007199254740993" {
			t.Errorf("UseNumber(%t): present=%t exact=%t text=%q", useNumber, present, exact, text)
		}
		if approximate, ok := number.Float64(); !ok || approximate != float64(9007199254740992) {
			t.Errorf("UseNumber(%t): Float64()=%v, %t", useNumber, approximate, ok)
		}
		groups, _ := claims.Lookup("groups")
		if array, ok := groups.Array(); !ok || len(array) != 1 {
			t.Errorf("UseNumber(%t): typed array lost: %v, %t", useNumber, array, ok)
		}
	}
}
