// Package v3 implements the galaxy v3 API: collections, namespaces, imports
// and API tokens.
package v3

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jvzantvoort/gogalaxy/internal/api/httpx"
	"github.com/jvzantvoort/gogalaxy/internal/auth"
	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/service"
)

// API serves the v3 endpoints.
type API struct {
	svc *service.Service
}

// New creates the v3 API.
func New(svc *service.Service) *API { return &API{svc: svc} }

// Routes registers the v3 endpoints on r. It is mounted both at
// <prefix>/v3/ (default repository) and at <prefix>/content/{repo}/v3/.
func (a *API) Routes(r chi.Router) {
	r.Post("/auth/token/", a.createToken)
	r.Delete("/auth/token/", a.deleteToken)

	r.Get("/namespaces/", a.listNamespaces)
	r.Get("/namespaces/{namespace}/", a.getNamespace)

	r.Post("/artifacts/collections/", a.upload)
	r.Get("/imports/collections/{id}/", a.getImport)
	r.Get("/tasks/{id}/", a.getImport)

	// Unpaginated listings are disabled in galaxy_ng as well.
	r.Get("/collections/all/", a.notFound)
	r.Get("/collection_versions/all/", a.notFound)

	collectionRoutes(r, a)
	r.Route("/plugin/ansible/content/{distro}/collections", func(r chi.Router) {
		r.Route("/index", func(r chi.Router) { collectionRoutes(r, a) })
		r.Get("/artifacts/{filename}", a.download)
		r.Post("/artifacts/", a.upload)
	})
	r.Get("/plugin/ansible/search/collection-versions/", a.search)
}

func collectionRoutes(r chi.Router, a *API) {
	r.Get("/collections/", a.listCollections)
	r.Get("/collections/{namespace}/{name}/", a.getCollection)
	r.Patch("/collections/{namespace}/{name}/", a.patchCollection)
	r.Delete("/collections/{namespace}/{name}/", a.deleteCollection)
	r.Get("/collections/{namespace}/{name}/versions/", a.listVersions)
	r.Get("/collections/{namespace}/{name}/versions/{version}/", a.getVersion)
	r.Delete("/collections/{namespace}/{name}/versions/{version}/", a.deleteVersion)
}

func (a *API) notFound(w http.ResponseWriter, r *http.Request) {
	httpx.V3Error(w, http.StatusNotFound, "not_found", "Not found.", "")
}

// repo returns the repository addressed by the request.
func (a *API) repo(r *http.Request) string {
	if d := chi.URLParam(r, "distro"); d != "" {
		return d
	}
	if d := chi.URLParam(r, "repo"); d != "" {
		return d
	}
	return a.svc.Cfg.Content.DefaultRepository
}

func (a *API) prefix() string { return a.svc.Cfg.API.Prefix }

func (a *API) indexHref(repo string, parts ...string) string {
	p := fmt.Sprintf("%s/v3/plugin/ansible/content/%s/collections/index/", a.prefix(), repo)
	if len(parts) > 0 {
		p += strings.Join(parts, "/") + "/"
	}
	return p
}

func (a *API) taskHref(id string) string {
	return fmt.Sprintf("%s/v3/imports/collections/%s/", a.prefix(), id)
}

// --- tokens -----------------------------------------------------------------

func (a *API) createToken(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		httpx.V3Error(w, http.StatusUnauthorized, "not_authenticated", "Authentication credentials were not provided.", "")
		return
	}
	token, err := a.svc.IssueToken(r.Context(), u.Username)
	if err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"token": token})
}

func (a *API) deleteToken(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		httpx.V3Error(w, http.StatusUnauthorized, "not_authenticated", "Authentication credentials were not provided.", "")
		return
	}
	if err := a.svc.RevokeToken(r.Context(), u.Username); err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- namespaces -------------------------------------------------------------

func (a *API) namespaceJSON(ns *model.Namespace) map[string]any {
	users := make([]map[string]string, 0, len(ns.Owners))
	for _, o := range ns.Owners {
		users = append(users, map[string]string{"name": o})
	}
	return map[string]any{
		"id":              ns.ID,
		"name":            ns.Name,
		"company":         ns.Company,
		"email":           ns.Email,
		"avatar_url":      ns.AvatarURL,
		"description":     ns.Description,
		"links":           []any{},
		"groups":          []any{},
		"users":           users,
		"resources":       "",
		"related_fields":  map[string]any{},
		"metadata_sha256": nil,
		"avatar_sha256":   nil,
	}
}

