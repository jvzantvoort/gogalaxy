package v3

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jvzantvoort/gogalaxy/internal/api/httpx"
	"github.com/jvzantvoort/gogalaxy/internal/auth"
	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/service"
)

func (a *API) collectionJSON(repo string, c *model.Collection, versions []*model.CollectionVersion) map[string]any {
	out := map[string]any{
		"href":            a.indexHref(repo, "collections", c.Namespace, c.Name),
		"namespace":       c.Namespace,
		"name":            c.Name,
		"deprecated":      c.Deprecated,
		"versions_url":    a.indexHref(repo, "collections", c.Namespace, c.Name, "versions"),
		"highest_version": nil,
		"created_at":      httpx.Time(c.Created),
		"updated_at":      httpx.Time(c.Modified),
		"download_count":  c.DownloadCount,
	}
	if hv := service.HighestVersion(versions); hv != nil {
		out["highest_version"] = map[string]string{
			"href":    a.indexHref(repo, "collections", c.Namespace, c.Name, "versions", hv.Version),
			"version": hv.Version,
		}
	}
	return out
}

func (a *API) versionSummaryJSON(repo string, v *model.CollectionVersion) map[string]any {
	return map[string]any{
		"version":          v.Version,
		"href":             a.indexHref(repo, "collections", v.Namespace, v.Name, "versions", v.Version),
		"created_at":       httpx.Time(v.Created),
		"updated_at":       httpx.Time(v.Modified),
		"requires_ansible": nullable(v.RequiresAnsible),
		"marks":            []any{},
	}
}

func (a *API) versionJSON(r *http.Request, repo string, v *model.CollectionVersion) map[string]any {
	out := a.versionSummaryJSON(repo, v)
	download := httpx.BaseURL(r, a.svc.Cfg) + a.prefix() + "/v3/plugin/ansible/content/" + repo +
		"/collections/artifacts/" + v.Artifact.Filename
	manifest := v.Manifest
	if manifest == nil {
		manifest = map[string]any{}
	}
	out["artifact"] = v.Artifact
	out["collection"] = map[string]any{
		"id":   v.Namespace + "." + v.Name,
		"name": v.Name,
		"href": a.indexHref(repo, "collections", v.Namespace, v.Name),
	}
	out["download_url"] = download
	out["name"] = v.Name
	out["namespace"] = map[string]any{"name": v.Namespace, "metadata_sha256": nil}
	out["signatures"] = []any{}
	out["metadata"] = metadataJSON(v.Metadata)
	out["git_url"] = nil
	out["git_commit_sha"] = nil
	out["manifest"] = manifest
	out["files"] = map[string]any{}
	return out
}

