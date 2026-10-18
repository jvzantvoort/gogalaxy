package importer

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// RoleNameRe matches valid role namespace and role names.
var RoleNameRe = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_-]*$`)

// Role is the information extracted from a role archive.
type Role struct {
	Namespace         string
	Name              string
	Description       string
	Author            string
	Company           string
	License           string
	MinAnsibleVersion string
	Tags              []string
	Platforms         []any
	Dependencies      []any
	Readme            string
}

type roleMeta struct {
	GalaxyInfo struct {
		RoleName          string   `yaml:"role_name"`
		Namespace         string   `yaml:"namespace"`
		Author            string   `yaml:"author"`
		Description       string   `yaml:"description"`
		Company           string   `yaml:"company"`
		License           any      `yaml:"license"`
		MinAnsibleVersion any      `yaml:"min_ansible_version"`
		Platforms         []any    `yaml:"platforms"`
		GalaxyTags        []string `yaml:"galaxy_tags"`
	} `yaml:"galaxy_info"`
	Dependencies []any `yaml:"dependencies"`
}

// InspectRole locates meta/main.yml in the role archive at file (the one
// closest to the archive root, mirroring ansible-galaxy) and parses it.
func InspectRole(file string) (*Role, error) {
	type found struct {
		dir  string
		data []byte
	}
	var meta *found
	readmes := map[string]string{}

	err := walkTarGz(file, func(name string, hdr *tar.Header, r io.Reader) error {
		if hdr.Typeflag != tar.TypeReg {
			return nil
		}
		base := path.Base(name)
		dir := path.Dir(name)
		switch {
		case base == "main.yml" || base == "main.yaml":
			if path.Base(dir) != "meta" {
				return nil
			}
			parent := path.Dir(dir)
			if meta != nil && len(parent) >= len(meta.dir) {
				return nil
			}
			b, err := readSmall(r)
			if err != nil {
				return err
			}
			meta = &found{dir: parent, data: b}
		case strings.EqualFold(base, "README.md"):
			b, err := readSmall(r)
			if err != nil {
				return err
			}
			readmes[dir] = string(b)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, errors.New("meta/main.yml not found in the archive")
	}

	var m roleMeta
	if err := yaml.Unmarshal(meta.data, &m); err != nil {
		return nil, fmt.Errorf("invalid meta/main.yml: %w", err)
	}
	gi := m.GalaxyInfo
	return &Role{
		Namespace:         gi.Namespace,
		Name:              gi.RoleName,
		Description:       gi.Description,
		Author:            gi.Author,
		Company:           gi.Company,
		License:           scalarOrList(gi.License),
		MinAnsibleVersion: scalarOrList(gi.MinAnsibleVersion),
		Tags:              gi.GalaxyTags,
		Platforms:         gi.Platforms,
		Dependencies:      m.Dependencies,
		Readme:            readmes[meta.dir],
	}, nil
}

func scalarOrList(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []any:
		parts := make([]string, 0, len(t))
		for _, p := range t {
			parts = append(parts, fmt.Sprint(p))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(t)
	}
}

// PackDir writes the directory dir as a gzipped tar archive to w, with all
// entries placed below the top level directory prefix. Hidden files and
// directories (such as .git) are skipped.
func PackDir(dir, prefix string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = path.Join(prefix, filepath.ToSlash(rel))
		if info.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// dir is a local directory chosen by the CLI operator, not untrusted input.
		f, err := os.Open(p) //nolint:gosec // see above
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
