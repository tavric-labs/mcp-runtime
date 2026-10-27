package apikit

import (
	"net/http/httptest"
	"testing"
)

func TestQueryIntOrFallsBackOnMissingOrInvalidValues(t *testing.T) {
	for name, tc := range map[string]struct {
		query string
		want  int
	}{
		"missing":       {"", 25},
		"blank":         {"?limit=%20", 25},
		"not a number":  {"?limit=abc", 25},
		"valid":         {"?limit=7", 7},
		"padded":        {"?limit=%209%20", 9},
		"negative":      {"?limit=-3", -3},
		"out of bounds": {"?limit=100000", 100000},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/x"+tc.query, nil)
			if got := QueryIntOr(r, "limit", 25); got != tc.want {
				t.Fatalf("QueryIntOr() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestClampInt(t *testing.T) {
	for _, tc := range []struct{ n, lo, hi, want int }{
		{5, 1, 10, 5}, {0, 1, 10, 1}, {11, 1, 10, 10}, {1, 1, 10, 1}, {10, 1, 10, 10},
	} {
		if got := ClampInt(tc.n, tc.lo, tc.hi); got != tc.want {
			t.Fatalf("ClampInt(%d,%d,%d) = %d, want %d", tc.n, tc.lo, tc.hi, got, tc.want)
		}
	}
}
