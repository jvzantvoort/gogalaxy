package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jvzantvoort/gogalaxy/internal/importer"
	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
)

func (s *Service) checkRepo(repo string) error {
	if !s.Cfg.HasRepository(repo) {
		return newErr(KindNotFound, "Repository %q not found.", repo)
	}
	return nil
}

// spool copies r to a temporary file, enforcing the upload size limit, and
// returns the file path, size and sha256.
func (s *Service) spool(r io.Reader) (string, int64, string, error) {
	tmp, err := os.CreateTemp("", "gogalaxy-upload-*.tar.gz")
	if err != nil {
		return "", 0, "", err
	}
	defer tmp.Close()
	h := sha256.New()
	limit := s.Cfg.Content.MaxUploadSize
	src := r
	if limit > 0 {
		src = io.LimitReader(r, limit+1)
	}
	n, err := io.Copy(io.MultiWriter(tmp, h), src)
	if err == nil && limit > 0 && n > limit {
		err = newErr(KindInvalid, "Artifact exceeds the maximum upload size of %d bytes.", limit)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", 0, "", err
	}
	return tmp.Name(), n, hex.EncodeToString(h.Sum(nil)), nil
}

func NewTaskID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// PublishRequest describes a collection upload.
type PublishRequest struct {
	Repository string
	Filename   string
	// SHA256 is the checksum announced by the client, verified when set.
	SHA256 string
	Body   io.Reader
}

// PublishCollection validates and stores a collection artifact. Import
// problems found in the archive are recorded on the returned (failed) task,
// request level problems are returned as errors.
func (s *Service) PublishCollection(ctx context.Context, u *model.User, req PublishRequest) (*model.Task, error) {
	if u == nil {
		return nil, newErr(KindUnauthorized, "Authentication credentials were not provided.")
	}
	if err := s.checkRepo(req.Repository); err != nil {
		return nil, err
	}
	fn, err := importer.ParseCollectionFilename(req.Filename)
	if err != nil {
		return nil, newErr(KindInvalid, "%s", err)
	}

	path, size, sum, err := s.spool(req.Body)
	if err != nil {
		return nil, err
	}
	defer os.Remove(path)
	if req.SHA256 != "" && !strings.EqualFold(req.SHA256, sum) {
		return nil, newErr(KindInvalid, "The provided sha256 checksum does not match the uploaded file.")
	}

	ns, created, err := s.namespaceForUpload(ctx, u, fn.Namespace)
	if err != nil {
		return nil, err
	}
	if err := s.requirePublish(u, ns); err != nil {
		return nil, err
	}
	if _, err := s.Store.Collections().GetVersion(ctx, req.Repository, fn.Namespace, fn.Name, fn.Version); err == nil {
		return nil, newErr(KindConflict, "Artifact \"%s\" already exists in repository %q.", req.Filename, req.Repository)
	}
	if err := s.checkArtifactReuse(ctx, req.Filename, sum); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	task := &model.Task{
		ID: NewTaskID(), Kind: "collection-import", State: model.TaskRunning,
		Repository: req.Repository, Namespace: fn.Namespace, Name: fn.Name, Version: fn.Version,
		Username: u.Username, Messages: []model.TaskMessage{}, Created: now, Started: &now,
	}
	if created {
		task.Log("INFO", fmt.Sprintf("Created namespace %s", ns.Name))
	}
	task.Log("INFO", fmt.Sprintf("Importing collection %s from %s", fn.Namespace+"."+fn.Name, req.Filename))

	if err := s.importCollection(ctx, u, req, fn, path, size, sum, task); err != nil {
		if KindOf(err) == KindInternal {
			return nil, err
		}
		task.State = model.TaskFailed
		task.Error = &model.TaskError{Code: "import_error", Description: err.Error()}
		task.Log("ERROR", err.Error())
	} else {
		task.State = model.TaskCompleted
		task.Log("INFO", fmt.Sprintf("Collection %s.%s %s published to %s", fn.Namespace, fn.Name, fn.Version, req.Repository))
	}
	end := time.Now().UTC()
	task.Finished = &end
	if err := s.Store.Tasks().Save(ctx, task); err != nil {
		return nil, err
	}
	s.Log.Info("collection import", "task", task.ID, "state", task.State,
		"collection", fn.Namespace+"."+fn.Name, "version", fn.Version, "user", u.Username)
	return task, nil
}

