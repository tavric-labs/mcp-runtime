package access

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
)

// EgressApprovedAnnotation marks an MCPServer whose declared external egress
// (spec.egress) a platform administrator approved. Only the admin
// "server egress approve" path sets it; the platform publish API never copies
// it from a request and drops it when the declaration changes.
const EgressApprovedAnnotation = "mcpruntime.org/egress-approved"

const (
	// MaxServerEgressHosts bounds spec.egress.hosts.
	MaxServerEgressHosts = 32
	// MaxServerEgressPorts bounds spec.egress.ports.
	MaxServerEgressPorts = 8
	// DefaultServerEgressPort is used when spec.egress.ports is empty.
	DefaultServerEgressPort int32 = 443
)

var egressHostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidateServerEgress validates a server's declared external egress: DNS
// names only (no wildcards, IP literals, URLs or ports), public-looking names,
// 1-32 unique hosts and 1-65535 unique TCP ports (at most 8).
func ValidateServerEgress(hosts []string, ports []int32) error {
	if len(hosts) == 0 {
		return fmt.Errorf("egress.hosts needs at least one host")
	}
	if len(hosts) > MaxServerEgressHosts {
		return fmt.Errorf("egress.hosts allows at most %d hosts", MaxServerEgressHosts)
	}
	seen := map[string]struct{}{}
	for _, raw := range hosts {
		host := NormalizeEgressHost(raw)
		if err := validateEgressHost(host); err != nil {
			return fmt.Errorf("egress.hosts %q: %w", raw, err)
		}
		if _, dup := seen[host]; dup {
			return fmt.Errorf("egress.hosts %q: duplicate host", raw)
		}
		seen[host] = struct{}{}
	}
	if len(ports) > MaxServerEgressPorts {
		return fmt.Errorf("egress.ports allows at most %d ports", MaxServerEgressPorts)
	}
	seenPorts := map[int32]struct{}{}
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("egress.ports %d: port must be 1-65535", port)
		}
		if _, dup := seenPorts[port]; dup {
			return fmt.Errorf("egress.ports %d: duplicate port", port)
		}
		seenPorts[port] = struct{}{}
	}
	return nil
}

// NormalizeEgressHost trims and lower-cases a declared host.
func NormalizeEgressHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

func validateEgressHost(host string) error {
	if strings.Contains(host, "*") {
		return fmt.Errorf("wildcards are not allowed")
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("IP literals are not allowed, declare a DNS name")
	}
	if len(host) == 0 || len(host) > 253 {
		return fmt.Errorf("must be a DNS name")
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return fmt.Errorf("must be a fully qualified DNS name such as api.example.com")
	}
	for _, label := range labels {
		if !egressHostLabel.MatchString(label) {
			return fmt.Errorf("must be a DNS name without scheme, port, path or trailing dot")
		}
	}
	last := labels[len(labels)-1]
	switch last {
	case "local", "internal", "svc", "cluster", "localhost", "lan", "home", "corp":
		return fmt.Errorf("cluster-internal or private names are not allowed")
	}
	if slices.Contains(labels, "svc") {
		return fmt.Errorf("cluster-internal or private names are not allowed")
	}
	return nil
}

// ServerEgressEqual reports whether two declarations request the same hosts
// and ports. Order and host case are ignored and an empty port list means the
// default port.
func ServerEgressEqual(aHosts []string, aPorts []int32, bHosts []string, bPorts []int32) bool {
	return slices.Equal(canonicalHosts(aHosts), canonicalHosts(bHosts)) &&
		slices.Equal(canonicalPorts(aPorts, len(aHosts) > 0), canonicalPorts(bPorts, len(bHosts) > 0))
}

func canonicalHosts(hosts []string) []string {
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, NormalizeEgressHost(host))
	}
	slices.Sort(out)
	return out
}

func canonicalPorts(ports []int32, declared bool) []int32 {
	if len(ports) == 0 {
		if !declared {
			return nil
		}
		return []int32{DefaultServerEgressPort}
	}
	out := slices.Clone(ports)
	slices.Sort(out)
	return out
}
