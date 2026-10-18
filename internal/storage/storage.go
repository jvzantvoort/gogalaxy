// Package storage defines the persistence interfaces used by gogalaxy and a
// registry of drivers implementing them.
//
// A driver registers itself from an init function:
//
//	func init() { storage.Register("file", New) }
//
// and is selected through the storage.driver configuration key.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/jvzantvoort/gogalaxy/internal/config"
	"github.com/jvzantvoort/gogalaxy/internal/model"
)

var (
	// ErrNotFound is returned when a requested object does not exist.
	ErrNotFound = errors.New("not found")
	// ErrExists is returned when creating an object that already exists.
	ErrExists = errors.New("already exists")
)

// Store gives access to all persisted data.
type Store interface {
	Users() UserStore
	Tokens() TokenStore
	Namespaces() NamespaceStore
	Collections() CollectionStore
	Roles() RoleStore
	Tasks() TaskStore
	Artifacts() ArtifactStore
	Close() error
}

// UserStore persists user accounts.
type UserStore interface {
	Get(ctx context.Context, username string) (*model.User, error)
	List(ctx context.Context) ([]*model.User, error)
	// Create stores a new user and assigns its ID.
	Create(ctx context.Context, u *model.User) error
	Update(ctx context.Context, u *model.User) error
	Delete(ctx context.Context, username string) error
}

// TokenStore persists API tokens. Implementations must only keep a hash of
// the token, never the token itself.
type TokenStore interface {
	// Lookup returns the username owning the token.
	Lookup(ctx context.Context, token string) (string, error)
	// Set replaces any existing token of the user.
	Set(ctx context.Context, username, token string) error
	// Revoke removes the token of the user, if any.
	Revoke(ctx context.Context, username string) error
}

// NamespaceStore persists namespaces.
type NamespaceStore interface {
	Get(ctx context.Context, name string) (*model.Namespace, error)
	List(ctx context.Context) ([]*model.Namespace, error)
	// Create stores a new namespace and assigns its ID.
	Create(ctx context.Context, ns *model.Namespace) error
	Update(ctx context.Context, ns *model.Namespace) error
	Delete(ctx context.Context, name string) error
}

// CollectionStore persists collections per repository.
type CollectionStore interface {
	ListCollections(ctx context.Context, repo string) ([]*model.Collection, error)
	GetCollection(ctx context.Context, repo, namespace, name string) (*model.Collection, error)
	UpdateCollection(ctx context.Context, repo string, c *model.Collection) error
	ListVersions(ctx context.Context, repo, namespace, name string) ([]*model.CollectionVersion, error)
	GetVersion(ctx context.Context, repo, namespace, name, version string) (*model.CollectionVersion, error)
	// CreateVersion stores a new version, creating the collection when needed.
	CreateVersion(ctx context.Context, repo string, cv *model.CollectionVersion) error
	// DeleteVersion removes a version and the collection once it has no versions left.
	DeleteVersion(ctx context.Context, repo, namespace, name, version string) error
}

// RoleStore persists legacy roles.
type RoleStore interface {
	List(ctx context.Context) ([]*model.Role, error)
	Get(ctx context.Context, namespace, name string) (*model.Role, error)
	GetByID(ctx context.Context, id int64) (*model.Role, error)
	// Save creates or updates a role, assigning IDs to the role and to new versions.
	Save(ctx context.Context, r *model.Role) error
	Delete(ctx context.Context, namespace, name string) error
	IncrementDownloads(ctx context.Context, namespace, name string) error
}

// TaskStore persists import tasks.
type TaskStore interface {
	Get(ctx context.Context, id string) (*model.Task, error)
	Save(ctx context.Context, t *model.Task) error
}

// Artifact kinds.
const (
	KindCollection = "collections"
	KindRole       = "roles"
)

// Blob is an opened artifact.
type Blob struct {
	io.ReadSeekCloser
	Size    int64
	ModTime time.Time
}

// ArtifactStore persists archive files.
type ArtifactStore interface {
	Put(ctx context.Context, kind, filename string, r io.Reader) error
	Open(ctx context.Context, kind, filename string) (*Blob, error)
	Delete(ctx context.Context, kind, filename string) error
}

// Factory creates a Store from configuration.
type Factory func(cfg config.StorageConfig) (Store, error)

var (
	mu      sync.RWMutex
	drivers = map[string]Factory{}
)

// Register makes a storage driver available by name.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := drivers[name]; dup {
		panic("storage: driver registered twice: " + name)
	}
	drivers[name] = f
}

// Drivers lists the registered driver names.
func Drivers() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(drivers))
	for n := range drivers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Open creates the store selected by cfg.Driver.
func Open(cfg config.StorageConfig) (Store, error) {
	mu.RLock()
	f, ok := drivers[cfg.Driver]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown storage driver %q (available: %v)", cfg.Driver, Drivers())
	}
	return f(cfg)
}
