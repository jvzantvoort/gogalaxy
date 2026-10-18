// Package v1 implements the legacy galaxy v1 API used by
// "ansible-galaxy role ..." commands.
package v1

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jvzantvoort/gogalaxy/internal/api/httpx"
	"github.com/jvzantvoort/gogalaxy/internal/auth"
	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/semver"
	"github.com/jvzantvoort/gogalaxy/internal/service"
)

// downloadTTL is the validity of signed role download links.
const downloadTTL = 10 * time.Minute

// API serves the v1 endpoints.
type API struct {
	svc    *service.Service
	signer *auth.Signer
}

// New creates the v1 API.
func New(svc *service.Service, signer *auth.Signer) *API { return &API{svc: svc, signer: signer} }

// Routes registers the v1 endpoints on r (mounted at <prefix>/v1).
func (a *API) Routes(r chi.Router) {
	r.Get("/", a.root)
	r.Get("/roles/", a.listRoles)
	r.Get("/search/roles/", a.listRoles)
	r.Get("/roles/{id}/", a.getRole)
	r.Delete("/roles/{id}/", a.deleteRole)
	r.Get("/roles/{id}/versions/", a.roleVersions)
	r.Get("/roles/{id}/content/", a.roleContent)
	r.Delete("/removerole/", a.removeRole)
	r.Get("/imports/", a.listImports)
	r.Post("/imports/", a.importRole)
	r.Get("/namespaces/", a.listNamespaces)
	r.Get("/namespaces/{id}/", a.getNamespace)
	r.Get("/users/", a.listUsers)
	r.Get("/users/{id}/", a.getUser)
	r.Get("/artifacts/roles/{filename}", a.download)
}

func (a *API) base() string { return a.svc.Cfg.API.Prefix + "/v1" }

func (a *API) root(w http.ResponseWriter, r *http.Request) {
	b := a.base()
	httpx.JSON(w, http.StatusOK, map[string]string{
		"sync":       b + "/sync/",
		"imports":    b + "/imports/",
		"roles":      b + "/roles/",
		"users":      b + "/users/",
		"namespaces": b + "/namespaces/",
	})
}

// --- roles ------------------------------------------------------------------

func avatar(ns string) string { return "https://github.com/" + ns + ".png" }

func (a *API) roleJSON(r *http.Request, role *model.Role) map[string]any {
	versions := slices.Clone(role.Versions)
	slices.Reverse(versions)
	if len(versions) > 10 {
		versions = versions[:10]
	}
	summary := make([]map[string]any, 0, len(versions))
	for _, v := range versions {
		summary = append(summary, map[string]any{"id": v.ID, "name": v.Name, "release_date": httpx.Time(v.Created)})
	}
	nsID := int64(0)
	if ns, err := a.svc.Store.Namespaces().Get(r.Context(), role.Namespace); err == nil {
		nsID = ns.ID
	}
	repo := role.GithubRepo
	if repo == "" {
		repo = role.Name
	}
	githubUser := role.GithubUser
	if githubUser == "" {
		githubUser = role.Namespace
	}
	deps := role.Dependencies
	if deps == nil {
		deps = []any{}
	}
	tags := role.Tags
	if tags == nil {
		tags = []string{}
	}
	var branch any
	if role.GithubBranch != "" {
		branch = role.GithubBranch
	}
	return map[string]any{
		"id":             role.ID,
		"upstream_id":    nil,
		"created":        httpx.Time(role.Created),
		"modified":       httpx.Time(role.Modified),
		"imported":       httpx.Time(role.Modified),
		"github_user":    githubUser,
		"username":       role.Namespace,
		"github_repo":    repo,
		"github_branch":  branch,
		"commit":         nil,
		"commit_message": nil,
		"name":           role.Name,
		"description":    role.Description,
		"summary_fields": map[string]any{
			"dependencies":       deps,
			"namespace":          map[string]any{"id": nsID, "name": role.Namespace, "avatar_url": avatar(role.Namespace)},
			"provider_namespace": map[string]any{"id": nsID, "name": role.Namespace},
			"repository":         map[string]any{"name": repo, "original_name": repo},
			"tags":               tags,
			"versions":           summary,
		},
		"download_count": role.DownloadCount,
	}
}

