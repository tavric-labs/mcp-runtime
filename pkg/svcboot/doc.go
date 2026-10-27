// Package svcboot is service startup.
//
// Run(Config) builds the http.Server with safe timeouts, wraps the handler with
// request IDs, request metrics and tracing, and serves until shutdown.
// APIKeySet and APIKeysFromEnv parse comma-separated API key lists. Services
// call it from main so they all start, log and shut down the same way.
package svcboot
