// Package ee answers execution environment (container) requests. gogalaxy
// does not host execution environments: listings are empty and everything
// else reports that none are available.
package ee

import (
	"net/http"
	"strings"

	"github.com/jvzantvoort/gogalaxy/internal/api/httpx"
)

// Message is returned for every execution environment lookup.
const Message = "No execution environments are available on this server."

// GalaxyHandler serves <prefix>/v3/plugin/execution-environments/* and
// <prefix>/_ui/v1/execution-environments/*.
func GalaxyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && isListing(r.URL.Path) {
		httpx.V3List(w, r, httpx.LimitOffset(r, 0), []any{})
		return
	}
	httpx.V3Error(w, http.StatusNotFound, "not_found", "Not found.", Message)
}

// isListing reports whether path is a collection endpoint such as
// .../repositories/, .../registries/ or .../remotes/.
func isListing(path string) bool {
	path = strings.TrimSuffix(path, "/")
	for _, s := range []string{"/repositories", "/registries", "/remotes", "/namespaces"} {
		if strings.HasSuffix(path, s) {
			return true
		}
	}
	return false
}

// RegistryHandler serves the OCI distribution API (/v2/*) used by podman
// and docker.
func RegistryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	switch strings.TrimSuffix(r.URL.Path, "/") {
	case "/v2":
		httpx.JSON(w, http.StatusOK, map[string]any{})
	case "/v2/_catalog":
		httpx.JSON(w, http.StatusOK, map[string]any{"repositories": []string{}})
	default:
		httpx.JSON(w, http.StatusNotFound, map[string]any{
			"errors": []map[string]string{{"code": "NAME_UNKNOWN", "message": Message}},
		})
	}
}
