# go-lambda-edge

A Go 1.27 module being built to serve AWS API Gateway requests through ordinary
`net/http` handlers in AWS Lambda, with companion identity, authentication, and
authorization packages.

The root package is `edge`. This project is being designed and built from scratch.
The constructor, options, and private event decoder are implemented. The decoder
uses AWS Lambda Go v1.55.0 event structs and encoding/json/v2 directly, retaining
authorizer JSON separately. Invocation, HTTP translation, and identity extraction
are not implemented yet; the adapter cannot yet be registered as a Lambda handler.

- [Implementation plan](docs/plan.md)
- [Design decisions](docs/decisions/README.md)
- [Repository working agreement](AGENTS.md)