func (a *API) listRoles(w http.ResponseWriter, r *http.Request) {
	ctx, u, q := r.Context(), auth.UserFrom(r.Context()), r.URL.Query()
	f := service.RoleFilter{Name: q.Get("name"), Keywords: append(q["keywords"], q["autocomplete"]...)}
	for _, k := range []string{"owner__username", "namespace", "github_user"} {
		if v := q.Get(k); v != "" {
			f.Namespace = v
		}
	}
	for _, k := range []string{"tags", "tag", "tags_autocomplete"} {
		for _, v := range q[k] {
			f.Tags = append(f.Tags, strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '+' || r == ' ' })...)
		}
	}
	roles, err := a.svc.ListRoles(ctx, u, f)
	if err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	if ua := strings.ToLower(q.Get("username_autocomplete")); ua != "" {
		roles = slices.DeleteFunc(roles, func(r *model.Role) bool { return !strings.Contains(strings.ToLower(r.Namespace), ua) })
	}
	sortRoles(roles, q.Get("order_by"))

	// Like galaxy_ng, a lookup by owner and name is counted as a download.
	if q.Get("owner__username") != "" && f.Name != "" && len(roles) > 0 {
		a.svc.CountRoleDownload(ctx, roles[0].Namespace, roles[0].Name)
		roles[0].DownloadCount++
	}

	p, page := httpx.PageNumber(r, len(roles), 10)
	lo, hi := p.Slice()
	out := make([]map[string]any, 0, hi-lo)
	for _, role := range roles[lo:hi] {
		out = append(out, a.roleJSON(r, role))
	}
	httpx.V1List(w, r, a.svc.Cfg, p, page, out)
}

func sortRoles(roles []*model.Role, order string) {
	desc := strings.HasPrefix(order, "-")
	key := strings.TrimPrefix(order, "-")
	cmp := func(x, y *model.Role) int {
		switch key {
		case "name":
			return strings.Compare(x.Name, y.Name)
		case "modified":
			return x.Modified.Compare(y.Modified)
		case "download_count":
			return int(x.DownloadCount - y.DownloadCount)
		}
		return x.Created.Compare(y.Created)
	}
	slices.SortStableFunc(roles, func(x, y *model.Role) int {
		if desc {
			return cmp(y, x)
		}
		return cmp(x, y)
	})
}

func (a *API) roleByID(w http.ResponseWriter, r *http.Request) *model.Role {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		httpx.JSON(w, http.StatusNotFound, map[string]string{"detail": "Not found."})
		return nil
	}
	role, err := a.svc.GetRoleByID(r.Context(), auth.UserFrom(r.Context()), id)
	if err != nil {
		httpx.V1Fail(w, r, err)
		return nil
	}
	return role
}

func (a *API) getRole(w http.ResponseWriter, r *http.Request) {
	if role := a.roleByID(w, r); role != nil {
		httpx.JSON(w, http.StatusOK, a.roleJSON(r, role))
	}
}

func (a *API) deleteRole(w http.ResponseWriter, r *http.Request) {
	role := a.roleByID(w, r)
	if role == nil {
		return
	}
	if err := a.svc.DeleteRole(r.Context(), auth.UserFrom(r.Context()), role.Namespace, role.Name, ""); err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) removeRole(w http.ResponseWriter, r *http.Request) {
	ctx, u, q := r.Context(), auth.UserFrom(r.Context()), r.URL.Query()
	if u == nil {
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"detail": "Authentication credentials were not provided."})
		return
	}
	ghUser, ghRepo := q.Get("github_user"), q.Get("github_repo")
	roles, err := a.svc.ListRoles(ctx, u, service.RoleFilter{Namespace: ghUser})
	if err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	deleted := []map[string]any{}
	for _, role := range roles {
		if role.GithubRepo != ghRepo && role.Name != ghRepo {
			continue
		}
		if err := a.svc.DeleteRole(ctx, u, role.Namespace, role.Name, ""); err != nil {
			httpx.V1Fail(w, r, err)
			return
		}
		deleted = append(deleted, map[string]any{
			"id": role.ID, "namespace": role.Namespace, "name": role.Name,
			"github_user": ghUser, "github_repo": ghRepo,
		})
	}
	var status string
	switch len(deleted) {
	case 0:
		status = fmt.Sprintf("Role %s.%s not found. Maybe it was deleted previously?", ghUser, ghRepo)
	case 1:
		status = fmt.Sprintf("Role %s.%s deleted", ghUser, ghRepo)
	default:
		status = fmt.Sprintf("Deleted %d roles associated with %s/%s", len(deleted), ghUser, ghRepo)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": status, "deleted_roles": deleted})
}

