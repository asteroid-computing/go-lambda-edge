# Decoder fixtures

These are synthetic integration events derived from the documented AWS payload
shapes, with focused edge cases added. They are not captured production requests,
SDK-generated golden files, or copies of Beakley tests.

- `rest.json`: REST proxy shape, decoded path containing delimiters, independent
  single/multivalue maps, and application-invalid JSON left as an opaque body.
- `http-v1.json`: HTTP API payload 1.0 and documented null optional fields.
- `http-v2.json`: HTTP API payload 2.0, escaped path, raw repeated query values,
  cookies, binary body, native authorizer, and a synthetic future field.

Sources:

- [REST proxy integration](https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html)
- [HTTP API payload formats](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)
