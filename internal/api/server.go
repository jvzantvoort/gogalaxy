// Package api assembles the HTTP handler of the galaxy server.
package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/jvzantvoort/gogalaxy/internal/api/ee"
	"github.com/jvzantvoort/gogalaxy/internal/api/httpx"
	v1 "github.com/jvzantvoort/gogalaxy/internal/api/v1"
	v3 "github.com/jvzantvoort/gogalaxy/internal/api/v3"
	"github.com/jvzantvoort/gogalaxy/internal/auth"
	"github.com/jvzantvoort/gogalaxy/internal/service"
	"github.com/jvzantvoort/gogalaxy/internal/version"
)

// Options configures the handler.
type Options struct {
	Service *service.Service
	Auth    auth.Chain
	Signer  *auth.Signer
	Log     *slog.Logger
}

// NewHandler returns the complete HTTP handler.
func NewHandler(o Options) http.Handler {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Signer == nil {
		o.Signer = auth.NewSigner(o.Service.Cfg.Server.SecretKey)
	}
	cfg := o.Service.Cfg
	prefix := cfg.API.Prefix
	h := &rootHandler{svc: o.Service}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer, accessLog(o.Log), authenticate(o.Auth))
	r.NotFound(appendSlash(r))
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.V3Error(w, http.StatusMethodNotAllowed, "method_not_allowed",
			`Method "`+r.Method+`" not allowed.`, "")
	})

	r.Get("/healthz", health)
	r.Get(prefix+"/status/healthz", health)

	// ansible-galaxy appends "api/" when the configured URL is not an API root.
	// Serving a root at /api/ lets clients use the bare server URL.
	if prefix != "/api" {
		r.Get("/api/", h.serverRoot)
	}

	r.Get(prefix+"/", h.apiRoot)
	r.Get(prefix+"/api/", redirect(prefix+"/"))
	r.Get(prefix+"/content/{repo}/", h.apiRoot)
	r.Get(prefix+"/content/{repo}/api/", func(w http.ResponseWriter, r *http.Request) {
		repo := chi.URLParam(r, "repo")
		if !cfg.HasRepository(repo) {
			httpx.V3Error(w, http.StatusNotFound, "not_found", "Not found.", "")
			return
		}
		// Local path built from a configured repository name.
		http.Redirect(w, r, prefix+"/content/"+repo+"/", http.StatusTemporaryRedirect) //nolint:gosec // see above
	})

	// Execution environments are not hosted.
	r.HandleFunc(prefix+"/v3/plugin/execution-environments/*", ee.GalaxyHandler)
	r.HandleFunc(prefix+"/_ui/v1/execution-environments/*", ee.GalaxyHandler)
	r.HandleFunc("/v2/*", ee.RegistryHandler)
	r.HandleFunc("/v2", ee.RegistryHandler)
	r.Get(prefix+"/_ui/v1/feature-flags/", h.featureFlags)

	v3api := v3.New(o.Service)
	repoCtx := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if repo := chi.URLParam(r, "repo"); repo != "" && !cfg.HasRepository(repo) {
				httpx.V3Error(w, http.StatusNotFound, "not_found", "Not found.", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	r.Route(prefix+"/v3", v3api.Routes)
	r.With(repoCtx).Route(prefix+"/content/{repo}/v3", v3api.Routes)

	if cfg.Content.EnableLegacyRoles {
		r.Route(prefix+"/v1", v1.New(o.Service, o.Signer).Routes)
	}
	return r
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("OK"))
}

func redirect(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, to, http.StatusTemporaryRedirect)
	}
}

// appendSlash redirects GET requests lacking a trailing slash to the slashed
// route when it exists, mirroring Django's APPEND_SLASH.
func appendSlash(mux *chi.Mux) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Paths starting with "//" would become protocol relative (off-site) redirects.
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && !strings.HasSuffix(r.URL.Path, "/") &&
			!strings.HasPrefix(r.URL.Path, "//") {
			if mux.Match(chi.NewRouteContext(), r.Method, r.URL.Path+"/") {
				target := r.URL.Path + "/"
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, target, http.StatusMovedPermanently) //nolint:gosec // local path, "//" excluded above
				return
			}
		}
		httpx.V3Error(w, http.StatusNotFound, "not_found", "Not found.", "")
	}
}

// authenticate resolves the request user. Invalid credentials are rejected
// right away; requests without credentials continue anonymously.
func authenticate(chain auth.Chain) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, err := chain.Authenticate(r)
			if err != nil {
				if !errors.Is(err, auth.ErrInvalidCredentials) {
					slog.Error("authentication backend failed", "error", err)
				}
				if c := chain.Challenge(); c != "" {
					w.Header().Set("WWW-Authenticate", c)
				}
				httpx.V3Error(w, http.StatusUnauthorized, "authentication_failed", "Invalid credentials.", "")
				return
			}
			if u != nil {
				r = r.WithContext(auth.WithUser(r.Context(), u))
			}
			next.ServeHTTP(w, r)
		})
	}
}

func accessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			user := "-"
			if u := auth.UserFrom(r.Context()); u != nil {
				user = u.Username
			}
			log.Info("request", "method", r.Method, "path", r.URL.Path, "status", ww.Status(),
				"bytes", ww.BytesWritten(), "duration", time.Since(start).Round(time.Microsecond),
				"remote", r.RemoteAddr, "user", user)
		})
	}
}

type rootHandler struct{ svc *service.Service }

func (h *rootHandler) versions(authenticated bool) map[string]any {
	cfg := h.svc.Cfg
	available := map[string]string{"v3": "v3/"}
	if cfg.Content.EnableLegacyRoles {
		available["v1"] = "v1/"
	}
	data := map[string]any{"available_versions": available}
	if authenticated {
		v := cfg.ReportedVersion(version.GalaxyNGCompat)
		data["server_version"] = v
		data["galaxy_ng_version"] = v
		data["galaxy_ng_commit"] = version.Commit
		data["gogalaxy_version"] = version.Version
	}
	return data
}

// apiRoot serves <prefix>/ and <prefix>/content/{repo}/.
func (h *rootHandler) apiRoot(w http.ResponseWriter, r *http.Request) {
	data := h.versions(auth.UserFrom(r.Context()) != nil)
	if repo := chi.URLParam(r, "repo"); repo != "" {
		if !h.svc.Cfg.HasRepository(repo) {
			httpx.JSON(w, http.StatusNotFound, map[string]string{"detail": "Not found."})
			return
		}
		data["distro_base_path"] = repo
		// v1 is only served at the top level.
		data["available_versions"] = map[string]string{"v3": "v3/"}
	}
	httpx.JSON(w, http.StatusOK, data)
}

// serverRoot serves /api/ with version paths relative to it.
func (h *rootHandler) serverRoot(w http.ResponseWriter, r *http.Request) {
	data := h.versions(auth.UserFrom(r.Context()) != nil)
	rel := strings.TrimPrefix(h.svc.Cfg.API.Prefix, "/api/")
	available := map[string]string{}
	for k, v := range data["available_versions"].(map[string]string) {
		available[k] = rel + "/" + v
	}
	data["available_versions"] = available
	httpx.JSON(w, http.StatusOK, data)
}

func (h *rootHandler) featureFlags(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"execution_environments":    false,
		"legacy_roles":              h.svc.Cfg.Content.EnableLegacyRoles,
		"collection_signing":        false,
		"signatures_enabled":        false,
		"require_upload_signatures": false,
		"can_upload_signatures":     false,
		"display_signatures":        false,
		"ai_deny_index":             false,
		"_messages":                 []string{},
	})
}
