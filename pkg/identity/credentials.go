package identity

import (
	"fmt"
	"net/http"
	"strings"
)

const MaxCredentialHeaders = 16

// ValidateCredentialHeaderName accepts HTTP token names, including Authorization, and rejects
// hop-by-hop, framing, routing, and trusted identity headers.
func ValidateCredentialHeaderName(name string) error {
	if name == "" {
		return fmt.Errorf("credential header name is empty")
	}
	for _, c := range name {
		if c > 127 || !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return fmt.Errorf("invalid credential header name")
		}
	}
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-mcp-") || strings.HasPrefix(lower, "mcp-") {
		return fmt.Errorf("reserved credential header name %q", name)
	}
	switch lower {
	case "host", "connection", "keep-alive", "proxy-connection", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "content-length", "content-type", "content-encoding", "accept", "cookie", "set-cookie", "forwarded", "x-forwarded-client-cert", "origin", "referer":
		return fmt.Errorf("reserved credential header name %q", name)
	}
	return nil
}

// ValidateCredentialValue accepts any configured credential value. The value is opaque.
// It never includes the credential in its error.
func ValidateCredentialValue(value string) error {
	if strings.TrimSpace(value) == "" || len(value) > 8192 {
		return fmt.Errorf("credential must be nonempty and at most 8192 bytes")
	}
	for _, c := range value {
		if c < 32 || c == 127 {
			return fmt.Errorf("credential contains a control character")
		}
	}
	return nil
}

// ValidateCredentialHeaderNames checks a configured list of credential header names.
func ValidateCredentialHeaderNames(names []string) error {
	if len(names) == 0 || len(names) > MaxCredentialHeaders {
		return fmt.Errorf("credential headers require between 1 and %d names", MaxCredentialHeaders)
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if err := ValidateCredentialHeaderName(name); err != nil {
			return err
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate credential header name %q", name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// NormalizeCredentialPresence accepts any, all, or empty (any).
func NormalizeCredentialPresence(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "any":
		return "any", nil
	case "all":
		return "all", nil
	default:
		return "", fmt.Errorf("credentialPresence must be any or all")
	}
}

// CredentialPolicy owns a validated header contract. Configuration is copied
// on construction and cannot be changed through the caller's original slice.
type CredentialPolicy struct {
	names []string
	all   bool
}

type CredentialFailure string

func (e CredentialFailure) Error() string { return string(e) }

const (
	ErrInvalidCredentialConfig CredentialFailure = "invalid_credential_config"
	ErrEmptyCredential         CredentialFailure = "empty_credential"
	ErrDuplicateCredential     CredentialFailure = "duplicate_credential"
	ErrMissingCredential       CredentialFailure = "missing_credential"
)

func NewCredentialPolicy(names []string, presence string) (CredentialPolicy, error) {
	if err := ValidateCredentialHeaderNames(names); err != nil {
		return CredentialPolicy{}, fmt.Errorf("%w: %v", ErrInvalidCredentialConfig, err)
	}
	mode, err := NormalizeCredentialPresence(presence)
	if err != nil {
		return CredentialPolicy{}, fmt.Errorf("%w: %v", ErrInvalidCredentialConfig, err)
	}
	return CredentialPolicy{names: append([]string(nil), names...), all: mode == CredentialPresenceAll}, nil
}

// Check never returns credential values. The zero policy fails closed.
func (p CredentialPolicy) Check(h http.Header) error {
	if len(p.names) == 0 {
		return ErrInvalidCredentialConfig
	}
	present := 0
	for _, name := range p.names {
		var values []string
		for key, vals := range h {
			if strings.EqualFold(key, name) {
				values = append(values, vals...)
			}
		}
		nonempty := make([]string, 0, len(values))
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return ErrEmptyCredential
			}
			nonempty = append(nonempty, value)
		}
		if len(nonempty) > 1 {
			return ErrDuplicateCredential
		}
		if len(nonempty) == 1 {
			present++
		}
	}
	if p.all && present != len(p.names) || !p.all && present == 0 {
		return ErrMissingCredential
	}
	return nil
}
