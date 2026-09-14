# go-lambda-edge

A planned Go 1.27 module for serving AWS API Gateway requests through ordinary
`net/http` handlers in AWS Lambda, with companion identity, authentication, and
authorization packages.

The root package will be named `edge`. This project is being designed and built
from scratch. No runtime implementation is present yet.

- [Implementation plan](docs/plan.md)
- [Design decisions](docs/decisions/README.md)
- [Repository working agreement](AGENTS.md)
