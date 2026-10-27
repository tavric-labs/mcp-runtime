package metadata

import (
	"net"
	"os"
	"strings"
)

// PublishScopeLabels returns the labels that record a server's publish scope.
func PublishScopeLabels(scope PublishScope) map[string]string {
	if scope == "" {
		return nil
	}
	return map[string]string{"mcpruntime.org/scope": string(scope)}
}

// ImageRefForClusterPull rewrites platform-registry images to the in-cluster pull host.
func ImageRefForClusterPull(image string) string {
	image = strings.TrimSpace(image)
	pullHost := ResolveRegistryPullHost()
	if pullHost == "" {
		return image
	}
	if pullHost == normalizeRegistryHost(ResolveRegistryHost()) {
		return image
	}
	registry, hasRegistry := imageRegistryHost(image)
	if hasRegistry && !isPlatformRegistryHost(registry) {
		return image
	}
	if rewritten, ok := RewriteImageRegistryHost(image, pullHost); ok {
		return rewritten
	}
	return image
}

func imageRegistryHost(image string) (string, bool) {
	first, _, found := strings.Cut(image, "/")
	if !found || !(strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		return "", false
	}
	return normalizeRegistryHost(first), true
}

func isPlatformRegistryHost(host string) bool {
	host = normalizeRegistryHost(host)
	if host == "" {
		return false
	}
	for _, candidate := range []string{
		os.Getenv(envMCPRegistryIngressHost),
		os.Getenv(envMCPRegistryHost),
		os.Getenv(envMCPRegistryEndpoint),
		ResolveRegistryHost(),
	} {
		if normalized := normalizeRegistryHost(candidate); normalized != "" && normalized == host {
			return true
		}
	}
	if domain := platformDomainFromEnv(); domain != "" && host == registryHostForDomain(domain) {
		return true
	}
	// The local default and loopback names are platform placeholders: images
	// built for development are tagged with them and must still be rewritten
	// to the in-cluster pull host once a real registry is configured.
	if host == normalizeRegistryHost(DefaultRegistryHost) {
		return true
	}
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	return name == "localhost" || name == "127.0.0.1"
}

func normalizeRegistryHost(host string) string {
	host = strings.TrimSpace(host)
	if scheme := strings.Index(host, "://"); scheme >= 0 {
		host = host[scheme+3:]
	}
	if before, _, found := strings.Cut(host, "/"); found {
		host = before
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "/"))
}
