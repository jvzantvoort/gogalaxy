// Package model contains the domain types shared by storage, services and API.
package model

import (
	"slices"
	"time"

	"github.com/jvzantvoort/gogalaxy/internal/semver"
)

// User is a local account.
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email,omitempty"`
	FirstName    string    `json:"first_name,omitempty"`
	LastName     string    `json:"last_name,omitempty"`
	PasswordHash string    `json:"password_hash,omitempty"`
	IsSuperuser  bool      `json:"is_superuser"`
	IsActive     bool      `json:"is_active"`
	Created      time.Time `json:"created"`
	Modified     time.Time `json:"modified"`
}

// Namespace groups collections and roles and determines who may publish them.
type Namespace struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Company     string    `json:"company,omitempty"`
	Email       string    `json:"email,omitempty"`
	Description string    `json:"description,omitempty"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	Owners      []string  `json:"owners,omitempty"`
	Created     time.Time `json:"created"`
	Modified    time.Time `json:"modified"`
}

// HasOwner reports whether username is listed as an owner of the namespace.
func (n *Namespace) HasOwner(username string) bool { return slices.Contains(n.Owners, username) }

// Artifact describes a stored archive.
type Artifact struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// Collection is the version-independent part of a collection in a repository.
type Collection struct {
	Namespace     string    `json:"namespace"`
	Name          string    `json:"name"`
	Deprecated    bool      `json:"deprecated"`
	DownloadCount int64     `json:"download_count"`
	Created       time.Time `json:"created"`
	Modified      time.Time `json:"modified"`
}

// CollectionMetadata is the collection_info block of MANIFEST.json.
type CollectionMetadata struct {
	Authors       []string          `json:"authors"`
	Contents      []any             `json:"contents"`
	Dependencies  map[string]string `json:"dependencies"`
	Description   string            `json:"description"`
	Documentation string            `json:"documentation"`
	Homepage      string            `json:"homepage"`
	Issues        string            `json:"issues"`
	License       []string          `json:"license"`
	LicenseFile   string            `json:"license_file,omitempty"`
	Readme        string            `json:"readme,omitempty"`
	Repository    string            `json:"repository"`
	Tags          []string          `json:"tags"`
}

// CollectionVersion is one published version of a collection.
type CollectionVersion struct {
	Namespace       string             `json:"namespace"`
	Name            string             `json:"name"`
	Version         string             `json:"version"`
	Artifact        Artifact           `json:"artifact"`
	Metadata        CollectionMetadata `json:"metadata"`
	Manifest        map[string]any     `json:"manifest,omitempty"`
	RequiresAnsible string             `json:"requires_ansible,omitempty"`
	Uploader        string             `json:"uploader,omitempty"`
	Created         time.Time          `json:"created"`
	Modified        time.Time          `json:"modified"`
}

// HighestVersion returns the highest stable version, or the highest
// pre-release when no stable version exists, like pulp_ansible's
// highest_version. versions must be sorted highest first.
func HighestVersion(versions []*CollectionVersion) *CollectionVersion {
	for _, v := range versions {
		if p, err := semver.Parse(v.Version); err == nil && !p.IsPrerelease() {
			return v
		}
	}
	if len(versions) > 0 {
		return versions[0]
	}
	return nil
}

// FQCN returns namespace.name.
func (c *CollectionVersion) FQCN() string { return c.Namespace + "." + c.Name }

// Role is a standalone (legacy, v1 API) role.
type Role struct {
	ID                int64         `json:"id"`
	Namespace         string        `json:"namespace"`
	Name              string        `json:"name"`
	Description       string        `json:"description,omitempty"`
	Author            string        `json:"author,omitempty"`
	Company           string        `json:"company,omitempty"`
	License           string        `json:"license,omitempty"`
	MinAnsibleVersion string        `json:"min_ansible_version,omitempty"`
	GithubUser        string        `json:"github_user,omitempty"`
	GithubRepo        string        `json:"github_repo,omitempty"`
	GithubBranch      string        `json:"github_branch,omitempty"`
	Tags              []string      `json:"tags,omitempty"`
	Platforms         []any         `json:"platforms,omitempty"`
	Dependencies      []any         `json:"dependencies,omitempty"`
	Readme            string        `json:"readme,omitempty"`
	DownloadCount     int64         `json:"download_count"`
	Versions          []RoleVersion `json:"versions"`
	Created           time.Time     `json:"created"`
	Modified          time.Time     `json:"modified"`
}

// RoleVersion is one uploaded release of a role.
type RoleVersion struct {
	ID       int64     `json:"id"`
	Name     string    `json:"name"`
	Artifact Artifact  `json:"artifact"`
	Uploader string    `json:"uploader,omitempty"`
	Created  time.Time `json:"created"`
	Modified time.Time `json:"modified"`
}

// Version returns the role version with the given name, or nil.
func (r *Role) Version(name string) *RoleVersion {
	for i := range r.Versions {
		if r.Versions[i].Name == name {
			return &r.Versions[i]
		}
	}
	return nil
}

// Task states.
const (
	TaskWaiting   = "waiting"
	TaskRunning   = "running"
	TaskCompleted = "completed"
	TaskFailed    = "failed"
)

// TaskMessage is a log line produced while running a task.
type TaskMessage struct {
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
}

// TaskError describes why a task failed.
type TaskError struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// Task records an import (collection or role).
type Task struct {
	ID         string        `json:"id"`
	Kind       string        `json:"kind"`
	State      string        `json:"state"`
	Repository string        `json:"repository,omitempty"`
	Namespace  string        `json:"namespace,omitempty"`
	Name       string        `json:"name,omitempty"`
	Version    string        `json:"version,omitempty"`
	Username   string        `json:"username,omitempty"`
	Messages   []TaskMessage `json:"messages"`
	Error      *TaskError    `json:"error,omitempty"`
	Created    time.Time     `json:"created"`
	Started    *time.Time    `json:"started,omitempty"`
	Finished   *time.Time    `json:"finished,omitempty"`
}

// Log appends a message to the task.
func (t *Task) Log(level, msg string) {
	t.Messages = append(t.Messages, TaskMessage{Level: level, Message: msg, Time: time.Now().UTC()})
}
