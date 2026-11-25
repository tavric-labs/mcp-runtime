package audit

import (
	"testing"
	"time"
)

func TestGrantNameCarriesUTCDateAndSuffix(t *testing.T) {
	// 23:30 at UTC-5 is already the next UTC day; the name must use UTC.
	zone := time.FixedZone("test", -5*3600)
	now := time.Date(2026, 11, 11, 23, 30, 0, 0, zone)
	got := grantName(now, [8]byte{0x3f, 0xa9, 0xc2, 0, 0, 0, 0, 1})
	want := "prod-audit-20261112-3fa9c20000000001"
	if got != want {
		t.Fatalf("grantName = %q, want %q", got, want)
	}
}
