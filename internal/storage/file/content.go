package file

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/semver"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
)

type collections struct{ s *Store }

func (s *Store) generationPath() string { return filepath.Join(s.root, "repositories", ".generation") }

// bump records that collection data changed by writing a new random token.
// It runs after every collection write, from any process, so readers in other
// processes (the server's index) notice CLI changes. Callers hold s.mu.
func (c collections) bump() {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	if err := writeAtomic(c.s.generationPath(), func(w io.Writer) error {
		_, err := io.WriteString(w, hex.EncodeToString(b))
		return err
	}); err != nil {
		// Without a new token cached readers could serve stale data.
		_ = os.Remove(c.s.generationPath())
	}
}

// CollectionsGeneration implements storage.ChangeTracker.
func (s *Store) CollectionsGeneration(_ context.Context) (string, error) {
	b, err := os.ReadFile(s.generationPath())
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(b), err
}

func (c collections) ListCollections(_ context.Context, repo string) ([]*model.Collection, error) {
	base, err := c.s.path("repositories", repo)
	if err != nil {
		return nil, err
	}
	nss, err := subdirs(base)
	if err != nil {
		return nil, err
	}
	var out []*model.Collection
	for _, ns := range nss {
		names, err := subdirs(filepath.Join(base, ns))
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			var col model.Collection
			if err := readJSON(filepath.Join(base, ns, name, "collection.json"), &col); err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					continue
				}
				return nil, err
			}
			out = append(out, &col)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (c collections) GetCollection(_ context.Context, repo, namespace, name string) (*model.Collection, error) {
	p, err := c.s.path("repositories", repo, namespace, name, "collection.json")
	if err != nil {
		return nil, storage.ErrNotFound
	}
	var col model.Collection
	if err := readJSON(p, &col); err != nil {
		return nil, err
	}
	return &col, nil
}

func (c collections) UpdateCollection(_ context.Context, repo string, col *model.Collection) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	defer c.bump()
	p, err := c.s.path("repositories", repo, col.Namespace, col.Name, "collection.json")
	if err != nil {
		return err
	}
	if !exists(p) {
		return storage.ErrNotFound
	}
	return writeJSON(p, col)
}

func (c collections) ListVersions(_ context.Context, repo, namespace, name string) ([]*model.CollectionVersion, error) {
	p, err := c.s.path("repositories", repo, namespace, name, "versions")
	if err != nil {
		return nil, storage.ErrNotFound
	}
	out, err := listJSON[model.CollectionVersion](p)
	if err != nil {
		return nil, err
	}
	// Highest version first, like pulp_ansible.
	sort.Slice(out, func(i, j int) bool { return semver.Compare(out[i].Version, out[j].Version) > 0 })
	return out, nil
}

func (c collections) GetVersion(_ context.Context, repo, namespace, name, version string) (*model.CollectionVersion, error) {
	p, err := c.s.path("repositories", repo, namespace, name, "versions", version+".json")
	if err != nil {
		return nil, storage.ErrNotFound
	}
	var cv model.CollectionVersion
	if err := readJSON(p, &cv); err != nil {
		return nil, err
	}
	return &cv, nil
}

func (c collections) CreateVersion(_ context.Context, repo string, cv *model.CollectionVersion) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	defer c.bump()
	vp, err := c.s.path("repositories", repo, cv.Namespace, cv.Name, "versions", cv.Version+".json")
	if err != nil {
		return err
	}
	if exists(vp) {
		return storage.ErrExists
	}
	cp := filepath.Join(filepath.Dir(filepath.Dir(vp)), "collection.json")
	var col model.Collection
	err = readJSON(cp, &col)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		col = model.Collection{Namespace: cv.Namespace, Name: cv.Name, Created: cv.Created}
	case err != nil:
		return err
	}
	col.Modified = time.Now().UTC()
	if err := writeJSON(vp, cv); err != nil {
		return err
	}
	return writeJSON(cp, &col)
}

func (c collections) DeleteVersion(_ context.Context, repo, namespace, name, version string) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	defer c.bump()
	vp, err := c.s.path("repositories", repo, namespace, name, "versions", version+".json")
	if err != nil {
		return storage.ErrNotFound
	}
	if err := os.Remove(vp); err != nil {
		if os.IsNotExist(err) {
			return storage.ErrNotFound
		}
		return err
	}
	vdir := filepath.Dir(vp)
	if entries, _ := os.ReadDir(vdir); len(entries) == 0 {
		colDir := filepath.Dir(vdir)
		if err := os.RemoveAll(colDir); err != nil {
			return err
		}
		_ = os.Remove(filepath.Dir(colDir)) // namespace dir, only when empty
	}
	return nil
}

type roles struct{ s *Store }

