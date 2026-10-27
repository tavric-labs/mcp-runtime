// Package events defines the platform event envelope shared by the MCP
// gateway, ingest and the processor.
//
// Envelope carries timestamp, trace id, source, event type and a raw JSON
// payload; NewEnvelope validates required fields. MCPRequest is the typed audit
// payload for a proxied MCP call.
package events
