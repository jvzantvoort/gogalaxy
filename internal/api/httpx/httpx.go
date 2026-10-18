// Package httpx contains HTTP helpers shared by the API packages: JSON
// responses, galaxy style errors, pagination and URL construction.
package httpx

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jvzantvoort/gogalaxy/internal/config"
	"github.com/jvzantvoort/gogalaxy/internal/service"
)

// JSON writes v as a JSON response.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
	}
}

// statusFor maps a service error to an HTTP status, error code and title.
func statusFor(err error) (int, string, string) {
	switch service.KindOf(err) {
	case service.KindNotFound:
		return http.StatusNotFound, "not_found", "Not found."
	case service.KindInvalid:
		return http.StatusBadRequest, "invalid", "Invalid input."
	case service.KindConflict:
		return http.StatusBadRequest, "invalid", "Invalid input."
	case service.KindForbidden:
		return http.StatusForbidden, "permission_denied", "You do not have permission to perform this action."
	case service.KindUnauthorized:
		return http.StatusUnauthorized, "not_authenticated", "Authentication credentials were not provided."
	}
	return http.StatusInternalServerError, "server_error", "Internal server error."
}

func detail(err error) string {
	var e *service.Error
	if errors.As(err, &e) {
		return e.Msg
	}
	return ""
}

// V3Error writes an error in the galaxy_ng v3 format.
func V3Error(w http.ResponseWriter, status int, code, title, detail string) {
	e := map[string]string{"status": strconv.Itoa(status), "code": code, "title": title}
	if detail != "" {
		e["detail"] = detail
	}
	JSON(w, status, map[string]any{"errors": []map[string]string{e}})
}

// V3Fail writes err in the v3 format, logging internal errors.
func V3Fail(w http.ResponseWriter, r *http.Request, err error) {
	status, code, title := statusFor(err)
	d := detail(err)
	if status == http.StatusInternalServerError {
		slog.Error("request failed", "path", r.URL.Path, "error", err)
	}
	V3Error(w, status, code, title, d)
}

// V1Fail writes err in the DRF ({"detail": ...}) format used by the v1 API.
func V1Fail(w http.ResponseWriter, r *http.Request, err error) {
	status, _, title := statusFor(err)
	d := detail(err)
	if status == http.StatusInternalServerError {
		slog.Error("request failed", "path", r.URL.Path, "error", err)
	}
	if d == "" {
		d = title
	}
	JSON(w, status, map[string]string{"detail": d})
}

// BaseURL returns scheme://host for absolute links.
func BaseURL(r *http.Request, cfg *config.Config) string {
	if cfg.Server.PublicURL != "" {
		return cfg.Server.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = strings.TrimSpace(strings.Split(p, ",")[0])
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = strings.TrimSpace(strings.Split(h, ",")[0])
	}
	return scheme + "://" + host
}

// Time formats timestamps like pulp does.
func Time(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}

// TimePtr formats an optional timestamp, returning nil when unset.
func TimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return Time(*t)
}

func intParam(q url.Values, key string, def, min, max int) int {
	v, err := strconv.Atoi(q.Get(key))
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if max > 0 && v > max {
		return max
	}
	return v
}

// Page is a window over a result list.
type Page struct {
	Offset, Limit, Count int
}

// Slice returns the bounds of the page for use with s[lo:hi].
func (p Page) Slice() (int, int) {
	lo := min(p.Offset, p.Count)
	return lo, min(lo+p.Limit, p.Count)
}

// LimitOffset reads pulp style ?limit=&offset= pagination parameters.
func LimitOffset(r *http.Request, count int) Page {
	q := r.URL.Query()
	return Page{
		Limit:  intParam(q, "limit", 100, 1, 1000),
		Offset: intParam(q, "offset", 0, 0, 0),
		Count:  count,
	}
}

func withQuery(r *http.Request, set map[string]int) string {
	q := r.URL.Query()
	for k, v := range set {
		q.Set(k, strconv.Itoa(v))
	}
	return r.URL.Path + "?" + q.Encode()
}

// V3List writes a pulp_ansible style paginated response. Links are paths
// relative to the server root, which is what ansible-galaxy expects.
func V3List(w http.ResponseWriter, r *http.Request, p Page, data any) {
	last := 0
	if p.Count > 0 {
		last = ((p.Count - 1) / p.Limit) * p.Limit
	}
	links := map[string]any{
		"first":    withQuery(r, map[string]int{"limit": p.Limit, "offset": 0}),
		"previous": nil,
		"next":     nil,
		"last":     withQuery(r, map[string]int{"limit": p.Limit, "offset": last}),
	}
	if p.Offset > 0 {
		links["previous"] = withQuery(r, map[string]int{"limit": p.Limit, "offset": max(p.Offset-p.Limit, 0)})
	}
	if p.Offset+p.Limit < p.Count {
		links["next"] = withQuery(r, map[string]int{"limit": p.Limit, "offset": p.Offset + p.Limit})
	}
	JSON(w, http.StatusOK, map[string]any{
		"meta":  map[string]int{"count": p.Count},
		"links": links,
		"data":  data,
	})
}

// PageNumber reads DRF style ?page=&page_size= parameters.
func PageNumber(r *http.Request, count, defSize int) (Page, int) {
	q := r.URL.Query()
	size := intParam(q, "page_size", defSize, 1, 1000)
	page := intParam(q, "page", 1, 1, 0)
	return Page{Offset: (page - 1) * size, Limit: size, Count: count}, page
}

// V1List writes a DRF page number paginated response.
func V1List(w http.ResponseWriter, r *http.Request, cfg *config.Config, p Page, page int, results any) {
	var next, nextLink, prev, prevLink any
	if p.Offset+p.Limit < p.Count {
		l := withQuery(r, map[string]int{"page": page + 1})
		next, nextLink = BaseURL(r, cfg)+l, l
	}
	if page > 1 {
		l := withQuery(r, map[string]int{"page": page - 1})
		prev, prevLink = BaseURL(r, cfg)+l, l
	}
	JSON(w, http.StatusOK, map[string]any{
		"count":         p.Count,
		"next":          next,
		"next_link":     nextLink,
		"previous":      prev,
		"previous_link": prevLink,
		"results":       results,
	})
}

// FormFile returns an uploaded file of a parsed multipart form, decoding a
// base64 Content-Transfer-Encoding (ansible-galaxy encodes uploads that way,
// which Go's multipart reader does not undo).
func FormFile(r *http.Request, field string) (io.ReadCloser, string, error) {
	f, hdr, err := r.FormFile(field)
	if err != nil {
		return nil, "", err
	}
	if strings.EqualFold(strings.TrimSpace(hdr.Header.Get("Content-Transfer-Encoding")), "base64") {
		return readCloser{base64.NewDecoder(base64.StdEncoding, f), f}, hdr.Filename, nil
	}
	return f, hdr.Filename, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}
