// Package oidcdiscovery reads issuer metadata and validates discovery redirects
// and signing-key locations. It retains the discovery client's existing request
// budget and transient startup retries; token authentication stays with callers.
package oidcdiscovery
