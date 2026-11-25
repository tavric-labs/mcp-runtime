package policy

import (
	"strings"
)

// IsToolCallMethod returns true if the method is a tool invocation method.
func IsToolCallMethod(method string) bool {
	switch method {
	case "tools/call", "call_tool":
		return true
	default:
		return false
	}
}

// PrimitiveMethod classifies the MCP methods that read resources, prompts and
// skills. targeted is false for list methods, which name no single item.
func PrimitiveMethod(method string) (kind string, targeted, ok bool) {
	switch method {
	case "resources/read", "resources/subscribe", "resources/unsubscribe", "resources/directory/read":
		return PrimitiveResource, true, true
	case "resources/list", "resources/templates/list":
		return PrimitiveResource, false, true
	case "prompts/get":
		return PrimitivePrompt, true, true
	case "prompts/list":
		return PrimitivePrompt, false, true
	case "skills/get":
		return PrimitiveSkill, true, true
	case "skills/list":
		return PrimitiveSkill, false, true
	default:
		return "", false, false
	}
}

// IsPrimitiveMethod reports whether a method reads a resource, prompt or skill.
func IsPrimitiveMethod(method string) bool {
	_, _, ok := PrimitiveMethod(method)
	return ok
}

// FirstNonEmpty returns the first non-empty string from the provided values.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// PolicyServerName returns the server name from a policy document.
func PolicyServerName(policy *Document) string {
	if policy == nil {
		return ""
	}
	return string(policy.Server.Name)
}

// PolicyServerNamespace returns the server namespace from a policy document.
func PolicyServerNamespace(policy *Document) string {
	if policy == nil {
		return ""
	}
	return string(policy.Server.Namespace)
}

// PolicyServerTeamID returns the owning team ID from a policy document.
func PolicyServerTeamID(policy *Document) string {
	if policy == nil {
		return ""
	}
	return string(policy.Server.TeamID)
}

// PolicyServerCluster returns the cluster name from a policy document.
func PolicyServerCluster(policy *Document) string {
	if policy == nil {
		return ""
	}
	return policy.Server.Cluster
}

// PolicyVersion returns the policy version from a policy document.
func PolicyVersion(policy *Document) string {
	if policy == nil || policy.Policy == nil {
		return ""
	}
	return policy.Policy.PolicyVersion
}

// UsesDelegatedHeaders reports upstream-owned credential authentication.
func UsesDelegatedHeaders(policy *Document) bool {
	return policy != nil && policy.Auth != nil && strings.EqualFold(strings.TrimSpace(policy.Auth.Mode), "header")
}

// DelegatedHeaderConfigured reports whether name is an accepted credential header.
func DelegatedHeaderConfigured(policy *Document, name string) bool {
	if !UsesDelegatedHeaders(policy) {
		return false
	}
	for _, header := range policy.Auth.Headers {
		if strings.EqualFold(header, name) {
			return true
		}
	}
	return false
}

// PolicyUsesOAuth returns true if the policy uses OAuth authentication.
func PolicyUsesOAuth(policy *Document) bool {
	if policy == nil || policy.Auth == nil || UsesDelegatedHeaders(policy) {
		return false
	}
	return strings.TrimSpace(policy.Auth.IssuerURL) != "" || strings.TrimSpace(policy.Auth.Audience) != ""
}

// ChoosePolicyVersion returns the first non-empty policy version from the provided values,
// or empty string if none found. Callers should pass their own default as the last value.
func ChoosePolicyVersion(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