// downloadURL returns the absolute download link of a role version, signed
// when anonymous access is disabled (ansible-galaxy sends no credentials
// when downloading roles).
func (a *API) downloadURL(r *http.Request, v model.RoleVersion) string {
	path := a.base() + "/artifacts/roles/" + v.Artifact.Filename
	link := httpx.BaseURL(r, a.svc.Cfg) + path
	if !a.svc.Cfg.Auth.AnonymousRead {
		link += "?" + a.signer.Sign(path, downloadTTL)
	}
	return link
}

func (a *API) roleVersions(w http.ResponseWriter, r *http.Request) {
	role := a.roleByID(w, r)
	if role == nil {
		return
	}
	versions := slices.Clone(role.Versions)
	slices.SortStableFunc(versions, func(x, y model.RoleVersion) int { return semver.Compare(x.Name, y.Name) })
	results := make([]map[string]any, 0, len(versions))
	for _, v := range versions {
		results = append(results, map[string]any{
			"id":           v.ID,
			"name":         v.Name,
			"version":      v.Name,
			"created":      httpx.Time(v.Created),
			"modified":     httpx.Time(v.Modified),
			"commit_date":  httpx.Time(v.Created),
			"commit_sha":   nil,
			"download_url": a.downloadURL(r, v),
			"active":       true,
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"count": len(results), "next": nil, "next_link": nil, "previous": nil, "previous_link": nil,
		"results": results,
	})
}

func (a *API) roleContent(w http.ResponseWriter, r *http.Request) {
	if role := a.roleByID(w, r); role != nil {
		httpx.JSON(w, http.StatusOK, map[string]any{"readme": role.Readme, "readme_html": ""})
	}
}

func (a *API) download(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	u := auth.UserFrom(r.Context())
	if !a.svc.Policy.CanRead(u) && !a.signer.Verify(r.URL.Path, r.URL.Query()) {
		httpx.V1Fail(w, r, a.svc.CanRead(r.Context(), u))
		return
	}
	blob, err := a.svc.OpenRoleArtifact(r.Context(), filename)
	if err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	defer blob.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	http.ServeContent(w, r, filename, blob.ModTime, blob)
}

// --- imports ----------------------------------------------------------------

func (a *API) listImports(w http.ResponseWriter, r *http.Request) {
	p, page := httpx.PageNumber(r, 0, 10)
	httpx.V1List(w, r, a.svc.Cfg, p, page, []any{})
}

// importRole accepts a role archive as multipart/form-data with the fields
// file, version and optionally namespace (or github_user), name (or
// github_repo / alternate_role_name). Importing from GitHub, which is what
// galaxy_ng does for JSON requests, is not supported.
func (a *API) importRole(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"detail": "Authentication credentials were not provided."})
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{
			"detail": "Importing roles from GitHub is not supported; upload the role archive as multipart/form-data (file, version, namespace, name).",
		})
		return
	}
	if limit := a.svc.Cfg.Content.MaxUploadSize; limit > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpx.JSON(w, http.StatusRequestEntityTooLarge, map[string]string{"detail": "Archive exceeds the maximum upload size."})
			return
		}
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"detail": "Invalid multipart upload."})
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, _, err := httpx.FormFile(r, "file")
	if err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"detail": "No file was submitted."})
		return
	}
	defer file.Close()

	first := func(keys ...string) string {
		for _, k := range keys {
			if v := r.FormValue(k); v != "" {
				return v
			}
		}
		return ""
	}
	role, err := a.svc.ImportRole(r.Context(), u, service.RoleImport{
		Namespace:  first("namespace", "github_user"),
		Name:       first("name", "alternate_role_name", "github_repo"),
		Version:    first("version", "github_reference"),
		GithubUser: r.FormValue("github_user"),
		GithubRepo: r.FormValue("github_repo"),
		Body:       file,
	})
	if err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"results": []map[string]any{{
			"id": role.ID, "state": "SUCCESS", "role_id": role.ID,
			"summary_fields": map[string]any{
				"role":          map[string]any{"id": role.ID, "name": role.Name},
				"namespace":     map[string]any{"name": role.Namespace},
				"task_messages": []any{},
			},
		}},
	})
}

