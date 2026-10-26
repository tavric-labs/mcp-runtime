package auth

import (
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"
)

// grafanaSignInTarget uses only a relative, validated Grafana page path from
// forwardAuth. Never construct a redirect from the forwarded host or scheme.
func (s *Server) grafanaSignInTarget(r *http.Request) string {
	method := r.Header.Get("X-Forwarded-Method")
	if method != http.MethodGet && method != http.MethodHead {
		return ""
	}
	wantsHTML := false
	for _, accept := range strings.Split(r.Header.Get("Accept"), ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(accept))
		if err != nil || media != "text/html" {
			continue
		}
		if quality, ok := params["q"]; ok {
			q, err := strconv.ParseFloat(quality, 64)
			if err != nil || !(q > 0 && q <= 1) {
				continue
			}
		}
		wantsHTML = true
	}
	if !wantsHTML {
		return ""
	}
	uri := r.Header.Get("X-Forwarded-Uri")
	if !strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, "//") {
		return ""
	}
	u, err := url.ParseRequestURI(uri)
	if err != nil || u.IsAbs() || u.Host != "" || strings.ContainsRune(u.Path, '\\') || strings.ContainsFunc(u.Path, unicode.IsControl) {
		return ""
	}
	routes := s.PublicRoutes.WithDefaults()
	clean := path.Clean(u.Path)
	if clean != routes.Grafana && !strings.HasPrefix(clean, routes.Grafana+"/") {
		return ""
	}
	if clean == routes.Grafana+"/api" || strings.HasPrefix(clean, routes.Grafana+"/api/") {
		return ""
	}
	// Preserve valid page paths and query encodings, including Explore links,
	// while excluding paths that browsers would normalize out of Grafana.
	if strings.TrimSuffix(u.Path, "/") != clean {
		return ""
	}
	return routes.PlatformPrefix() + "/#/signin?" + url.Values{"return_to": {u.RequestURI()}}.Encode()
}
