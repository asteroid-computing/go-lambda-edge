// Package authn selects one configured Bearer or EdgeIAM verifier from an HTTP
// Authorization header and transports the verified caller to protected handlers.
// NewCognitoVerifier provides local RS256 Cognito access-token verification with
// direct JSON v2 and bounded JWKS caching. It does not check token revocation,
// authorize actions, or infer trust from a gateway, a TLS field, an action header
// or forwarded headers.
//
// Use New to configure explicit verifier functions. Authenticate supports custom
// HTTP pipelines without writing responses or installing context. Handler adds
// required-authentication middleware. Public routes omit that middleware; CORS
// and preflight policy belong to the gateway or outer middleware.
//
// Verifiers must authenticate credentials before returning a caller and classify
// rejection with ErrInvalidCredentials. Adapt iamproof.ErrInvalidProof explicitly
// when wiring an IAM proof verifier; this package does not import AWS packages.
// Never retry another verifier after rejection. Authenticate and authorize the
// selected action before committing a response or starting a stream.
package authn
