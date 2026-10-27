package access

import (
	"errors"
	"fmt"
	"strings"

	"mcp-runtime/pkg/platforminventory"
)

// DefaultMCPResourceNamespace is used when a ServerReference or access resource omits a namespace.
const DefaultMCPResourceNamespace = platforminventory.MCPServersNamespace

// ServerRetiringAnnotation prevents issuing access or republishing a server
// while a failed retirement is awaiting a retry.
const ServerRetiringAnnotation = "mcpruntime.org/retiring"

// ServerUIDAnnotation fences delayed authorization writes from an earlier
// deployment when the same namespace/name is published again.
const ServerUIDAnnotation = "mcpruntime.org/server-uid"

// ResolveServerRefNamespace returns the namespace to resolve serverRef against.
// Empty or whitespace ref.Namespace defaults to DefaultMCPResourceNamespace.
func ResolveServerRefNamespace(ref ServerReference) Namespace {
	if ns := strings.TrimSpace(string(ref.Namespace)); ns != "" {
		return Namespace(ns)
	}
	return Namespace(DefaultMCPResourceNamespace)
}

// ErrMCPServerNotFound is returned by AssertMCPServerRef when the target MCPServer is missing.
type ErrMCPServerNotFound struct {
	Name, Namespace string
}

func (e *ErrMCPServerNotFound) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("unknown serverRef: MCPServer %q not found in namespace %q", e.Name, e.Namespace)
}

// IsMCPServerNotFoundForRef returns true if err is an *ErrMCPServerNotFound.
func IsMCPServerNotFoundForRef(err error) bool {
	var missing *ErrMCPServerNotFound
	return err != nil && errors.As(err, &missing)
}
