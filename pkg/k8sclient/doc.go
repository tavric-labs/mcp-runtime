// Package k8sclient is the shared Kubernetes client wrapper.
//
// Clients bundles the typed, dynamic and discovery clients. Around it the
// package provides apply helpers with transient-error handling, namespace,
// Deployment, Job and ConfigMap operations, cert-manager Certificate, CRD and
// issuer checks, registry ingress host selection with a guard against
// downgrading live routes, and the Deployment and Traefik readers used by
// workload-identity diagnostics (workload_identity_*.go).
//
// Only adapters may import it; domain and leaf packages must stay free of
// Kubernetes libraries.
package k8sclient
