package access

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"sync/atomic"

	"mcp-runtime/internal/cli/core"
	kubeapply "mcp-runtime/internal/cli/kube"
)

// maxTokenBytes bounds how much of a token file is read; real access tokens
// are a few kilobytes.
const maxTokenBytes = 64 * 1024

// explainIdentityNote is printed with every explanation. The gateway matches
// grants against the token's sub claim, while explain can only evaluate the
// identity it is given, so a passing explain does not prove a live call matches.
const explainIdentityNote = "explain evaluates the identity you supply; the gateway evaluates the token's sub claim. Use --token-file or --token-env to evaluate the subject a client actually presents."

var cliVersion atomic.Value

// SetCLIVersion records the running CLI version so errors can name it.
// An empty value falls back to the module build info.
func SetCLIVersion(version string) {
	cliVersion.Store(strings.TrimSpace(version))
}

func currentCLIVersion() string {
	if v, ok := cliVersion.Load().(string); ok && v != "" {
		return v
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "unknown"
}

var unsupportedSchemaPattern = regexp.MustCompile(`unsupported schema version "([^"]*)"`)

// wrapSchemaVersionError turns the policy validator's "unsupported schema
// version" failure into guidance for a CLI that is older than the platform.
// Other errors pass through unchanged.
func wrapSchemaVersionError(err error) error {
	if err == nil {
		return nil
	}
	match := unsupportedSchemaPattern.FindStringSubmatch(err.Error())
	if match == nil {
		return err
	}
	return fmt.Errorf("this CLI (%s) is older than the platform policy (schema version %q) and cannot read it: install the matching release of the mcp-runtime CLI from https://github.com/tavric-labs/mcp-runtime/releases (the platform itself does not need to change): %w", currentCLIVersion(), match[1], err)
}

// subjectFromToken returns the sub claim of a JWT. The signature is not
// verified: the value is only used to show or seed the identity a client will
// present. Nothing else from the token is returned, and errors never include
// token text.
func subjectFromToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", core.NewWithBase(nil, "token is empty")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", core.NewWithBase(nil, "token is not a JWT (expected three dot-separated parts); opaque tokens carry no readable subject")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", core.NewWithBase(nil, "token payload is not valid base64url")
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", core.NewWithBase(nil, "token payload is not valid JSON")
	}
	sub := strings.TrimSpace(claims.Sub)
	if sub == "" {
		return "", core.NewWithBase(nil, "token has no sub claim")
	}
	return sub, nil
}

// resolveTokenSubject reads a token from a file or an environment variable and
// returns its subject. It returns "" with no error when neither source is set.
func resolveTokenSubject(file, envName string) (string, error) {
	file = strings.TrimSpace(file)
	envName = strings.TrimSpace(envName)
	switch {
	case file != "" && envName != "":
		return "", core.NewWithBase(nil, "use only one of --token-file and --token-env")
	case file != "":
		path, err := kubeapply.ResolveRegularFilePath(file)
		if err != nil {
			return "", err
		}
		data, err := kubeapply.ReadFileAtPath(path)
		if err != nil {
			return "", err
		}
		if len(data) > maxTokenBytes {
			return "", core.NewWithBase(nil, "token file is too large")
		}
		return subjectFromToken(string(data))
	case envName != "":
		value, ok := os.LookupEnv(envName)
		if !ok || strings.TrimSpace(value) == "" {
			return "", core.NewWithBase(nil, fmt.Sprintf("environment variable %s is not set", envName))
		}
		return subjectFromToken(value)
	}
	return "", nil
}
