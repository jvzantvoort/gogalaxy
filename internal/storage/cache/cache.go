// Package cache keeps an in-memory index of collections in front of a
// storage.Store, so that browsing and search do not read every collection
// file on each request.
//
// The index is rebuilt per repository whenever the store's change token
// (storage.ChangeTracker) differs from the one the index was built from. The
// token is read before scanning, so a write that races with a rebuild changes
// the token again and triggers another rebuild on the next read. Reads after a
// write, in this or another process, therefore always see that write.
//
// Values returned by the cache are copies of the indexed structs; nested maps
// and slices (metadata, manifest) are shared and must be treated as read-only.
package cache

import (
	"context"
	"sync"

	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
)

// Wrap returns s with an indexed CollectionStore. Stores that do not
// implement storage.ChangeTracker are returned unchanged and ok is false.
func Wrap(s storage.Store) (wrapped storage.Store, ok bool) {
	tracker, ok := s.(storage.ChangeTracker)
	if !ok {
		return s, false
	}
	return &store{
		Store: s,
		cols:  &collections{inner: s.Collections(), tracker: tracker, repos: map[string]*repoCache{}},
	}, true
}

type store struct {
	storage.Store
	cols *collections
}

func (s *store) Collections() storage.CollectionStore { return s.cols }

type collections struct {
	inner   storage.CollectionStore
	tracker storage.ChangeTracker

	mu    sync.Mutex
	repos map[string]*repoCache
}

// repoCache holds the current index of one repository.
type repoCache struct {
	mu  sync.Mutex
	idx *index
}

// index is an immutable snapshot of a repository.
type index struct {
	gen         string
	collections []*model.Collection // sorted by namespace, name
	entries     map[string]*entry   // "namespace/name"
}

type entry struct {
	collection *model.Collection
	versions   []*model.CollectionVersion // highest first
	byVersion  map[string]*model.CollectionVersion
	highest    *model.CollectionVersion
}

func key(namespace, name string) string { return namespace + "/" + name }

// snapshot returns an up to date index of repo, rebuilding it when needed.
func (c *collections) snapshot(ctx context.Context, repo string) (*index, error) {
	gen, err := c.tracker.CollectionsGeneration(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	rc := c.repos[repo]
	if rc == nil {
		rc = &repoCache{}
		c.repos[repo] = rc
	}
	c.mu.Unlock()

	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.idx != nil && rc.idx.gen == gen {
		return rc.idx, nil
	}
	idx, err := c.build(ctx, repo, gen)
	if err != nil {
		return nil, err
	}
	rc.idx = idx
	return idx, nil
}

func (c *collections) build(ctx context.Context, repo, gen string) (*index, error) {
	cols, err := c.inner.ListCollections(ctx, repo)
	if err != nil {
		return nil, err
	}
	idx := &index{gen: gen, collections: cols, entries: make(map[string]*entry, len(cols))}
	for _, col := range cols {
		versions, err := c.inner.ListVersions(ctx, repo, col.Namespace, col.Name)
		if err != nil {
			return nil, err
		}
		e := &entry{
			collection: col,
			versions:   versions,
			byVersion:  make(map[string]*model.CollectionVersion, len(versions)),
			highest:    model.HighestVersion(versions),
		}
		for _, v := range versions {
			e.byVersion[v.Version] = v
		}
		idx.entries[key(col.Namespace, col.Name)] = e
	}
	return idx, nil
}

func copyCollection(c *model.Collection) *model.Collection {
	cp := *c
	return &cp
}

func copyVersions(in []*model.CollectionVersion) []*model.CollectionVersion {
	out := make([]*model.CollectionVersion, len(in))
	for i, v := range in {
		cp := *v
		out[i] = &cp
	}
	return out
}

func (c *collections) ListCollections(ctx context.Context, repo string) ([]*model.Collection, error) {
	idx, err := c.snapshot(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Collection, len(idx.collections))
	for i, col := range idx.collections {
		out[i] = copyCollection(col)
	}
	return out, nil
}

func (c *collections) GetCollection(ctx context.Context, repo, namespace, name string) (*model.Collection, error) {
	idx, err := c.snapshot(ctx, repo)
	if err != nil {
		return nil, err
	}
	e := idx.entries[key(namespace, name)]
	if e == nil {
		return nil, storage.ErrNotFound
	}
	return copyCollection(e.collection), nil
}

func (c *collections) ListVersions(ctx context.Context, repo, namespace, name string) ([]*model.CollectionVersion, error) {
	idx, err := c.snapshot(ctx, repo)
	if err != nil {
		return nil, err
	}
	e := idx.entries[key(namespace, name)]
	if e == nil {
		return nil, nil
	}
	return copyVersions(e.versions), nil
}

func (c *collections) GetVersion(ctx context.Context, repo, namespace, name, version string) (*model.CollectionVersion, error) {
	idx, err := c.snapshot(ctx, repo)
	if err != nil {
		return nil, err
	}
	if e := idx.entries[key(namespace, name)]; e != nil {
		if v := e.byVersion[version]; v != nil {
			cp := *v
			return &cp, nil
		}
	}
	return nil, storage.ErrNotFound
}

// Writes go straight to the underlying store, which changes the token.

func (c *collections) UpdateCollection(ctx context.Context, repo string, col *model.Collection) error {
	return c.inner.UpdateCollection(ctx, repo, col)
}

func (c *collections) CreateVersion(ctx context.Context, repo string, cv *model.CollectionVersion) error {
	return c.inner.CreateVersion(ctx, repo, cv)
}

func (c *collections) DeleteVersion(ctx context.Context, repo, namespace, name, version string) error {
	return c.inner.DeleteVersion(ctx, repo, namespace, name, version)
}

// Search implements storage.CollectionSearcher.
func (c *collections) Search(ctx context.Context, repo string, match func(storage.SearchHit) bool) ([]storage.SearchHit, error) {
	idx, err := c.snapshot(ctx, repo)
	if err != nil {
		return nil, err
	}
	var hits []storage.SearchHit
	for _, col := range idx.collections {
		e := idx.entries[key(col.Namespace, col.Name)]
		for _, v := range e.versions {
			h := storage.SearchHit{Collection: col, Version: v, Highest: v == e.highest}
			if match(h) {
				cv := *v
				h.Collection, h.Version = copyCollection(col), &cv
				hits = append(hits, h)
			}
		}
	}
	return hits, nil
}