func (s *Service) importCollection(ctx context.Context, u *model.User, req PublishRequest,
	fn importer.CollectionFilename, path string, size int64, sum string, task *model.Task) error {
	info, err := importer.InspectCollection(path)
	if err != nil {
		return newErr(KindInvalid, "%s", err)
	}
	if info.Namespace != fn.Namespace || info.Name != fn.Name || info.Version != fn.Version {
		return newErr(KindInvalid, "MANIFEST.json (%s.%s %s) does not match the filename %s",
			info.Namespace, info.Name, info.Version, req.Filename)
	}
	for dep := range info.Metadata.Dependencies {
		task.Log("INFO", "Dependency: "+dep+" "+info.Metadata.Dependencies[dep])
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := s.Store.Artifacts().Put(ctx, storage.KindCollection, req.Filename, f); err != nil {
		return err
	}
	now := time.Now().UTC()
	cv := &model.CollectionVersion{
		Namespace:       info.Namespace,
		Name:            info.Name,
		Version:         info.Version,
		Artifact:        model.Artifact{Filename: req.Filename, SHA256: sum, Size: size},
		Metadata:        info.Metadata,
		Manifest:        info.Manifest,
		RequiresAnsible: info.RequiresAnsible,
		Uploader:        u.Username,
		Created:         now,
		Modified:        now,
	}
	if err := s.Store.Collections().CreateVersion(ctx, req.Repository, cv); err != nil {
		if errors.Is(err, storage.ErrExists) {
			return newErr(KindConflict, "Artifact \"%s\" already exists.", req.Filename)
		}
		return err
	}
	return nil
}

// checkArtifactReuse rejects an upload whose filename is already stored (in
// another repository) with different content.
func (s *Service) checkArtifactReuse(ctx context.Context, filename, sum string) error {
	for _, repo := range s.Cfg.Content.Repositories {
		fn, _ := importer.ParseCollectionFilename(filename)
		cv, err := s.Store.Collections().GetVersion(ctx, repo, fn.Namespace, fn.Name, fn.Version)
		if err == nil && cv.Artifact.SHA256 != sum {
			return newErr(KindConflict, "Artifact \"%s\" already exists with different content in repository %q.", filename, repo)
		}
	}
	return nil
}

// ListCollections returns the collections of a repository.
func (s *Service) ListCollections(ctx context.Context, u *model.User, repo string) ([]*model.Collection, error) {
	if err := s.requireRead(u); err != nil {
		return nil, err
	}
	if err := s.checkRepo(repo); err != nil {
		return nil, err
	}
	return s.Store.Collections().ListCollections(ctx, repo)
}

// GetCollection returns a collection.
func (s *Service) GetCollection(ctx context.Context, u *model.User, repo, namespace, name string) (*model.Collection, error) {
	if err := s.requireRead(u); err != nil {
		return nil, err
	}
	if err := s.checkRepo(repo); err != nil {
		return nil, err
	}
	c, err := s.Store.Collections().GetCollection(ctx, repo, namespace, name)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, newErr(KindNotFound, "Collection %s.%s not found.", namespace, name)
	}
	return c, err
}

// ListVersions returns the versions of a collection, highest first.
func (s *Service) ListVersions(ctx context.Context, u *model.User, repo, namespace, name string) ([]*model.CollectionVersion, error) {
	if _, err := s.GetCollection(ctx, u, repo, namespace, name); err != nil {
		return nil, err
	}
	return s.Store.Collections().ListVersions(ctx, repo, namespace, name)
}

// GetVersion returns a collection version.
func (s *Service) GetVersion(ctx context.Context, u *model.User, repo, namespace, name, version string) (*model.CollectionVersion, error) {
	if err := s.requireRead(u); err != nil {
		return nil, err
	}
	if err := s.checkRepo(repo); err != nil {
		return nil, err
	}
	cv, err := s.Store.Collections().GetVersion(ctx, repo, namespace, name, version)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, newErr(KindNotFound, "Collection version %s.%s %s not found.", namespace, name, version)
	}
	return cv, err
}

// HighestVersion returns the highest stable version, or the highest
// pre-release when no stable version exists. versions must be sorted
// highest first.
func HighestVersion(versions []*model.CollectionVersion) *model.CollectionVersion {
	return model.HighestVersion(versions)
}

