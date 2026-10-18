package auth

import "github.com/jvzantvoort/gogalaxy/internal/model"

// Authorizer decides what a user may do. A nil user is an anonymous request.
type Authorizer interface {
	// CanRead allows browsing and downloading content.
	CanRead(u *model.User) bool
	// CanPublish allows uploading and deleting content in ns.
	CanPublish(u *model.User, ns *model.Namespace) bool
	// CanCreateNamespace allows creating a namespace, e.g. on first upload.
	CanCreateNamespace(u *model.User) bool
	// IsAdmin allows managing users and namespaces.
	IsAdmin(u *model.User) bool
}

// DefaultPolicy is a simple owner based policy: superusers may do anything,
// namespace owners may publish to their namespace.
type DefaultPolicy struct {
	AnonymousRead        bool
	AutoCreateNamespaces bool
}

func (p DefaultPolicy) CanRead(u *model.User) bool { return p.AnonymousRead || u != nil }

func (p DefaultPolicy) CanPublish(u *model.User, ns *model.Namespace) bool {
	if u == nil {
		return false
	}
	return u.IsSuperuser || (ns != nil && ns.HasOwner(u.Username))
}

func (p DefaultPolicy) CanCreateNamespace(u *model.User) bool {
	return u != nil && (u.IsSuperuser || p.AutoCreateNamespaces)
}

func (p DefaultPolicy) IsAdmin(u *model.User) bool { return u != nil && u.IsSuperuser }
