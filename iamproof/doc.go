// Package iamproof generates short-lived, audience-bound GetCallerIdentity proofs and verifies them online with regional AWS STS.
// It does not require an API Gateway authorizer.
// It neither authorizes application actions nor installs callers in a context;
// compose those responsibilities in the application.
//
// Send [Token.Value] as the credential in Authorization: EdgeIAM <value>, over HTTPS.
// A proof is reusable within its freshness window and does not sign the application request body, method or action.
// Do not log credentials or signed STS requests.
// Generation uses the caller's AWS credentials;
// verification never signs with the server's credentials.
// Every verification calls STS, without an application retry or result cache.
// Account for STS quotas when sizing traffic.
//
// The version 1 protocol bounds encoded credentials to 8 KiB and STS responses to 64 KiB.
// Proofs expire 60 seconds after their signing time;
// verification permits up to 30 seconds of future clock skew, with no expired-proof grace.
// These are library policies, not AWS credential-size or expiration guarantees.
package iamproof
