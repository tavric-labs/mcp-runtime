package policy

import (
	"fmt"
	"net/http"
	"strings"
)

// Limits for a server's public paths. They bound what an unauthenticated
// route can cost: a handful of prefixes, a body cap, and a time cap.
const (
	// MaxPublicPaths is the most public path prefixes one server may declare.
	MaxPublicPaths = 8
	// MaxPublicPathBodyBytes caps a request body on a public path (4 GiB).
	MaxPublicPathBodyBytes int64 = 4 << 30
	// MaxPublicPathDurationSeconds caps how long one public request may take.
	MaxPublicPathDurationSeconds = 3600
)

// PublicPath opens one narrow, bearer-less route to the upstream MCP server.
//
// It exists for single-use signed upload URLs: the server authenticates a
// request by the secret inside the path, so the gateway cannot demand an OAuth
// token. Only POST to a path under PathPrefix skips authentication and
// authorization; the gateway still bounds the body and the duration, strips
// credentials and identity headers, and audits the request. Every other path,
// including the MCP endpoint, stays on the full bearer and policy pipeline.
type PublicPath struct {
	// PathPrefix is the path as the upstream server sees it (after the gateway
	// strips its public prefix). It must start and end with "/".
	PathPrefix string `json:"path_prefix"`
	// MaxBodyBytes is the largest request body accepted.
	MaxBodyBytes int64 `json:"max_body_bytes"`
	// MaxDurationSeconds bounds the whole request, from first byte to response.
	MaxDurationSeconds int `json:"max_duration_seconds"`
}

// MatchPublicPath returns the public path rule that admits method and path, if
// any. path is the upstream-visible request path. It fails closed: a path that
// is not already in clean form, or that has nothing after the prefix, never
// matches, so "..", "//" and the bare prefix cannot widen a rule.
func MatchPublicPath(doc *Document, method, path string) (PublicPath, bool) {
	if doc == nil || doc.Auth == nil || method != http.MethodPost || !cleanPublicRequestPath(path) {
		return PublicPath{}, false
	}
	for _, rule := range doc.Auth.PublicPaths {
		if strings.HasPrefix(path, rule.PathPrefix) && len(path) > len(rule.PathPrefix) {
			return rule, true
		}
	}
	return PublicPath{}, false
}

// cleanPublicRequestPath reports whether path is absolute with no empty, "."
// or ".." segments.
func cleanPublicRequestPath(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#\\") {
		return false
	}
	segments := strings.Split(path[1:], "/")
	for i, segment := range segments {
		last := i == len(segments)-1
		if segment == "." || segment == ".." || (segment == "" && !last) {
			return false
		}
	}
	return true
}

// ValidatePublicPaths checks the public paths on auth. The operator's admission
// validation and the rendered-policy validator share it.
func ValidatePublicPaths(auth *Auth) error { return validatePublicPaths(auth) }

func validatePublicPaths(auth *Auth) error {
	if len(auth.PublicPaths) == 0 {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(auth.Mode), "header") {
		return fmt.Errorf("policy: public_paths are not supported with header auth")
	}
	if len(auth.PublicPaths) > MaxPublicPaths {
		return fmt.Errorf("policy: auth allows at most %d public_paths", MaxPublicPaths)
	}
	for i, rule := range auth.PublicPaths {
		if err := validatePublicPathPrefix(rule.PathPrefix); err != nil {
			return err
		}
		if rule.MaxBodyBytes <= 0 || rule.MaxBodyBytes > MaxPublicPathBodyBytes {
			return fmt.Errorf("policy: public path %q max_body_bytes must be between 1 and %d", rule.PathPrefix, MaxPublicPathBodyBytes)
		}
		if rule.MaxDurationSeconds <= 0 || rule.MaxDurationSeconds > MaxPublicPathDurationSeconds {
			return fmt.Errorf("policy: public path %q max_duration_seconds must be between 1 and %d", rule.PathPrefix, MaxPublicPathDurationSeconds)
		}
		for _, other := range auth.PublicPaths[:i] {
			switch {
			case other.PathPrefix == rule.PathPrefix:
				return fmt.Errorf("policy: duplicate public path %q", rule.PathPrefix)
			case strings.HasPrefix(other.PathPrefix, rule.PathPrefix) || strings.HasPrefix(rule.PathPrefix, other.PathPrefix):
				return fmt.Errorf("policy: public paths %q and %q overlap", other.PathPrefix, rule.PathPrefix)
			}
		}
	}
	return nil
}

func validatePublicPathPrefix(prefix string) error {
	invalid := func(reason string) error {
		return fmt.Errorf("policy: public path_prefix %q %s", prefix, reason)
	}
	switch {
	case !strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/"):
		return invalid("must start and end with \"/\"")
	case prefix == "/":
		return invalid("cannot be the root")
	case strings.ContainsAny(prefix, "?#%\\ \t\r\n") || strings.ContainsFunc(prefix, func(r rune) bool { return r < 0x21 || r > 0x7e }):
		return invalid("may contain only plain path characters")
	case !cleanPublicRequestPath(strings.TrimSuffix(prefix, "/")):
		return invalid("must not contain empty, \".\" or \"..\" segments")
	}
	return nil
}
