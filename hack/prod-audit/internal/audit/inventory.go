package audit

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

func checkProductionInventory(raw []byte, manifestPath, tag, registry, authVersion string) error {
	var inventory struct{ Items []json.RawMessage }
	if json.Unmarshal(raw, &inventory) != nil {
		return fmt.Errorf("invalid deployment inventory")
	}
	byNamespace := map[string][]json.RawMessage{}
	images := map[string][]string{}
	for _, item := range inventory.Items {
		var d struct {
			Metadata struct{ Name, Namespace string }
			Spec     struct {
				Template struct {
					Spec struct{ Containers []struct{ Image string } }
				}
			}
		}
		if json.Unmarshal(item, &d) != nil {
			return fmt.Errorf("invalid deployment")
		}
		byNamespace[d.Metadata.Namespace] = append(byNamespace[d.Metadata.Namespace], item)
		key := d.Metadata.Namespace + "/" + d.Metadata.Name
		for _, c := range d.Spec.Template.Spec.Containers {
			images[key] = append(images[key], c.Image)
		}
	}
	for _, ns := range []string{"mcp-runtime", "mcp-platform", "mcp-observability", "registry", "cert-manager", "mcp-servers-org"} {
		list, _ := json.Marshal(map[string]any{"items": byNamespace[ns]})
		if err := CheckDeploymentReadiness(list); err != nil {
			return fmt.Errorf("%s: %w", ns, err)
		}
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("release manifest unavailable")
	}
	var manifest struct {
		Version    string
		Components []struct{ Name, Repository, Tag string }
	}
	if json.Unmarshal(data, &manifest) != nil || manifest.Version != tag {
		return fmt.Errorf("release manifest version mismatch")
	}
	componentMap := map[string]string{"operator": "mcp-runtime/mcp-runtime-operator-controller-manager", "platform-api": "mcp-platform/mcp-platform-api", "runtime-api": "mcp-platform/mcp-runtime-api", "analytics-api": "mcp-observability/mcp-analytics-api", "ingest": "mcp-observability/mcp-ingest", "processor": "mcp-observability/mcp-processor", "ui": "mcp-platform/mcp-ui", "gateway": "mcp-platform/mcp-platform-gateway"}
	registryURL, _ := url.Parse(registry)
	verified := map[string]bool{}
	for _, c := range manifest.Components {
		key, ok := componentMap[c.Name]
		if !ok {
			continue
		}
		repo := c.Repository
		if repo == "" || c.Tag == "" {
			return fmt.Errorf("release component %s has no image pin", c.Name)
		}
		if c.Name != "gateway" && !strings.Contains(repo, "/") {
			repo = registryURL.Host + "/" + repo
		}
		want := repo + ":" + c.Tag
		for _, image := range images[key] {
			if image == want {
				verified[c.Name] = true
			}
		}
		if !verified[c.Name] {
			return fmt.Errorf("deployment %s does not run the manifest image for %s", key, c.Name)
		}
	}
	for name := range componentMap {
		if !verified[name] {
			return fmt.Errorf("release manifest is missing required component %s", name)
		}
	}
	if authVersion != "" {
		want := "docker.io/princekrroshan01/mcp-auth-server:" + authVersion
		found := false
		for _, image := range images["mcp-platform/mcp-auth-server"] {
			if image == want {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("MCP Auth image does not match the requested version")
		}
	}
	return nil
}
