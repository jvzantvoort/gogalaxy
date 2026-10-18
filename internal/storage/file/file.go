// Package file implements a storage driver that keeps everything as plain
// files below a single directory:
//
//	<path>/users/<username>.json
//	<path>/tokens/<sha256 of token>.json
//	<path>/namespaces/<name>.json
//	<path>/repositories/<repo>/<namespace>/<name>/collection.json
//	<path>/repositories/<repo>/<namespace>/<name>/versions/<version>.json
//	<path>/roles/<namespace>/<name>.json
//	<path>/tasks/<id>.json
//	<path>/artifacts/<kind>/<filename>
//	<path>/sequences.json
//
// Writes are atomic (write to a temporary file, then rename) and serialised
// within the process. Reads always go to disk, so changes made by CLI
// commands are visible to a running server immediately.
package file

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jvzantvoort/gogalaxy/internal/config"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
)

// DriverName is the name the driver registers under.
const DriverName = "file"

func init() { storage.Register(DriverName, New) }

// Store is the file based storage.Store.
type Store struct {
	root string
	mu   sync.RWMutex
}

// New creates (if needed) and opens a file store at cfg.Path.
func New(cfg config.StorageConfig) (storage.Store, error) {
	if cfg.Path == "" {
		return nil, errors.New("file storage: storage.path is required")
	}
	root, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{"users", "tokens", "namespaces", "repositories", "roles", "tasks",
		filepath.Join("artifacts", storage.KindCollection), filepath.Join("artifacts", storage.KindRole)} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			return nil, fmt.Errorf("file storage: %w", err)
		}
	}
	return &Store{root: root}, nil
}

// Root returns the storage directory.
func (s *Store) Root() string { return s.root }

func (s *Store) Users() storage.UserStore             { return users{s} }
func (s *Store) Tokens() storage.TokenStore           { return tokens{s} }
func (s *Store) Namespaces() storage.NamespaceStore   { return namespaces{s} }
func (s *Store) Collections() storage.CollectionStore { return collections{s} }
func (s *Store) Roles() storage.RoleStore             { return roles{s} }
func (s *Store) Tasks() storage.TaskStore             { return tasks{s} }
func (s *Store) Artifacts() storage.ArtifactStore     { return artifacts{s} }
func (s *Store) Close() error                         { return nil }

// path joins root with validated path components.
func (s *Store) path(parts ...string) (string, error) {
	for _, p := range parts {
		if err := checkComponent(p); err != nil {
			return "", err
		}
	}
	return filepath.Join(append([]string{s.root}, parts...)...), nil
}

func checkComponent(p string) error {
	if p == "" || p == "." || p == ".." || strings.ContainsAny(p, `/\`+"\x00") || strings.HasPrefix(p, ".") {
		return fmt.Errorf("invalid name %q", p)
	}
	return nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return storage.ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, func(w io.Writer) error {
		_, err := w.Write(append(b, '\n'))
		return err
	})
}

func writeAtomic(path string, fill func(io.Writer) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := fill(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// listJSON decodes every *.json file in dir.
func listJSON[T any](dir string) ([]*T, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*T
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		v := new(T)
		if err := readJSON(filepath.Join(dir, e.Name()), v); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out = append(out, v)
	}
	return out, nil
}

// subdirs lists the visible sub directories of dir.
func subdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// nextID returns the next value of the named sequence. Callers must hold s.mu.
func (s *Store) nextID(name string) (int64, error) {
	path := filepath.Join(s.root, "sequences.json")
	seq := map[string]int64{}
	if err := readJSON(path, &seq); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return 0, err
	}
	seq[name]++
	if err := writeJSON(path, seq); err != nil {
		return 0, err
	}
	return seq[name], nil
}
