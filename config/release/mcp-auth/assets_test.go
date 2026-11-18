package mcpauthpin

import (
	"regexp"
	"testing"
)

func TestDefaultImageIsPinnedSemverOnTheOfficialRepository(t *testing.T) {
	re := regexp.MustCompile(`^docker\.io/princekrroshan01/mcp-auth-server:(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)
	if !re.MatchString(DefaultImage) {
		t.Fatalf("pinned image %q must be docker.io/princekrroshan01/mcp-auth-server:<MAJOR.MINOR.PATCH> (no latest, no digest-less float)", DefaultImage)
	}
}

func TestParseRejectsMalformedPins(t *testing.T) {
	for name, in := range map[string]string{
		"empty":    "# nothing\n",
		"two from": "FROM a:1.0.0\nFROM b:1.0.0\n",
		"stage":    "FROM a:1.0.0 AS x\n",
	} {
		if _, err := Parse(in); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if got, err := Parse("# c\nFROM a:1.2.3\n"); err != nil || got != "a:1.2.3" {
		t.Fatalf("got %q, %v", got, err)
	}
}
