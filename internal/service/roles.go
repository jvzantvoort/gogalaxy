package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jvzantvoort/gogalaxy/internal/importer"
	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/semver"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
)

var roleVersionRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,127}$`)

// RoleImport describes a role archive upload.
type RoleImport struct {
	// Namespace and Name default to galaxy_info.namespace and
	// galaxy_info.role_name from meta/main.yml.
	Namespace string
	Name      string
	Version   string
	// GithubUser and GithubRepo are informational only.
	GithubUser string
	GithubRepo string
	Body       io.Reader
}

// RoleArtifactName returns the stored filename of a role version.
func RoleArtifactName(namespace, name, version string) string {
	return fmt.Sprintf("%s-%s-%s.tar.gz", namespace, name, version)
}

// ImportRole validates and stores a role archive as a new role version.
func (s *Service) ImportRole(ctx context.Context, u *model.User, in RoleImport) (*model.Role, error) {
	if !s.Cfg.Content.EnableLegacyRoles {
		return nil, newErr(KindNotFound, "Legacy roles are disabled.")
	}
	if u == nil {
		return nil, newErr(KindUnauthorized, "Authentication credentials were not provided.")
	}
	if !roleVersionRe.MatchString(in.Version) {
		return nil, newErr(KindInvalid, "invalid role version %q", in.Version)
	}
	path, size, sum, err := s.spool(in.Body)
	if err != nil {
		return nil, err
	}
	defer os.Remove(path)

	meta, err := importer.InspectRole(path)
	if err != nil {
		return nil, newErr(KindInvalid, "%s", err)
	}
	if in.Namespace == "" {
		in.Namespace = meta.Namespace
	}
	if in.Name == "" {
		in.Name = meta.Name
	}
	if in.Namespace == "" || in.Name == "" {
		return nil, newErr(KindInvalid, "role namespace and name are required (or set galaxy_info.namespace and galaxy_info.role_name)")
	}
	if !importer.RoleNameRe.MatchString(in.Namespace) || !importer.RoleNameRe.MatchString(in.Name) {
		return nil, newErr(KindInvalid, "invalid role name %s.%s", in.Namespace, in.Name)
	}

	ns, _, err := s.namespaceForUpload(ctx, u, in.Namespace)
	if err != nil {
		return nil, err
	}
	if err := s.requirePublish(u, ns); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	role, err := s.Store.Roles().Get(ctx, in.Namespace, in.Name)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		role = &model.Role{Namespace: in.Namespace, Name: in.Name, Created: now}
	case err != nil:
		return nil, err
	}
	if role.Version(in.Version) != nil {
		return nil, newErr(KindConflict, "Role %s.%s version %s already exists.", in.Namespace, in.Name, in.Version)
	}

	filename := RoleArtifactName(in.Namespace, in.Name, in.Version)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := s.Store.Artifacts().Put(ctx, storage.KindRole, filename, f); err != nil {
		return nil, err
	}

	role.Versions = append(role.Versions, model.RoleVersion{
		Name:     in.Version,
		Artifact: model.Artifact{Filename: filename, SHA256: sum, Size: size},
		Uploader: u.Username,
		Created:  now,
		Modified: now,
	})
	slices.SortStableFunc(role.Versions, func(a, b model.RoleVersion) int { return semver.Compare(a.Name, b.Name) })

	// Metadata follows the highest version.
	if role.Versions[len(role.Versions)-1].Name == in.Version {
		role.Description = meta.Description
		role.Author = meta.Author
		role.Company = meta.Company
		role.License = meta.License
		role.MinAnsibleVersion = meta.MinAnsibleVersion
		role.Tags = meta.Tags
		role.Platforms = meta.Platforms
		role.Dependencies = meta.Dependencies
		role.Readme = meta.Readme
	}
	if in.GithubUser != "" {
		role.GithubUser = in.GithubUser
	}
	if in.GithubRepo != "" {
		role.GithubRepo = in.GithubRepo
	}
	role.Modified = now
	if err := s.Store.Roles().Save(ctx, role); err != nil {
		return nil, err
	}
	s.Log.Info("role import", "role", role.Namespace+"."+role.Name, "version", in.Version, "user", u.Username)
	return role, nil
}

// RoleFilter selects roles in ListRoles. Empty fields match everything.
type RoleFilter struct {
	Namespace string
	Name      string
	Keywords  []string
	Tags      []string
}

func (f RoleFilter) match(r *model.Role) bool {
	if f.Namespace != "" && !strings.EqualFold(r.Namespace, f.Namespace) {
		return false
	}
	if f.Name != "" && r.Name != f.Name {
		return false
	}
	for _, k := range f.Keywords {
		k = strings.ToLower(k)
		if !strings.Contains(strings.ToLower(r.Namespace), k) && !strings.Contains(strings.ToLower(r.Name), k) &&
			!strings.Contains(strings.ToLower(r.Description), k) {
			return false
		}
	}
	for _, t := range f.Tags {
		if !slices.Contains(r.Tags, t) {
			return false
		}
	}
	return true
}

func (s *Service) checkRoles(u *model.User) error {
	if !s.Cfg.Content.EnableLegacyRoles {
		return newErr(KindNotFound, "Legacy roles are disabled.")
	}
	return s.requireRead(u)
}

// ListRoles returns the roles matching f.
func (s *Service) ListRoles(ctx context.Context, u *model.User, f RoleFilter) ([]*model.Role, error) {
	if err := s.checkRoles(u); err != nil {
		return nil, err
	}
	// Exact lookups (ansible-galaxy role install) read a single file. A miss
	// falls through to the scan, which also matches the namespace case
	// insensitively.
	if f.Namespace != "" && f.Name != "" {
		r, err := s.Store.Roles().Get(ctx, f.Namespace, f.Name)
		switch {
		case err == nil && f.match(r):
			return []*model.Role{r}, nil
		case err != nil && !errors.Is(err, storage.ErrNotFound):
			return nil, err
		}
	}
	all, err := s.Store.Roles().List(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, r := range all {
		if f.match(r) {
			out = append(out, r)
		}
	}
	return out, nil
}

// GetRole returns a role by namespace and name.
func (s *Service) GetRole(ctx context.Context, u *model.User, namespace, name string) (*model.Role, error) {
	if err := s.checkRoles(u); err != nil {
		return nil, err
	}
	r, err := s.Store.Roles().Get(ctx, namespace, name)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, newErr(KindNotFound, "Role %s.%s not found.", namespace, name)
	}
	return r, err
}

// GetRoleByID returns a role by ID.
func (s *Service) GetRoleByID(ctx context.Context, u *model.User, id int64) (*model.Role, error) {
	if err := s.checkRoles(u); err != nil {
		return nil, err
	}
	r, err := s.Store.Roles().GetByID(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, newErr(KindNotFound, "Not found.")
	}
	return r, err
}

// CountRoleDownload records an install of the role. While the download
// counter is started the count is buffered in memory and written in batches, otherwise
// it is stored right away.
func (s *Service) CountRoleDownload(ctx context.Context, namespace, name string) {
	key := [2]string{namespace, name}
	s.downloads.mu.Lock()
	if s.downloads.buffered {
		if s.downloads.pending == nil {
			s.downloads.pending = map[[2]string]int64{}
		}
		s.downloads.pending[key]++
		s.downloads.mu.Unlock()
		return
	}
	s.downloads.mu.Unlock()
	s.addDownloads(ctx, key, 1)
}

func (s *Service) addDownloads(ctx context.Context, key [2]string, n int64) {
	if err := s.Store.Roles().AddDownloads(ctx, key[0], key[1], n); err != nil && !errors.Is(err, storage.ErrNotFound) {
		s.Log.Warn("counting role download", "role", key[0]+"."+key[1], "error", err)
	}
}

// FlushDownloads writes buffered download counts to storage.
func (s *Service) FlushDownloads(ctx context.Context) {
	s.downloads.mu.Lock()
	pending := s.downloads.pending
	s.downloads.pending = nil
	s.downloads.mu.Unlock()
	for key, n := range pending {
		s.addDownloads(ctx, key, n)
	}
}

// StartDownloadCounter buffers download counts and flushes them every
// interval; counting per request would otherwise cost a synchronous disk
// write on every role install. Buffering is active when it returns. The
// returned stop function ends buffering and flushes what is pending.
func (s *Service) StartDownloadCounter(interval time.Duration) (stop func()) {
	s.downloads.mu.Lock()
	s.downloads.buffered = true
	s.downloads.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.FlushDownloads(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		cancel()
		<-done
		s.downloads.mu.Lock()
		s.downloads.buffered = false
		s.downloads.mu.Unlock()
		s.FlushDownloads(context.Background())
	}
}

// DeleteRole removes a role, or a single version of it when version is set.
func (s *Service) DeleteRole(ctx context.Context, u *model.User, namespace, name, version string) error {
	role, err := s.GetRole(ctx, u, namespace, name)
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

	var drop []model.RoleVersion
	if version == "" {
		drop = role.Versions
	} else {
		v := role.Version(version)
		if v == nil {
			return newErr(KindNotFound, "Role %s.%s version %s not found.", namespace, name, version)
		}
		drop = []model.RoleVersion{*v}
		role.Versions = slices.DeleteFunc(role.Versions, func(rv model.RoleVersion) bool { return rv.Name == version })
	}
	if version == "" || len(role.Versions) == 0 {
		err = s.Store.Roles().Delete(ctx, namespace, name)
	} else {
		role.Modified = time.Now().UTC()
		err = s.Store.Roles().Save(ctx, role)
	}
	if err != nil {
		return err
	}
	for _, v := range drop {
		if err := s.Store.Artifacts().Delete(ctx, storage.KindRole, v.Artifact.Filename); err != nil &&
			!errors.Is(err, storage.ErrNotFound) {
			s.Log.Warn("removing role artifact", "file", v.Artifact.Filename, "error", err)
		}
	}
	return nil
}

// OpenRoleArtifact opens the archive of a role version. Authorization is the
// caller's responsibility (ansible-galaxy downloads roles without credentials,
// so the API authorizes through signed URLs).
func (s *Service) OpenRoleArtifact(ctx context.Context, filename string) (*storage.Blob, error) {
	b, err := s.Store.Artifacts().Open(ctx, storage.KindRole, filename)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, newErr(KindNotFound, "Not found.")
	}
	return b, err
}
