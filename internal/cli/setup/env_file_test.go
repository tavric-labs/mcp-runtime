package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEnvFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile.env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadEnvFileAcceptsAnUnsetLine(t *testing.T) {
	t.Setenv("ENVFILE_TEST_KEPT", "")
	os.Unsetenv("ENVFILE_TEST_KEPT")
	os.Unsetenv("ENVFILE_TEST_DROPPED")
	path := writeEnvFile(t, "export ENVFILE_TEST_KEPT=kept\nexport ENVFILE_TEST_DROPPED=gone\nunset ENVFILE_TEST_DROPPED\n")
	t.Cleanup(func() { os.Unsetenv("ENVFILE_TEST_KEPT") })

	if err := loadEnvFile(path); err != nil {
		t.Fatalf("loadEnvFile() = %v; a profile that unsets a variable must load", err)
	}
	if got := os.Getenv("ENVFILE_TEST_KEPT"); got != "kept" {
		t.Fatalf("ENVFILE_TEST_KEPT = %q, want kept", got)
	}
	if _, set := os.LookupEnv("ENVFILE_TEST_DROPPED"); set {
		t.Fatal("ENVFILE_TEST_DROPPED was set although the profile unsets it")
	}
}

func TestLoadEnvFileUnsetDoesNotClearAnExplicitEnvironmentValue(t *testing.T) {
	t.Setenv("ENVFILE_TEST_EXPLICIT", "from-environment")
	path := writeEnvFile(t, "unset ENVFILE_TEST_EXPLICIT\n")
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ENVFILE_TEST_EXPLICIT"); got != "from-environment" {
		t.Fatalf("ENVFILE_TEST_EXPLICIT = %q; explicit environment values always win over the file", got)
	}
}

func TestLoadEnvFileKeepsPlainKeyValueBehavior(t *testing.T) {
	os.Unsetenv("ENVFILE_TEST_PLAIN")
	t.Cleanup(func() { os.Unsetenv("ENVFILE_TEST_PLAIN") })
	path := writeEnvFile(t, "ENVFILE_TEST_PLAIN=value\n")
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ENVFILE_TEST_PLAIN"); got != "value" {
		t.Fatalf("ENVFILE_TEST_PLAIN = %q, want value", got)
	}
}
