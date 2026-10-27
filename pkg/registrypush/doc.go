// Package registrypush pushes a local Docker archive into the in-cluster
// registry without exposing it publicly. It runs a short-lived helper Pod in a
// workload namespace, streams the archive to it with a transfer token, and
// rewrites push targets between the public, ingress and cluster registry hosts.
package registrypush
