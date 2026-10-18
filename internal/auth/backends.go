package auth

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
)

func init() {
	Register("token", func(d Deps) (Authenticator, error) { return &TokenAuth{store: d.Store}, nil })
	Register("basic", func(d Deps) (Authenticator, error) { return NewBasicAuth(d.Store), nil })
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
//
// bcrypt is deliberately slow (tens of milliseconds per check), and clients
// configured with a username and password send them on every request, so
// successful verifications are cached briefly. The cache key is derived from
// the stored hash as well, so password changes take effect immediately; the
// user record itself (e.g. is_active) is still read on every request.
type BasicAuth struct {
	store storage.Store

	mu    sync.Mutex
	valid map[[32]byte]time.Time
}

const (
	basicCacheTTL  = 5 * time.Minute
	basicCacheSize = 10000
)

// NewBasicAuth creates the basic authentication backend.
func NewBasicAuth(store storage.Store) *BasicAuth {
	return &BasicAuth{store: store, valid: map[[32]byte]time.Time{}}
}

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
	if u.PasswordHash == "" {
		return nil, ErrInvalidCredentials
	}
	key := sha256.Sum256([]byte(u.PasswordHash + "\x00" + password))
	now := time.Now()

	b.mu.Lock()
	exp, cached := b.valid[key]
	b.mu.Unlock()
	if cached && now.Before(exp) {
		return u, nil
	}
	if !CheckPassword(u.PasswordHash, password) {
		return nil, ErrInvalidCredentials
	}

	b.mu.Lock()
	if len(b.valid) >= basicCacheSize {
		for k, e := range b.valid {
			if now.After(e) {
				delete(b.valid, k)
			}
		}
		if len(b.valid) >= basicCacheSize {
			clear(b.valid)
		}
	}
	b.valid[key] = now.Add(basicCacheTTL)
	b.mu.Unlock()
	return u, nil
}
