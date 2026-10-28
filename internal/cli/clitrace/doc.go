// Package clitrace gives one CLI invocation one trace. Its transport carries
// that trace to the platform on the CLI's platform API clients, so audit
// entries and server spans for a command share a trace id the console can open.
package clitrace
