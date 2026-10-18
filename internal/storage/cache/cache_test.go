package cache_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jvzantvoort/gogalaxy/internal/config"
	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
	"github.com/jvzantvoort/gogalaxy/internal/storage/cache"
	"github.com/jvzantvoort/gogalaxy/internal/storage/file"
)

const repo = "published"

// stores returns a cached store and a second, independent file store on the
// same directory, standing in for a CLI process.
func stores(t *testing.T) (cached, other storage.Store, dir string) {
	t.Helper()
	dir = t.TempDir()
	raw, err := file.New(config.StorageConfig{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	cached, ok := cache.Wrap(raw)
	if !ok {
		t.Fatal("file store should support change tracking")
	}
	if other, err = file.New(config.StorageConfig{Path: dir}); err != nil {
		t.Fatal(err)
	}
	return cached, other, dir
}

func version(ns, name, v string) *model.CollectionVersion {
	now := time.Now().UTC()
	return &model.CollectionVersion{Namespace: ns, Name: name, Version: v, Created: now, Modified: now}
}

func create(t *testing.T, s storage.Store, ns, name string, versions ...string) {
	t.Helper()
	for _, v := range versions {
		if err := s.Collections().CreateVersion(context.Background(), repo, version(ns, name, v)); err != nil {
			t.Fatal(err)
		}
	}
}

func names(t *testing.T, s storage.Store) []string {
	t.Helper()
	cols, err := s.Collections().ListCollections(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, c := range cols {
		out = append(out, c.Namespace+"."+c.Name)
	}
	return out
}

func TestSeesWritesFromOtherProcesses(t *testing.T) {
	ctx := context.Background()
	cached, other, _ := stores(t)
	if got := names(t, cached); len(got) != 0 {
		t.Fatalf("expected empty repository, got %v", got)
	}

	create(t, other, "ns", "a", "1.0.0")
	if got := names(t, cached); fmt.Sprint(got) != "[ns.a]" {
		t.Fatalf("create not visible: %v", got)
	}
	if _, err := cached.Collections().GetVersion(ctx, repo, "ns", "a", "1.0.0"); err != nil {
		t.Fatalf("version not visible: %v", err)
	}

	col, _ := other.Collections().GetCollection(ctx, repo, "ns", "a")
	col.Deprecated = true
	if err := other.Collections().UpdateCollection(ctx, repo, col); err != nil {
		t.Fatal(err)
	}
	if c, _ := cached.Collections().GetCollection(ctx, repo, "ns", "a"); !c.Deprecated {
		t.Fatal("update not visible")
	}

	if err := other.Collections().DeleteVersion(ctx, repo, "ns", "a", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.Collections().GetCollection(ctx, repo, "ns", "a"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("delete not visible: %v", err)
	}
}

func TestServesFromMemoryUntilChanged(t *testing.T) {
	cached, other, dir := stores(t)
	create(t, other, "ns", "a", "1.0.0")
	_ = names(t, cached) // build the index

	// Remove the data behind the cache's back, without a change token.
	if err := os.RemoveAll(filepath.Join(dir, "repositories", repo, "ns")); err != nil {
		t.Fatal(err)
	}
	if got := names(t, cached); fmt.Sprint(got) != "[ns.a]" {
		t.Fatalf("expected the indexed copy, got %v", got)
	}

	create(t, other, "ns", "b", "1.0.0") // changes the token
	if got := names(t, cached); fmt.Sprint(got) != "[ns.b]" {
		t.Fatalf("expected a rebuilt index, got %v", got)
	}
}

func TestReturnsCopies(t *testing.T) {
	ctx := context.Background()
	cached, other, _ := stores(t)
	create(t, other, "ns", "a", "1.0.0")

	c, _ := cached.Collections().GetCollection(ctx, repo, "ns", "a")
	c.Deprecated = true
	vs, _ := cached.Collections().ListVersions(ctx, repo, "ns", "a")
	vs[0].Version = "changed"

	if c, _ := cached.Collections().GetCollection(ctx, repo, "ns", "a"); c.Deprecated {
		t.Fatal("collection mutation leaked into the index")
	}
	if vs, _ := cached.Collections().ListVersions(ctx, repo, "ns", "a"); vs[0].Version != "1.0.0" {
		t.Fatal("version mutation leaked into the index")
	}
}

func TestSearch(t *testing.T) {
	cached, other, _ := stores(t)
	create(t, other, "ns", "a", "1.0.0", "1.1.0", "2.0.0-rc.1")
	create(t, other, "ns", "b", "0.1.0-beta.1")

	searcher, ok := cached.Collections().(storage.CollectionSearcher)
	if !ok {
		t.Fatal("cached collections should implement CollectionSearcher")
	}
	hits, err := searcher.Search(context.Background(), repo, func(h storage.SearchHit) bool { return h.Highest })
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, h := range hits {
		got = append(got, h.Collection.Name+"@"+h.Version.Version)
	}
	// Stable releases win over pre-releases; pre-release only when nothing else.
	if fmt.Sprint(got) != "[a@1.1.0 b@0.1.0-beta.1]" {
		t.Fatalf("highest versions = %v", got)
	}
}

func TestConcurrentReadsAndWrites(t *testing.T) {
	ctx := context.Background()
	cached, other, _ := stores(t)
	create(t, other, "ns", "seed", "1.0.0")

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 20 {
				create(t, other, "ns", fmt.Sprintf("w%d", i), fmt.Sprintf("1.%d.0", j))
			}
		}()
	}
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if _, err := cached.Collections().ListCollections(ctx, repo); err != nil {
					t.Error(err)
					return
				}
				if _, err := cached.Collections().GetVersion(ctx, repo, "ns", "seed", "1.0.0"); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := len(names(t, cached)); got != 5 {
		t.Fatalf("collections after concurrent writes = %d, want 5", got)
	}
	vs, _ := cached.Collections().ListVersions(ctx, repo, "ns", "w0")
	if len(vs) != 20 {
		t.Fatalf("versions of ns.w0 = %d, want 20", len(vs))
	}
}
