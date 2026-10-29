package k8sstore

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"mcp-runtime/pkg/access"
)

// MCPAccessGrant grants a human or agent access to an MCPServer.
type MCPAccessGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   access.MCPAccessGrantSpec   `json:"spec,omitempty"`
	Status access.MCPAccessGrantStatus `json:"status,omitempty"`
}

// MCPAccessGrantList contains a list of MCPAccessGrant.
type MCPAccessGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPAccessGrant `json:"items"`
}

// MCPAgentSession is a platform session management envelope, not a Kubernetes resource.
type MCPAgentSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   access.MCPAgentSessionSpec   `json:"spec,omitempty"`
	Status access.MCPAgentSessionStatus `json:"status,omitempty"`
}

// MCPAgentSessionList contains a list of MCPAgentSession.
type MCPAgentSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPAgentSession `json:"items"`
}
