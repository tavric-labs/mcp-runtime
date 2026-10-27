package httpx

import "testing"

func TestNormalizePublicAPIPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/api/v1/runtime/teams/core/members/user-1", "/runtime/teams/core/members/user-1"},
		{"/api/runtime/grants/ns/g", "/runtime/grants/ns/g"},
		{"/health", "/health"},
	}
	for _, tc := range cases {
		if got := NormalizePublicAPIPath(tc.in); got != tc.want {
			t.Fatalf("NormalizePublicAPIPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
