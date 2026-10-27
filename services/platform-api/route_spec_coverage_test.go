package main

import (
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"

	"mcp-runtime/pkg/openapi"
)

// undocumentedRoutes is the backlog of public routes that openapi.yaml does not
// describe. It is empty: a new route must be documented when it is added. An
// entry that gets documented must be removed, so the list only shrinks.
var undocumentedRoutes = []string{}

func routeDocumented(specPaths []string, pattern string) bool {
	if !strings.HasSuffix(pattern, "/") {
		return slices.Contains(specPaths, pattern)
	}
	// A subtree pattern is documented by any path below it.
	for _, path := range specPaths {
		if strings.HasPrefix(path, pattern) {
			return true
		}
	}
	return false
}

func TestEveryPublicRouteIsDocumentedOrListedAsBacklog(t *testing.T) {
	doc, err := openapi.Load(openAPISpec)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	var specPaths []string
	for path := range doc.Paths.Map() {
		specPaths = append(specPaths, path)
	}

	var patterns []string
	srv := &apiServer{routeObserver: func(pattern string) { patterns = append(patterns, pattern) }}
	srv.registerRoutes(http.NewServeMux())

	var missing []string
	for _, pattern := range patterns {
		if routeDocumented(specPaths, pattern) || pattern == "/api/v1/openapi.yaml" {
			continue
		}
		if !slices.Contains(undocumentedRoutes, pattern) {
			missing = append(missing, pattern)
		}
	}
	sort.Strings(missing)
	for _, pattern := range missing {
		t.Errorf("%s is registered but not described in openapi.yaml; document it", pattern)
	}
	for _, listed := range undocumentedRoutes {
		if routeDocumented(specPaths, listed) {
			t.Errorf("%s is now documented; remove it from undocumentedRoutes", listed)
		}
		if !slices.Contains(patterns, listed) {
			t.Errorf("%s is listed as undocumented but no longer registered; remove it", listed)
		}
	}
}
