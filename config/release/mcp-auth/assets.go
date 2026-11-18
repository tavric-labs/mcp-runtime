// Package mcpauthpin exposes the pinned default MCP Auth image. The pin lives in
// the Dockerfile beside this file so Dependabot can bump it; nothing else in the
// repository repeats the version.
package mcpauthpin

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed Dockerfile
var dockerfile string

// DefaultImage is the image `setup --with-mcp-auth-server` installs when
// --mcp-auth-server-image is not set.
var DefaultImage = mustParse(dockerfile)

// Parse returns the image of the single FROM line in a pin file.
func Parse(content string) (string, error) {
	var image string
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		if len(fields) != 2 || image != "" {
			return "", fmt.Errorf("mcp-auth pin must have exactly one `FROM <image>` line")
		}
		image = fields[1]
	}
	if image == "" {
		return "", fmt.Errorf("mcp-auth pin has no FROM line")
	}
	return image, nil
}

func mustParse(content string) string {
	image, err := Parse(content)
	if err != nil {
		panic(err)
	}
	return image
}