// --- namespaces & users -----------------------------------------------------

func (a *API) namespaceJSON(ns *model.Namespace) map[string]any {
	owners := make([]map[string]string, 0, len(ns.Owners))
	for _, o := range ns.Owners {
		owners = append(owners, map[string]string{"username": o})
	}
	return map[string]any{
		"id":         ns.ID,
		"url":        "",
		"name":       ns.Name,
		"created":    httpx.Time(ns.Created),
		"modified":   httpx.Time(ns.Modified),
		"avatar_url": avatar(ns.Name),
		"summary_fields": map[string]any{
			"owners":              owners,
			"provider_namespaces": []map[string]any{{"id": ns.ID, "name": ns.Name}},
		},
	}
}

func (a *API) listNamespaces(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.CanRead(r.Context(), auth.UserFrom(r.Context())); err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	all, err := a.svc.ListNamespaces(r.Context())
	if err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	if name := q.Get("name"); name != "" {
		all = slices.DeleteFunc(all, func(n *model.Namespace) bool { return n.Name != name })
	}
	if owner := q.Get("owner"); owner != "" {
		all = slices.DeleteFunc(all, func(n *model.Namespace) bool { return !n.HasOwner(owner) })
	}
	p, page := httpx.PageNumber(r, len(all), 10)
	lo, hi := p.Slice()
	out := make([]map[string]any, 0, hi-lo)
	for _, ns := range all[lo:hi] {
		out = append(out, a.namespaceJSON(ns))
	}
	httpx.V1List(w, r, a.svc.Cfg, p, page, out)
}

func (a *API) getNamespace(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.CanRead(r.Context(), auth.UserFrom(r.Context())); err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	all, err := a.svc.ListNamespaces(r.Context())
	if err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	for _, ns := range all {
		if ns.ID == id {
			httpx.JSON(w, http.StatusOK, a.namespaceJSON(ns))
			return
		}
	}
	httpx.JSON(w, http.StatusNotFound, map[string]string{"detail": "Not found."})
}

func userJSON(u *model.User) map[string]any {
	return map[string]any{
		"id":             u.ID,
		"url":            "",
		"username":       u.Username,
		"full_name":      strings.TrimSpace(u.FirstName + " " + u.LastName),
		"date_joined":    httpx.Time(u.Created),
		"created":        httpx.Time(u.Created),
		"modified":       httpx.Time(u.Modified),
		"avatar_url":     avatar(u.Username),
		"github_id":      nil,
		"summary_fields": map[string]any{},
	}
}

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.CanRead(r.Context(), auth.UserFrom(r.Context())); err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	all, err := a.svc.ListUsers(r.Context())
	if err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	if name := r.URL.Query().Get("username"); name != "" {
		all = slices.DeleteFunc(all, func(u *model.User) bool { return u.Username != name })
	}
	p, page := httpx.PageNumber(r, len(all), 10)
	lo, hi := p.Slice()
	out := make([]map[string]any, 0, hi-lo)
	for _, u := range all[lo:hi] {
		out = append(out, userJSON(u))
	}
	httpx.V1List(w, r, a.svc.Cfg, p, page, out)
}

func (a *API) getUser(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.CanRead(r.Context(), auth.UserFrom(r.Context())); err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	all, err := a.svc.ListUsers(r.Context())
	if err != nil {
		httpx.V1Fail(w, r, err)
		return
	}
	for _, u := range all {
		if u.ID == id {
			httpx.JSON(w, http.StatusOK, userJSON(u))
			return
		}
	}
	httpx.JSON(w, http.StatusNotFound, map[string]string{"detail": "Not found."})
}
