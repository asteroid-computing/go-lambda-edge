// Package authz authorizes explicit caller, action and resource facts using immutable rules.
// It does not authenticate, evaluate AWS IAM policies, resolve application grants automatically or write HTTP responses.
//
// Construct rules once and share them across requests.
// Custom checks must be concurrency-safe, honor their context and return false, nil for ordinary denial.
// Every callback error becomes ErrUnavailable.
// All and Any evaluate left to right and stop on an observed error;
// keep mandatory application guards outside alternative permission branches.
//
// Authorize requires a nonanonymous caller even for a permissive custom check.
// Identity constructors validate facts but do not authenticate them: establish the caller through a trusted producer before authorization.
// Complete selection and authorization before executing the same action/resource or committing an HTTP response, including a stream.
package authz
