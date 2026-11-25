package access

import (
	"strings"
	"testing"
)

func TestValidateServerEgress(t *testing.T) {
	valid := []struct {
		name  string
		hosts []string
		ports []int32
	}{
		{"single host default port", []string{"www.googleapis.com"}, nil},
		{"mixed case is normalised", []string{"API.Example.COM"}, []int32{443, 8443}},
		{"max hosts", manyHosts(32), nil},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			if err := ValidateServerEgress(tc.hosts, tc.ports); err != nil {
				t.Fatalf("ValidateServerEgress(%v, %v) = %v, want nil", tc.hosts, tc.ports, err)
			}
		})
	}

	invalid := []struct {
		name  string
		hosts []string
		ports []int32
		want  string
	}{
		{"no hosts", nil, nil, "at least one"},
		{"too many hosts", manyHosts(33), nil, "at most 32"},
		{"wildcard", []string{"*.googleapis.com"}, nil, "wildcard"},
		{"ipv4 literal", []string{"10.0.0.1"}, nil, "IP"},
		{"ipv6 literal", []string{"::1"}, nil, "IP"},
		{"url instead of host", []string{"https://www.googleapis.com/youtube"}, nil, "DNS name"},
		{"host with port", []string{"example.com:443"}, nil, "DNS name"},
		{"single label", []string{"localhost"}, nil, "DNS name"},
		{"underscore", []string{"bad_host.example.com"}, nil, "DNS name"},
		{"trailing dot is rejected", []string{"example.com."}, nil, "DNS name"},
		{"cluster local", []string{"kubernetes.default.svc.cluster.local"}, nil, "cluster-internal"},
		{"internal suffix", []string{"db.internal"}, nil, "cluster-internal"},
		{"port zero", []string{"example.com"}, []int32{0}, "port"},
		{"port too large", []string{"example.com"}, []int32{65536}, "port"},
		{"too many ports", []string{"example.com"}, []int32{1, 2, 3, 4, 5, 6, 7, 8, 9}, "at most 8"},
		{"duplicate host", []string{"example.com", "EXAMPLE.com"}, nil, "duplicate"},
		{"duplicate port", []string{"example.com"}, []int32{443, 443}, "duplicate"},
	}
	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			err := ValidateServerEgress(tc.hosts, tc.ports)
			if err == nil {
				t.Fatalf("ValidateServerEgress(%v, %v) = nil, want error containing %q", tc.hosts, tc.ports, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestServerEgressEqualIgnoresOrderAndCase(t *testing.T) {
	if !ServerEgressEqual([]string{"B.example.com", "a.example.com"}, nil, []string{"a.example.com", "b.example.com"}, []int32{443}) {
		t.Fatal("same hosts (reordered, different case) and default port should be equal")
	}
	if ServerEgressEqual([]string{"a.example.com"}, nil, []string{"a.example.com", "b.example.com"}, nil) {
		t.Fatal("an added host must not be equal")
	}
	if ServerEgressEqual([]string{"a.example.com"}, []int32{443}, []string{"a.example.com"}, []int32{8443}) {
		t.Fatal("a changed port must not be equal")
	}
	if ServerEgressEqual(nil, nil, []string{"a.example.com"}, nil) {
		t.Fatal("no egress must not equal declared egress")
	}
	if !ServerEgressEqual(nil, nil, nil, nil) {
		t.Fatal("two empty declarations are equal")
	}
}

func manyHosts(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "h"+strings.Repeat("a", i)+".example.com")
	}
	return out
}