func (r roles) List(_ context.Context) ([]*model.Role, error) {
	base := filepath.Join(r.s.root, "roles")
	nss, err := subdirs(base)
	if err != nil {
		return nil, err
	}
	var out []*model.Role
	for _, ns := range nss {
		items, err := listJSON[model.Role](filepath.Join(base, ns))
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r roles) Get(_ context.Context, namespace, name string) (*model.Role, error) {
	return r.getLocked(namespace, name)
}

func (r roles) getLocked(namespace, name string) (*model.Role, error) {
	p, err := r.s.path("roles", namespace, name+".json")
	if err != nil {
		return nil, storage.ErrNotFound
	}
	var role model.Role
	if err := readJSON(p, &role); err != nil {
		return nil, err
	}
	return &role, nil
}

func (r roles) indexPath(id int64) string {
	return filepath.Join(r.s.root, "roles", ".index", strconv.FormatInt(id, 10))
}

func (r roles) GetByID(ctx context.Context, id int64) (*model.Role, error) {
	if b, err := os.ReadFile(r.indexPath(id)); err == nil {
		if ns, name, ok := strings.Cut(string(b), "/"); ok {
			if role, err := r.getLocked(ns, name); err == nil && role.ID == id {
				return role, nil
			}
		}
	}
	// Missing or stale index entry (e.g. roles stored before the index
	// existed): scan, then repair the entry so the next lookup is direct.
	all, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, role := range all {
		if role.ID == id {
			r.s.mu.Lock()
			_ = r.writeIndex(role)
			r.s.mu.Unlock()
			return role, nil
		}
	}
	return nil, storage.ErrNotFound
}

func (r roles) writeIndex(role *model.Role) error {
	return writeAtomic(r.indexPath(role.ID), func(w io.Writer) error {
		_, err := io.WriteString(w, role.Namespace+"/"+role.Name)
		return err
	})
}

func (r roles) Save(_ context.Context, role *model.Role) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	p, err := r.s.path("roles", role.Namespace, role.Name+".json")
	if err != nil {
		return err
	}
	if role.ID == 0 {
		if exists(p) {
			return storage.ErrExists
		}
		if role.ID, err = r.s.nextID("role"); err != nil {
			return err
		}
	}
	for i := range role.Versions {
		if role.Versions[i].ID == 0 {
			if role.Versions[i].ID, err = r.s.nextID("role_version"); err != nil {
				return err
			}
		}
	}
	if err := writeJSON(p, role); err != nil {
		return err
	}
	return r.writeIndex(role)
}

func (r roles) Delete(_ context.Context, namespace, name string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	p, err := r.s.path("roles", namespace, name+".json")
	if err != nil {
		return storage.ErrNotFound
	}
	role, _ := r.getLocked(namespace, name)
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return storage.ErrNotFound
		}
		return err
	}
	if role != nil {
		_ = os.Remove(r.indexPath(role.ID))
	}
	_ = os.Remove(filepath.Dir(p)) // namespace dir, only when empty
	return nil
}

func (r roles) AddDownloads(_ context.Context, namespace, name string, n int64) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	role, err := r.getLocked(namespace, name)
	if err != nil {
		return err
	}
	role.DownloadCount += n
	p, _ := r.s.path("roles", namespace, name+".json")
	return writeJSON(p, role)
}

type tasks struct{ s *Store }

func (t tasks) Get(_ context.Context, id string) (*model.Task, error) {
	p, err := t.s.path("tasks", id+".json")
	if err != nil {
		return nil, storage.ErrNotFound
	}
	var task model.Task
	if err := readJSON(p, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func (t tasks) Save(_ context.Context, task *model.Task) error {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	p, err := t.s.path("tasks", task.ID+".json")
	if err != nil {
		return err
	}
	return writeJSON(p, task)
}

type artifacts struct{ s *Store }

func (a artifacts) Put(_ context.Context, kind, filename string, r io.Reader) error {
	p, err := a.s.path("artifacts", kind, filename)
	if err != nil {
		return err
	}
	return writeAtomic(p, func(w io.Writer) error {
		_, err := io.Copy(w, r)
		return err
	})
}

func (a artifacts) Open(_ context.Context, kind, filename string) (*storage.Blob, error) {
	p, err := a.s.path("artifacts", kind, filename)
	if err != nil || strings.HasPrefix(filename, ".tmp-") {
		return nil, storage.ErrNotFound
	}
	f, err := os.Open(p)
	if os.IsNotExist(err) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &storage.Blob{ReadSeekCloser: f, Size: st.Size(), ModTime: st.ModTime()}, nil
}

func (a artifacts) Delete(_ context.Context, kind, filename string) error {
	p, err := a.s.path("artifacts", kind, filename)
	if err != nil {
		return storage.ErrNotFound
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return storage.ErrNotFound
		}
		return err
	}
	return nil
}
