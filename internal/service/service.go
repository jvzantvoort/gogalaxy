// Package service implements the gogalaxy business logic on top of a
// storage.Store. It is shared by the HTTP API and the CLI so that both apply
// the same validation and authorization rules.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jvzantvoort/gogalaxy/internal/auth"
	"github.com/jvzantvoort/gogalaxy/internal/config"
	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
)

// Kind classifies service errors so that callers can map them to responses.
type Kind int

const (
	KindInternal Kind = iota
	KindNotFound
	KindInvalid
	KindConflict
	KindForbidden
	KindUnauthorized
)

// Error is a classified error with a user facing message.
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func newErr(k Kind, format string, args ...any) error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, args...)}
}

// KindOf returns the Kind of err; storage errors are mapped as well.
func KindOf(err error) Kind {
	var e *Error
	switch {
	case errors.As(err, &e):
		return e.Kind
	case errors.Is(err, storage.ErrNotFound):
		return KindNotFound
	case errors.Is(err, storage.ErrExists):
		return KindConflict
	}
	return KindInternal
}

// Service bundles configuration, storage and policy.
type Service struct {
	Cfg    *config.Config
	Store  storage.Store
	Policy auth.Authorizer
	Log    *slog.Logger
}

// New creates a Service. A nil policy selects auth.DefaultPolicy.
func New(cfg *config.Config, store storage.Store, policy auth.Authorizer, log *slog.Logger) *Service {
	if policy == nil {
		policy = auth.DefaultPolicy{
			AnonymousRead:        cfg.Auth.AnonymousRead,
			AutoCreateNamespaces: cfg.Content.AutoCreateNamespaces,
		}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{Cfg: cfg, Store: store, Policy: policy, Log: log}
}

// System is the pseudo user used by CLI commands; it bypasses authorization.
var System = &model.User{Username: "system", IsSuperuser: true, IsActive: true}

func (s *Service) requireRead(u *model.User) error {
	if !s.Policy.CanRead(u) {
		if u == nil {
			return newErr(KindUnauthorized, "Authentication credentials were not provided.")
		}
		return newErr(KindForbidden, "You do not have permission to perform this action.")
	}
	return nil
}

func (s *Service) requirePublish(u *model.User, ns *model.Namespace) error {
	if u == nil {
		return newErr(KindUnauthorized, "Authentication credentials were not provided.")
	}
	if !s.Policy.CanPublish(u, ns) {
		return newErr(KindForbidden, "You do not have permission to publish to namespace %q.", ns.Name)
	}
	return nil
}

func (s *Service) requireAdmin(u *model.User) error {
	if u == nil {
		return newErr(KindUnauthorized, "Authentication credentials were not provided.")
	}
	if !s.Policy.IsAdmin(u) {
		return newErr(KindForbidden, "You do not have permission to perform this action.")
	}
	return nil
}

// CanRead reports whether u may read content.
func (s *Service) CanRead(ctx context.Context, u *model.User) error { return s.requireRead(u) }
