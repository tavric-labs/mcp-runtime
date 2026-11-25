package main

// extensionRegistry lists the protocol extensions whose methods the gateway
// knows. Each method states whether policy gates it (it must then also be a
// primitive method in pkg/policy) and is given a fixed metric label, so a client
// can never mint label values. A method that is not registered, and is not a
// core method, is forwarded without grant evaluation and counted as "other";
// see docs/identity-and-authorization.md.
//
// MCP Apps adds no gateway-visible method: its ui:// resources travel over
// resources/read (gated by resource rules) and an app-originated call reaches
// the gateway as an ordinary tools/call from the host. Its ui/* methods are
// postMessage traffic between host and app.
var extensionRegistry = []extensionSpec{
	{
		ID: "io.modelcontextprotocol/skills",
		Methods: []extensionMethod{
			{Method: "skills/list", Gated: true},
			{Method: "skills/get", Gated: true},
			{Method: "resources/directory/read", Gated: true},
		},
	},
	{
		ID: "io.modelcontextprotocol/tasks",
		Methods: []extensionMethod{
			{Method: "tasks/get", Gated: true},
			{Method: "tasks/update", Gated: true},
			{Method: "tasks/cancel", Gated: true},
		},
	},
	{ID: "io.modelcontextprotocol/ui"},
}

type extensionSpec struct {
	// ID is the {vendor}/{name} capability identifier. Confirm it against the
	// extension's own page before relying on it.
	ID      string
	Methods []extensionMethod
}

type extensionMethod struct {
	Method string
	// Gated is true when the method needs an active matching grant.
	Gated bool
}

// extensionMethodLabels is the set of registered extension methods, used by
// metricRPCMethod.
var extensionMethodLabels = func() map[string]struct{} {
	set := map[string]struct{}{}
	for _, ext := range extensionRegistry {
		for _, m := range ext.Methods {
			set[m.Method] = struct{}{}
		}
	}
	return set
}()