func metadataJSON(m model.CollectionMetadata) map[string]any {
	deps := m.Dependencies
	if deps == nil {
		deps = map[string]string{}
	}
	return map[string]any{
		"authors":       orEmpty(m.Authors),
		"contents":      orEmptyAny(m.Contents),
		"dependencies":  deps,
		"description":   m.Description,
		"documentation": m.Documentation,
		"homepage":      m.Homepage,
		"issues":        m.Issues,
		"license":       orEmpty(m.License),
		"repository":    m.Repository,
		"tags":          orEmpty(m.Tags),
		"signatures":    []any{},
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyAny(s []any) []any {
	if s == nil {
		return []any{}
	}
	return s
}

func (a *API) listCollections(w http.ResponseWriter, r *http.Request) {
	ctx, u, repo := r.Context(), auth.UserFrom(r.Context()), a.repo(r)
	cols, err := a.svc.ListCollections(ctx, u, repo)
	if err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	cols = slices.DeleteFunc(cols, func(c *model.Collection) bool {
		if ns := q.Get("namespace"); ns != "" && c.Namespace != ns {
			return true
		}
		if n := q.Get("name"); n != "" && c.Name != n {
			return true
		}
		if d := q.Get("deprecated"); d != "" {
			want, _ := strconv.ParseBool(d)
			return c.Deprecated != want
		}
		return false
	})
	p := httpx.LimitOffset(r, len(cols))
	lo, hi := p.Slice()
	out := make([]map[string]any, 0, hi-lo)
	for _, c := range cols[lo:hi] {
		versions, err := a.svc.Store.Collections().ListVersions(ctx, repo, c.Namespace, c.Name)
		if err != nil {
			httpx.V3Fail(w, r, err)
			return
		}
		out = append(out, a.collectionJSON(repo, c, versions))
	}
	httpx.V3List(w, r, p, out)
}

func (a *API) getCollection(w http.ResponseWriter, r *http.Request) {
	ctx, u, repo := r.Context(), auth.UserFrom(r.Context()), a.repo(r)
	ns, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	c, err := a.svc.GetCollection(ctx, u, repo, ns, name)
	if err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	versions, err := a.svc.Store.Collections().ListVersions(ctx, repo, ns, name)
	if err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a.collectionJSON(repo, c, versions))
}

func (a *API) patchCollection(w http.ResponseWriter, r *http.Request) {
	ctx, u, repo := r.Context(), auth.UserFrom(r.Context()), a.repo(r)
	ns, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	var body struct {
		Deprecated *bool `json:"deprecated"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Deprecated == nil {
		httpx.V3Error(w, http.StatusBadRequest, "invalid", "Invalid input.", `Expected {"deprecated": true|false}.`)
		return
	}
	c, err := a.svc.SetDeprecated(ctx, u, repo, ns, name, *body.Deprecated)
	if err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	versions, _ := a.svc.Store.Collections().ListVersions(ctx, repo, ns, name)
	httpx.JSON(w, http.StatusOK, a.collectionJSON(repo, c, versions))
}

func (a *API) deleteCollection(w http.ResponseWriter, r *http.Request) {
	ns, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if err := a.svc.DeleteCollection(r.Context(), auth.UserFrom(r.Context()), a.repo(r), ns, name); err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	a.deletedTask(w, r, ns, name, "")
}

func (a *API) listVersions(w http.ResponseWriter, r *http.Request) {
	ctx, u, repo := r.Context(), auth.UserFrom(r.Context()), a.repo(r)
	versions, err := a.svc.ListVersions(ctx, u, repo, chi.URLParam(r, "namespace"), chi.URLParam(r, "name"))
	if err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	p := httpx.LimitOffset(r, len(versions))
	lo, hi := p.Slice()
	out := make([]map[string]any, 0, hi-lo)
	for _, v := range versions[lo:hi] {
		out = append(out, a.versionSummaryJSON(repo, v))
	}
	httpx.V3List(w, r, p, out)
}

func (a *API) getVersion(w http.ResponseWriter, r *http.Request) {
	ctx, u, repo := r.Context(), auth.UserFrom(r.Context()), a.repo(r)
	v, err := a.svc.GetVersion(ctx, u, repo, chi.URLParam(r, "namespace"), chi.URLParam(r, "name"), chi.URLParam(r, "version"))
	if err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a.versionJSON(r, repo, v))
}

func (a *API) deleteVersion(w http.ResponseWriter, r *http.Request) {
	ns, name, version := chi.URLParam(r, "namespace"), chi.URLParam(r, "name"), chi.URLParam(r, "version")
	if err := a.svc.DeleteCollectionVersion(r.Context(), auth.UserFrom(r.Context()), a.repo(r), ns, name, version); err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	a.deletedTask(w, r, ns, name, version)
}

func (a *API) download(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	blob, err := a.svc.OpenCollectionArtifact(r.Context(), auth.UserFrom(r.Context()), a.repo(r), filename)
	if err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	defer blob.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	http.ServeContent(w, r, filename, blob.ModTime, blob)
}

// search implements plugin/ansible/search/collection-versions/ across all
// repositories.
func (a *API) search(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.UserFrom(r.Context())
	if err := a.svc.CanRead(ctx, u); err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	repos := a.svc.Cfg.Content.Repositories
	if names := q["repository_name"]; len(names) > 0 {
		repos = slices.DeleteFunc(slices.Clone(repos), func(r string) bool { return !slices.Contains(names, r) })
	}
	keywords := strings.ToLower(q.Get("keywords"))
	if keywords == "" {
		keywords = strings.ToLower(q.Get("q"))
	}

	var out []map[string]any
	for _, repo := range repos {
		cols, err := a.svc.Store.Collections().ListCollections(ctx, repo)
		if err != nil {
			httpx.V3Fail(w, r, err)
			return
		}
		for _, c := range cols {
			if ns := q.Get("namespace"); ns != "" && c.Namespace != ns {
				continue
			}
			if n := q.Get("name"); n != "" && c.Name != n {
				continue
			}
			if d := q.Get("is_deprecated"); d != "" {
				if want, _ := strconv.ParseBool(d); want != c.Deprecated {
					continue
				}
			}
			versions, err := a.svc.Store.Collections().ListVersions(ctx, repo, c.Namespace, c.Name)
			if err != nil {
				httpx.V3Fail(w, r, err)
				return
			}
			highest := service.HighestVersion(versions)
			for _, v := range versions {
				if !matchSearch(q, keywords, v, v == highest) {
					continue
				}
				out = append(out, a.searchItem(repo, c, v, v == highest))
			}
		}
	}
	p := httpx.LimitOffset(r, len(out))
	lo, hi := p.Slice()
	page := out[lo:hi]
	if page == nil {
		page = []map[string]any{}
	}
	httpx.V3List(w, r, p, page)
}

func matchSearch(q map[string][]string, keywords string, v *model.CollectionVersion, highest bool) bool {
	get := func(k string) string {
		if vs := q[k]; len(vs) > 0 {
			return vs[0]
		}
		return ""
	}
	if ver := get("version"); ver != "" && v.Version != ver {
		return false
	}
	if h := get("is_highest"); h != "" {
		if want, _ := strconv.ParseBool(h); want != highest {
			return false
		}
	}
	for _, tag := range q["tags"] {
		if !slices.Contains(v.Metadata.Tags, tag) {
			return false
		}
	}
	if keywords != "" {
		hay := strings.ToLower(v.Namespace + " " + v.Name + " " + v.Metadata.Description + " " + strings.Join(v.Metadata.Tags, " "))
		if !strings.Contains(hay, keywords) {
			return false
		}
	}
	return true
}

func (a *API) searchItem(repo string, c *model.Collection, v *model.CollectionVersion, highest bool) map[string]any {
	tags := make([]map[string]string, 0, len(v.Metadata.Tags))
	for _, t := range v.Metadata.Tags {
		tags = append(tags, map[string]string{"name": t})
	}
	return map[string]any{
		"repository": map[string]any{"name": repo},
		"collection_version": map[string]any{
			"namespace":        v.Namespace,
			"name":             v.Name,
			"version":          v.Version,
			"description":      v.Metadata.Description,
			"tags":             tags,
			"dependencies":     v.Metadata.Dependencies,
			"requires_ansible": nullable(v.RequiresAnsible),
			"contents":         orEmptyAny(v.Metadata.Contents),
			"pulp_created":     httpx.Time(v.Created),
			"pulp_href":        a.indexHref(repo, "collections", v.Namespace, v.Name, "versions", v.Version),
		},
		"repository_version": "latest",
		"namespace_metadata": map[string]any{"name": v.Namespace},
		"is_highest":         highest,
		"is_deprecated":      c.Deprecated,
		"is_signed":          false,
	}
}
