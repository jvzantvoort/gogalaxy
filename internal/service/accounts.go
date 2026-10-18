package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/jvzantvoort/gogalaxy/internal/auth"
	"github.com/jvzantvoort/gogalaxy/internal/importer"
	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9@+_-][a-zA-Z0-9@.+_-]{0,149}$`)

// NewUser describes a user to create.
type NewUser struct {
	Username  string
	Password  string
	Email     string
	FirstName string
	LastName  string
	Superuser bool
}

// CreateUser creates an active local user.
func (s *Service) CreateUser(ctx context.Context, in NewUser) (*model.User, error) {
	if !usernameRe.MatchString(in.Username) {
		return nil, newErr(KindInvalid, "invalid username %q", in.Username)
	}
	now := time.Now().UTC()
	u := &model.User{
		Username:    in.Username,
		Email:       in.Email,
		FirstName:   in.FirstName,
		LastName:    in.LastName,
		IsSuperuser: in.Superuser,
		IsActive:    true,
		Created:     now,
		Modified:    now,
	}
	if in.Password != "" {
		h, err := auth.HashPassword(in.Password)
		if err != nil {
			return nil, err
		}
		u.PasswordHash = h
	}
	if err := s.Store.Users().Create(ctx, u); err != nil {
		if errors.Is(err, storage.ErrExists) {
			return nil, newErr(KindConflict, "user %q already exists", in.Username)
		}
		return nil, err
	}
	return u, nil
}

// GetUser returns a user.
func (s *Service) GetUser(ctx context.Context, username string) (*model.User, error) {
	u, err := s.Store.Users().Get(ctx, username)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, newErr(KindNotFound, "user %q not found", username)
	}
	return u, err
}

// ListUsers returns all users.
func (s *Service) ListUsers(ctx context.Context) ([]*model.User, error) {
	return s.Store.Users().List(ctx)
}

// UpdateUser applies fn to the user and stores the result.
func (s *Service) UpdateUser(ctx context.Context, username string, fn func(*model.User) error) (*model.User, error) {
	u, err := s.GetUser(ctx, username)
	if err != nil {
		return nil, err
	}
	if err := fn(u); err != nil {
		return nil, err
	}
	u.Modified = time.Now().UTC()
	return u, s.Store.Users().Update(ctx, u)
}

// SetPassword changes the password of a user. An empty password disables
// password login.
func (s *Service) SetPassword(ctx context.Context, username, password string) error {
	_, err := s.UpdateUser(ctx, username, func(u *model.User) error {
		if password == "" {
			u.PasswordHash = ""
			return nil
		}
		h, err := auth.HashPassword(password)
		u.PasswordHash = h
		return err
	})
	return err
}

// DeleteUser removes a user and its token.
func (s *Service) DeleteUser(ctx context.Context, username string) error {
	err := s.Store.Users().Delete(ctx, username)
	if errors.Is(err, storage.ErrNotFound) {
		return newErr(KindNotFound, "user %q not found", username)
	}
	return err
}

// IssueToken creates a new API token for the user, replacing any existing one.
func (s *Service) IssueToken(ctx context.Context, username string) (string, error) {
	if _, err := s.GetUser(ctx, username); err != nil {
		return "", err
	}
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	if err := s.Store.Tokens().Set(ctx, username, token); err != nil {
		return "", err
	}
	return token, nil
}

// RevokeToken removes the API token of the user.
func (s *Service) RevokeToken(ctx context.Context, username string) error {
	return s.Store.Tokens().Revoke(ctx, username)
}

// CreateNamespace creates a namespace owned by owners.
func (s *Service) CreateNamespace(ctx context.Context, u *model.User, name string, owners []string) (*model.Namespace, error) {
	if !s.Policy.CanCreateNamespace(u) {
		return nil, newErr(KindForbidden, "You do not have permission to create namespace %q.", name)
	}
	if !importer.NameRe.MatchString(name) && !importer.RoleNameRe.MatchString(name) {
		return nil, newErr(KindInvalid, "invalid namespace name %q", name)
	}
	for _, o := range owners {
		if _, err := s.GetUser(ctx, o); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	ns := &model.Namespace{Name: name, Owners: owners, Created: now, Modified: now}
	if err := s.Store.Namespaces().Create(ctx, ns); err != nil {
		if errors.Is(err, storage.ErrExists) {
			return nil, newErr(KindConflict, "namespace %q already exists", name)
		}
		return nil, err
	}
	return ns, nil
}

// GetNamespace returns a namespace.
func (s *Service) GetNamespace(ctx context.Context, name string) (*model.Namespace, error) {
	ns, err := s.Store.Namespaces().Get(ctx, name)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, newErr(KindNotFound, "namespace %q not found", name)
	}
	return ns, err
}

// ListNamespaces returns all namespaces.
func (s *Service) ListNamespaces(ctx context.Context) ([]*model.Namespace, error) {
	return s.Store.Namespaces().List(ctx)
}

// UpdateNamespace applies fn to the namespace and stores the result.
func (s *Service) UpdateNamespace(ctx context.Context, name string, fn func(*model.Namespace) error) (*model.Namespace, error) {
	ns, err := s.GetNamespace(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := fn(ns); err != nil {
		return nil, err
	}
	ns.Modified = time.Now().UTC()
	return ns, s.Store.Namespaces().Update(ctx, ns)
}

// AddNamespaceOwners adds users as owners of a namespace.
func (s *Service) AddNamespaceOwners(ctx context.Context, name string, usernames ...string) (*model.Namespace, error) {
	for _, o := range usernames {
		if _, err := s.GetUser(ctx, o); err != nil {
			return nil, err
		}
	}
	return s.UpdateNamespace(ctx, name, func(ns *model.Namespace) error {
		for _, o := range usernames {
			if !ns.HasOwner(o) {
				ns.Owners = append(ns.Owners, o)
			}
		}
		return nil
	})
}

// RemoveNamespaceOwners removes users from the owners of a namespace.
func (s *Service) RemoveNamespaceOwners(ctx context.Context, name string, usernames ...string) (*model.Namespace, error) {
	drop := map[string]bool{}
	for _, o := range usernames {
		drop[o] = true
	}
	return s.UpdateNamespace(ctx, name, func(ns *model.Namespace) error {
		kept := ns.Owners[:0]
		for _, o := range ns.Owners {
			if !drop[o] {
				kept = append(kept, o)
			}
		}
		ns.Owners = kept
		return nil
	})
}

// DeleteNamespace removes an empty namespace.
func (s *Service) DeleteNamespace(ctx context.Context, name string) error {
	if _, err := s.GetNamespace(ctx, name); err != nil {
		return err
	}
	for _, repo := range s.Cfg.Content.Repositories {
		cols, err := s.Store.Collections().ListCollections(ctx, repo)
		if err != nil {
			return err
		}
		for _, c := range cols {
			if c.Namespace == name {
				return newErr(KindConflict, "namespace %q still contains collections (repository %s)", name, repo)
			}
		}
	}
	roles, err := s.Store.Roles().List(ctx)
	if err != nil {
		return err
	}
	for _, r := range roles {
		if r.Namespace == name {
			return newErr(KindConflict, "namespace %q still contains roles", name)
		}
	}
	return s.Store.Namespaces().Delete(ctx, name)
}

// namespaceForUpload returns the namespace, creating it for u when allowed.
func (s *Service) namespaceForUpload(ctx context.Context, u *model.User, name string) (*model.Namespace, bool, error) {
	ns, err := s.Store.Namespaces().Get(ctx, name)
	if err == nil {
		return ns, false, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return nil, false, err
	}
	if u == nil || !s.Policy.CanCreateNamespace(u) {
		return nil, false, newErr(KindInvalid, "Namespace %q does not exist.", name)
	}
	var owners []string
	if u != System {
		owners = []string{u.Username}
	}
	ns, err = s.CreateNamespace(ctx, u, name, owners)
	return ns, err == nil, err
}

// GetTask returns an import task.
func (s *Service) GetTask(ctx context.Context, id string) (*model.Task, error) {
	t, err := s.Store.Tasks().Get(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, newErr(KindNotFound, "Not found.")
	}
	return t, err
}
