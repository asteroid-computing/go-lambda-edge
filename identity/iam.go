package identity

import (
	"strings"
	"unicode/utf8"
)

// IAMPrincipalType describes the supported request-caller form, not an IAM
// resource-policy Principal expression.
type IAMPrincipalType uint8

// Supported IAM principal forms preserve session identities.
const (
	IAMPrincipalUnknown IAMPrincipalType = iota
	IAMPrincipalRoot
	IAMPrincipalUser
	IAMPrincipalAssumedRole
	IAMPrincipalFederatedUser
)

// IAM is an immutable view of an exact caller ARN and optional principal ID.
// Validation does not establish existence, credential possession or permission.
type IAM struct {
	arn           string
	partition     string
	account       string
	service       string
	principalType IAMPrincipalType
	principalID   string
}

// IAMOption supplies an additional caller fact. Nil and duplicate options fail.
type IAMOption func(*iamConfig) error

type iamConfig struct {
	account        string
	principalID    string
	accountSet     bool
	principalIDSet bool
}

// WithIAMAccountID cross-checks an independently supplied caller account against
// the ARN. Do not pass API Gateway's API-owner requestContext.accountId here.
func WithIAMAccountID(account string) IAMOption {
	return func(c *iamConfig) error {
		if c.accountSet || !validAccount(account) {
			return ErrInvalidCaller
		}
		c.accountSet, c.account = true, account
		return nil
	}
}

// WithIAMPrincipalID preserves a nonempty UTF-8 principal identifier opaquely.
// It is not parsed into an identity or treated as evidence of authentication.
func WithIAMPrincipalID(id string) IAMOption {
	return func(c *iamConfig) error {
		if c.principalIDSet || id == "" || !utf8.ValidString(id) {
			return ErrInvalidCaller
		}
		c.principalIDSet, c.principalID = true, id
		return nil
	}
}

// NewIAM validates and owns an exact root, user, assumed-role session or
// federated-user ARN. Bare role ARNs and policy patterns are not caller forms.
// Literal punctuation in a valid IAM user path is preserved without wildcard
// interpretation. Source must be gateway, custom assertion or verified IAM proof.
// Errors match ErrInvalidCaller and contain no supplied identifiers.
func NewIAM(principalARN string, source Source, opts ...IAMOption) (Caller, error) {
	if source != SourceGatewayAssertion && source != SourceCustomAssertion && source != SourceVerifiedIAMProof {
		return Caller{}, ErrInvalidCaller
	}
	var cfg iamConfig
	for _, opt := range opts {
		if opt == nil {
			return Caller{}, ErrInvalidCaller
		}
		if err := opt(&cfg); err != nil {
			return Caller{}, err
		}
	}
	if len(principalARN) > 2048 {
		return Caller{}, ErrInvalidCaller
	}
	parts := strings.SplitN(principalARN, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || !validPartition(parts[1]) || parts[3] != "" || !validAccount(parts[4]) {
		return Caller{}, ErrInvalidCaller
	}
	if cfg.accountSet && cfg.account != parts[4] {
		return Caller{}, ErrInvalidCaller
	}
	var form IAMPrincipalType
	resource := parts[5]
	switch parts[2] {
	case "iam":
		switch {
		case resource == "root":
			form = IAMPrincipalRoot
		case strings.HasPrefix(resource, "user/") && validUserResource(resource[len("user/"):]):
			form = IAMPrincipalUser
		}
	case "sts":
		switch {
		case strings.HasPrefix(resource, "assumed-role/"):
			role, session, ok := strings.Cut(resource[len("assumed-role/"):], "/")
			if ok && validIAMName(role, 1, 64) && validIAMName(session, 2, 64) {
				form = IAMPrincipalAssumedRole
			}
		case strings.HasPrefix(resource, "federated-user/"):
			if validIAMName(resource[len("federated-user/"):], 2, 32) {
				form = IAMPrincipalFederatedUser
			}
		}
	}
	if form == IAMPrincipalUnknown {
		return Caller{}, ErrInvalidCaller
	}
	return Caller{source: source, iam: &IAM{
		arn: strings.Clone(principalARN), partition: strings.Clone(parts[1]),
		account: strings.Clone(parts[4]), service: strings.Clone(parts[2]),
		principalType: form, principalID: strings.Clone(cfg.principalID),
	}}, nil
}

func validAccount(account string) bool {
	if len(account) != 12 {
		return false
	}
	for i := range len(account) {
		if account[i] < '0' || account[i] > '9' {
			return false
		}
	}
	return true
}

// Validate lexical partition syntax, not an allowlist of today's AWS partitions.
// A producer such as the STS verifier must establish its expected partition.
func validPartition(partition string) bool {
	if partition == "" || partition[0] < 'a' || partition[0] > 'z' {
		return false
	}
	for i := range len(partition) {
		b := partition[i]
		if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-') {
			return false
		}
	}
	return true
}

func validIAMName(name string, minimum, maximum int) bool {
	if len(name) < minimum || len(name) > maximum {
		return false
	}
	for i := range len(name) {
		b := name[i]
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("_+=,.@-", rune(b))) {
			return false
		}
	}
	return true
}

func validUserResource(resource string) bool {
	last := strings.LastIndexByte(resource, '/')
	if !validIAMName(resource[last+1:], 1, 64) {
		return false
	}
	if last == -1 {
		return true // The default IAM path is "/".
	}
	// The ARN's user/ separator contributes the path's leading slash. Follow
	// CreateUser's formal path regex (U+0021..U+007E), not its prose DEL endpoint.
	path := resource[:last+1]
	if len(path)+1 > 512 || len(path) < 2 {
		return false
	}
	for i := range len(path) {
		if path[i] < 0x21 || path[i] > 0x7e {
			return false
		}
	}
	return true
}

// PrincipalARN returns the complete ARN, including any path and session name.
func (i IAM) PrincipalARN() string { return i.arn }

// Partition returns the exact partition component.
func (i IAM) Partition() string { return i.partition }

// AccountID returns the caller's twelve-digit account ID.
func (i IAM) AccountID() string { return i.account }

// Service returns iam or sts, or empty for a zero IAM view.
func (i IAM) Service() string { return i.service }

// PrincipalType returns the validated caller form.
func (i IAM) PrincipalType() IAMPrincipalType { return i.principalType }

// PrincipalID reports whether an opaque nonempty principal ID was supplied.
func (i IAM) PrincipalID() (string, bool) { return i.principalID, i.principalID != "" }

// String returns a diagnostic description without identifiers.
func (i IAM) String() string { return "identity.IAM" }

// GoString returns the same sanitized description as String.
func (i IAM) GoString() string { return i.String() }
