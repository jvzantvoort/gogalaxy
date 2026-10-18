package file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
)

type users struct{ s *Store }

func (u users) Get(_ context.Context, username string) (*model.User, error) {
	u.s.mu.RLock()
	defer u.s.mu.RUnlock()
	p, err := u.s.path("users", username+".json")
	if err != nil {
		return nil, storage.ErrNotFound
	}
	var out model.User
	if err := readJSON(p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (u users) List(_ context.Context) ([]*model.User, error) {
	u.s.mu.RLock()
	defer u.s.mu.RUnlock()
	out, err := listJSON[model.User](filepath.Join(u.s.root, "users"))
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}

func (u users) Create(_ context.Context, user *model.User) error {
	u.s.mu.Lock()
	defer u.s.mu.Unlock()
	p, err := u.s.path("users", user.Username+".json")
	if err != nil {
		return err
	}
	if exists(p) {
		return storage.ErrExists
	}
	if user.ID, err = u.s.nextID("user"); err != nil {
		return err
	}
	return writeJSON(p, user)
}

func (u users) Update(_ context.Context, user *model.User) error {
	u.s.mu.Lock()
	defer u.s.mu.Unlock()
	p, err := u.s.path("users", user.Username+".json")
	if err != nil {
		return err
	}
	if !exists(p) {
		return storage.ErrNotFound
	}
	return writeJSON(p, user)
}

func (u users) Delete(_ context.Context, username string) error {
	u.s.mu.Lock()
	defer u.s.mu.Unlock()
	p, err := u.s.path("users", username+".json")
	if err != nil {
		return storage.ErrNotFound
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return storage.ErrNotFound
		}
		return err
	}
	return revokeLocked(u.s, username)
}

type tokenRecord struct {
	Username string    `json:"username"`
	Created  time.Time `json:"created"`
}

type tokens struct{ s *Store }

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (t tokens) Lookup(_ context.Context, token string) (string, error) {
	t.s.mu.RLock()
	defer t.s.mu.RUnlock()
	var rec tokenRecord
	if err := readJSON(filepath.Join(t.s.root, "tokens", hashToken(token)+".json"), &rec); err != nil {
		return "", err
	}
	return rec.Username, nil
}

func (t tokens) Set(_ context.Context, username, token string) error {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	if err := revokeLocked(t.s, username); err != nil {
		return err
	}
	return writeJSON(filepath.Join(t.s.root, "tokens", hashToken(token)+".json"),
		tokenRecord{Username: username, Created: time.Now().UTC()})
}

func (t tokens) Revoke(_ context.Context, username string) error {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	return revokeLocked(t.s, username)
}

func revokeLocked(s *Store, username string) error {
	dir := filepath.Join(s.root, "tokens")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		var rec tokenRecord
		p := filepath.Join(dir, e.Name())
		if err := readJSON(p, &rec); err != nil {
			continue
		}
		if rec.Username == username {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

type namespaces struct{ s *Store }

func (n namespaces) Get(_ context.Context, name string) (*model.Namespace, error) {
	n.s.mu.RLock()
	defer n.s.mu.RUnlock()
	p, err := n.s.path("namespaces", name+".json")
	if err != nil {
		return nil, storage.ErrNotFound
	}
	var out model.Namespace
	if err := readJSON(p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (n namespaces) List(_ context.Context) ([]*model.Namespace, error) {
	n.s.mu.RLock()
	defer n.s.mu.RUnlock()
	out, err := listJSON[model.Namespace](filepath.Join(n.s.root, "namespaces"))
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

func (n namespaces) Create(_ context.Context, ns *model.Namespace) error {
	n.s.mu.Lock()
	defer n.s.mu.Unlock()
	p, err := n.s.path("namespaces", ns.Name+".json")
	if err != nil {
		return err
	}
	if exists(p) {
		return storage.ErrExists
	}
	if ns.ID, err = n.s.nextID("namespace"); err != nil {
		return err
	}
	return writeJSON(p, ns)
}

func (n namespaces) Update(_ context.Context, ns *model.Namespace) error {
	n.s.mu.Lock()
	defer n.s.mu.Unlock()
	p, err := n.s.path("namespaces", ns.Name+".json")
	if err != nil {
		return err
	}
	if !exists(p) {
		return storage.ErrNotFound
	}
	return writeJSON(p, ns)
}

func (n namespaces) Delete(_ context.Context, name string) error {
	n.s.mu.Lock()
	defer n.s.mu.Unlock()
	p, err := n.s.path("namespaces", name+".json")
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
