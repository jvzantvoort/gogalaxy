package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
)

func init() {
	Register("token", func(d Deps) (Authenticator, error) { return &TokenAuth{store: d.Store}, nil })
	Register("basic", func(d Deps) (Authenticator, error) { return &BasicAuth{store: d.Store}, nil })
}

// TokenAuth accepts API tokens, sent by ansible-galaxy as
// "Authorization: Token <key>" (or "Bearer <key>").
type TokenAuth struct{ store storage.Store }

func (*TokenAuth) Name() string   { return "token" }
func (*TokenAuth) Scheme() string { return "Token" }

func (t *TokenAuth) Authenticate(r *http.Request) (*model.User, error) {
	scheme, key, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || (!strings.EqualFold(scheme, "Token") && !strings.EqualFold(scheme, "Bearer")) {
		return nil, nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrInvalidCredentials
	}
	username, err := t.store.Tokens().Lookup(r.Context(), key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	return activeUser(r.Context(), t.store, username)
}

// BasicAuth accepts HTTP basic authentication with a local password.
type BasicAuth struct{ store storage.Store }

func (*BasicAuth) Name() string   { return "basic" }
func (*BasicAuth) Scheme() string { return "Basic" }

func (b *BasicAuth) Authenticate(r *http.Request) (*model.User, error) {
	username, password, ok := r.BasicAuth()
	if !ok {
		return nil, nil
	}
	u, err := activeUser(r.Context(), b.store, username)
	if err != nil {
		return nil, err
	}
	if !CheckPassword(u.PasswordHash, password) {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}
