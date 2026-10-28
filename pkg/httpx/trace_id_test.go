package httpx

import "testing"

func TestValidTraceID(t *testing.T) {
	for id, want := range map[string]bool{
		"4bf92f3577b34da6a3ce929d0e0e4736":  true,
		"4BF92F3577B34DA6A3CE929D0E0E4736":  false,
		"00000000000000000000000000000000":  false,
		"4bf92f3577b34da6a3ce929d0e0e473":   false,
		"4bf92f3577b34da6a3ce929d0e0e47360": false,
		"4bf92f3577b34da6a3ce929d0e0e473z":  false,
		"":                                  false,
	} {
		if got := ValidTraceID(id); got != want {
			t.Errorf("ValidTraceID(%q) = %v, want %v", id, got, want)
		}
	}
}