func (a *API) listNamespaces(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if err := a.svc.CanRead(r.Context(), u); err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	all, err := a.svc.ListNamespaces(r.Context())
	if err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	if q := r.URL.Query().Get("name"); q != "" {
		all = slices.DeleteFunc(all, func(n *model.Namespace) bool { return n.Name != q })
	}
	if q := strings.ToLower(r.URL.Query().Get("keywords")); q != "" {
		all = slices.DeleteFunc(all, func(n *model.Namespace) bool { return !strings.Contains(n.Name, q) })
	}
	p := httpx.LimitOffset(r, len(all))
	lo, hi := p.Slice()
	out := make([]map[string]any, 0, hi-lo)
	for _, ns := range all[lo:hi] {
		out = append(out, a.namespaceJSON(ns))
	}
	httpx.V3List(w, r, p, out)
}

func (a *API) getNamespace(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.CanRead(r.Context(), auth.UserFrom(r.Context())); err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	ns, err := a.svc.GetNamespace(r.Context(), chi.URLParam(r, "namespace"))
	if err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a.namespaceJSON(ns))
}

// --- imports ----------------------------------------------------------------

func (a *API) upload(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		httpx.V3Error(w, http.StatusUnauthorized, "not_authenticated", "Authentication credentials were not provided.", "")
		return
	}
	repo := a.svc.Cfg.Content.UploadRepository
	if d := chi.URLParam(r, "distro"); d != "" {
		repo = d
	} else if d := chi.URLParam(r, "repo"); d != "" {
		repo = d
	}

	if limit := a.svc.Cfg.Content.MaxUploadSize; limit > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	}
	// The body is bounded by MaxBytesReader above; parts beyond 32MiB spill to disk.
	if err := r.ParseMultipartForm(32 << 20); err != nil { //nolint:gosec // see above
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpx.V3Error(w, http.StatusRequestEntityTooLarge, "invalid", "Invalid input.", "Artifact exceeds the maximum upload size.")
			return
		}
		httpx.V3Error(w, http.StatusBadRequest, "invalid", "Invalid input.", "Expected a multipart/form-data upload.")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	file, filename, err := httpx.FormFile(r, "file")
	if err != nil {
		httpx.V3Error(w, http.StatusBadRequest, "invalid", "Invalid input.", "No file was submitted.")
		return
	}
	defer file.Close()

	task, err := a.svc.PublishCollection(r.Context(), u, service.PublishRequest{
		Repository: repo,
		Filename:   filename,
		SHA256:     r.FormValue("sha256"),
		Body:       file,
	})
	if err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"task": a.taskHref(task.ID)})
}

func (a *API) getImport(w http.ResponseWriter, r *http.Request) {
	if auth.UserFrom(r.Context()) == nil {
		httpx.V3Error(w, http.StatusUnauthorized, "not_authenticated", "Authentication credentials were not provided.", "")
		return
	}
	t, err := a.svc.GetTask(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, taskJSON(t, a.taskHref(t.ID)))
}

func taskJSON(t *model.Task, href string) map[string]any {
	msgs := make([]map[string]string, 0, len(t.Messages))
	for _, m := range t.Messages {
		msgs = append(msgs, map[string]string{"message": m.Message, "level": m.Level, "time": httpx.Time(m.Time)})
	}
	var taskErr any
	if t.Error != nil {
		taskErr = map[string]string{"code": t.Error.Code, "description": t.Error.Description, "traceback": ""}
	}
	updated := t.Created
	if t.Finished != nil {
		updated = *t.Finished
	}
	return map[string]any{
		"id":          t.ID,
		"href":        href,
		"state":       t.State,
		"created_at":  httpx.Time(t.Created),
		"updated_at":  httpx.Time(updated),
		"started_at":  httpx.TimePtr(t.Started),
		"finished_at": httpx.TimePtr(t.Finished),
		"error":       taskErr,
		"messages":    msgs,
		"namespace":   t.Namespace,
		"name":        t.Name,
		"version":     t.Version,
	}
}

// deletedTask records a completed delete task, matching galaxy_ng which runs
// deletions asynchronously and answers with a task reference.
func (a *API) deletedTask(w http.ResponseWriter, r *http.Request, ns, name, version string) {
	now := time.Now().UTC()
	t := &model.Task{
		ID: service.NewTaskID(), Kind: "collection-delete", State: model.TaskCompleted,
		Repository: a.repo(r), Namespace: ns, Name: name, Version: version,
		Messages: []model.TaskMessage{}, Created: now, Started: &now, Finished: &now,
	}
	if u := auth.UserFrom(r.Context()); u != nil {
		t.Username = u.Username
	}
	if err := a.svc.Store.Tasks().Save(r.Context(), t); err != nil {
		httpx.V3Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"task": a.taskHref(t.ID)})
}