// SearchVersions returns the versions in repos matching match. It uses the
// store's CollectionSearcher when available and scans otherwise.
func (s *Service) SearchVersions(ctx context.Context, u *model.User, repos []string,
	match func(repo string, hit storage.SearchHit) bool) (map[string][]storage.SearchHit, error) {
	if err := s.requireRead(u); err != nil {
		return nil, err
	}
	cs := s.Store.Collections()
	out := map[string][]storage.SearchHit{}
	for _, repo := range repos {
		if err := s.checkRepo(repo); err != nil {
			return nil, err
		}
		pred := func(h storage.SearchHit) bool { return match(repo, h) }
		if searcher, ok := cs.(storage.CollectionSearcher); ok {
			hits, err := searcher.Search(ctx, repo, pred)
			if err != nil {
				return nil, err
			}
			out[repo] = hits
			continue
		}
		cols, err := cs.ListCollections(ctx, repo)
		if err != nil {
			return nil, err
		}
		for _, c := range cols {
			versions, err := cs.ListVersions(ctx, repo, c.Namespace, c.Name)
			if err != nil {
				return nil, err
			}
			highest := model.HighestVersion(versions)
			for _, v := range versions {
				if h := (storage.SearchHit{Collection: c, Version: v, Highest: v == highest}); pred(h) {
					out[repo] = append(out[repo], h)
				}
			}
		}
	}
	return out, nil
}

// SetDeprecated marks a collection as (not) deprecated.
func (s *Service) SetDeprecated(ctx context.Context, u *model.User, repo, namespace, name string, deprecated bool) (*model.Collection, error) {
	c, err := s.GetCollection(ctx, u, repo, namespace, name)
	if err != nil {
		return nil, err
	}
	ns, err := s.GetNamespace(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if err := s.requirePublish(u, ns); err != nil {
		return nil, err
	}
	c.Deprecated = deprecated
	c.Modified = time.Now().UTC()
	return c, s.Store.Collections().UpdateCollection(ctx, repo, c)
}

// DeleteCollectionVersion removes a version from a repository. The artifact
// is removed once no repository references it.
func (s *Service) DeleteCollectionVersion(ctx context.Context, u *model.User, repo, namespace, name, version string) error {
	cv, err := s.GetVersion(ctx, u, repo, namespace, name, version)
	if err != nil {
		return err
	}
	ns, err := s.Store.Namespaces().Get(ctx, namespace)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	if ns == nil {
		ns = &model.Namespace{Name: namespace}
	}
	if err := s.requirePublish(u, ns); err != nil {
		return err
	}
	if err := s.Store.Collections().DeleteVersion(ctx, repo, namespace, name, version); err != nil {
		return err
	}
	for _, r := range s.Cfg.Content.Repositories {
		if _, err := s.Store.Collections().GetVersion(ctx, r, namespace, name, version); err == nil {
			return nil
		}
	}
	if err := s.Store.Artifacts().Delete(ctx, storage.KindCollection, cv.Artifact.Filename); err != nil &&
		!errors.Is(err, storage.ErrNotFound) {
		return err
	}
	return nil
}

// DeleteCollection removes all versions of a collection from a repository.
func (s *Service) DeleteCollection(ctx context.Context, u *model.User, repo, namespace, name string) error {
	versions, err := s.ListVersions(ctx, u, repo, namespace, name)
	if err != nil {
		return err
	}
	for _, v := range versions {
		if err := s.DeleteCollectionVersion(ctx, u, repo, namespace, name, v.Version); err != nil {
			return err
		}
	}
	return nil
}

// OpenCollectionArtifact opens the artifact filename if it belongs to repo.
func (s *Service) OpenCollectionArtifact(ctx context.Context, u *model.User, repo, filename string) (*storage.Blob, error) {
	fn, err := importer.ParseCollectionFilename(filename)
	if err != nil {
		return nil, newErr(KindNotFound, "Not found.")
	}
	cv, err := s.GetVersion(ctx, u, repo, fn.Namespace, fn.Name, fn.Version)
	if err != nil {
		return nil, err
	}
	b, err := s.Store.Artifacts().Open(ctx, storage.KindCollection, cv.Artifact.Filename)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, newErr(KindNotFound, "Artifact %s is missing from storage.", filename)
	}
	return b, err
}
