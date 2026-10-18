// Package auth provides pluggable request authentication and authorization.
//
// Authentication backends implement Authenticator and register a Factory
// under a name; the auth.backends configuration key selects which backends
// are chained, in order. Authorization decisions go through the Authorizer
// interface so that the policy can be replaced (e.g. by RBAC) later.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"golang.org/x/crypto/bcrypt"

	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
)

// ErrInvalidCredentials is returned when a request carries credentials that
// cannot be verified.
var ErrInvalidCredentials = errors.New("invalid credentials")

// Authenticator identifies the user making a request.
type Authenticator interface {
	// Name returns the backend name.
	Name() string
	// Scheme returns the HTTP authentication scheme, used in WWW-Authenticate.
	Scheme() string
	// Authenticate returns (nil, nil) when the request carries no credentials
	// handled by this backend, the user when they are valid and
	// ErrInvalidCredentials when they are not.
	Authenticate(r *http.Request) (*model.User, error)
}

// Deps are the dependencies handed to backend factories.
type Deps struct {
	Store storage.Store
	// Options holds the backend specific configuration (auth.options.<name>).
	Options map[string]any
}

// Factory creates an Authenticator.
type Factory func(deps Deps) (Authenticator, error)

var (
	mu       sync.RWMutex
	backends = map[string]Factory{}
)

// Register makes an authentication backend available by name.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := backends[name]; dup {
		panic("auth: backend registered twice: " + name)
	}
	backends[name] = f
}

// Backends lists the registered backend names.
func Backends() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(backends))
	for n := range backends {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Chain tries authenticators in order.
type Chain []Authenticator

// Build creates the chain for the named backends.
func Build(names []string, store storage.Store, options map[string]map[string]any) (Chain, error) {
	var chain Chain
	for _, n := range names {
		mu.RLock()
		f, ok := backends[n]
		mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("unknown auth backend %q (available: %v)", n, Backends())
		}
		a, err := f(Deps{Store: store, Options: options[n]})
		if err != nil {
			return nil, fmt.Errorf("auth backend %q: %w", n, err)
		}
		chain = append(chain, a)
	}
	return chain, nil
}

// Authenticate returns the first user identified by a backend. It returns
// (nil, nil) for anonymous requests.
func (c Chain) Authenticate(r *http.Request) (*model.User, error) {
	for _, a := range c {
		u, err := a.Authenticate(r)
		if err != nil {
			return nil, err
		}
		if u != nil {
			return u, nil
		}
	}
	return nil, nil
}

// Challenge returns the WWW-Authenticate header value for the chain.
func (c Chain) Challenge() string {
	if len(c) == 0 {
		return ""
	}
	return c[0].Scheme() + ` realm="api"`
}

type ctxKey struct{}

// WithUser stores the authenticated user in ctx.
func WithUser(ctx context.Context, u *model.User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// UserFrom returns the authenticated user, or nil for anonymous requests.
func UserFrom(ctx context.Context) *model.User {
	u, _ := ctx.Value(ctxKey{}).(*model.User)
	return u
}

// HashPassword returns a bcrypt hash of password.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}

// CheckPassword reports whether password matches hash.
func CheckPassword(hash, password string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// activeUser loads username and checks that the account may log in.
func activeUser(ctx context.Context, store storage.Store, username string) (*model.User, error) {
	u, err := store.Users().Get(ctx, username)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if !u.IsActive {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}
