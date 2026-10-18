package importer

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/semver"
)

var (
	// NameRe matches valid collection namespace and collection names.
	NameRe = regexp.MustCompile(`^[a-z0-9_]+$`)

	filenameRe = regexp.MustCompile(`^([a-z0-9_]+)-([a-z0-9_]+)-(.+)\.tar\.gz$`)
)

// CollectionFilename is the parsed artifact filename namespace-name-version.tar.gz.
type CollectionFilename struct {
	Namespace, Name, Version string
}

// ParseCollectionFilename validates and splits a collection artifact filename.
func ParseCollectionFilename(filename string) (CollectionFilename, error) {
	m := filenameRe.FindStringSubmatch(filename)
	if m == nil {
		return CollectionFilename{}, fmt.Errorf("invalid filename %q, expected <namespace>-<name>-<version>.tar.gz", filename)
	}
	if !semver.Valid(m[3]) {
		return CollectionFilename{}, fmt.Errorf("invalid version %q in filename %q, a semantic version is required", m[3], filename)
	}
	return CollectionFilename{Namespace: m[1], Name: m[2], Version: m[3]}, nil
}

// Collection is the information extracted from a collection artifact.
type Collection struct {
	Namespace       string
	Name            string
	Version         string
	Metadata        model.CollectionMetadata
	Manifest        map[string]any
	RequiresAnsible string
}

type manifestInfo struct {
	Namespace     string            `json:"namespace"`
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	Authors       []string          `json:"authors"`
	Readme        string            `json:"readme"`
	Tags          []string          `json:"tags"`
	Description   string            `json:"description"`
	License       []string          `json:"license"`
	LicenseFile   string            `json:"license_file"`
	Dependencies  map[string]string `json:"dependencies"`
	Repository    string            `json:"repository"`
	Documentation string            `json:"documentation"`
	Homepage      string            `json:"homepage"`
	Issues        string            `json:"issues"`
}

// InspectCollection reads MANIFEST.json and meta/runtime.yml from the
// collection artifact at file and validates them.
func InspectCollection(file string) (*Collection, error) {
	var rawManifest, rawRuntime []byte
	err := walkTarGz(file, func(name string, hdr *tar.Header, r io.Reader) error {
		if hdr.Typeflag != tar.TypeReg {
			return nil
		}
		var err error
		switch name {
		case "MANIFEST.json":
			rawManifest, err = readSmall(r)
		case "meta/runtime.yml", "meta/runtime.yaml":
			rawRuntime, err = readSmall(r)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if rawManifest == nil {
		return nil, errors.New("MANIFEST.json not found in the archive")
	}

	var manifest struct {
		CollectionInfo manifestInfo `json:"collection_info"`
	}
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		return nil, fmt.Errorf("invalid MANIFEST.json: %w", err)
	}
	var full map[string]any
	_ = json.Unmarshal(rawManifest, &full)

	ci := manifest.CollectionInfo
	if !NameRe.MatchString(ci.Namespace) {
		return nil, fmt.Errorf("invalid namespace %q in MANIFEST.json", ci.Namespace)
	}
	if !NameRe.MatchString(ci.Name) {
		return nil, fmt.Errorf("invalid name %q in MANIFEST.json", ci.Name)
	}
	if !semver.Valid(ci.Version) {
		return nil, fmt.Errorf("invalid version %q in MANIFEST.json", ci.Version)
	}
	if len(ci.Authors) == 0 {
		return nil, errors.New("MANIFEST.json: authors must list at least one author")
	}
	if ci.Dependencies == nil {
		ci.Dependencies = map[string]string{}
	}

	c := &Collection{
		Namespace: ci.Namespace,
		Name:      ci.Name,
		Version:   ci.Version,
		Manifest:  full,
		Metadata: model.CollectionMetadata{
			Authors:       ci.Authors,
			Contents:      []any{},
			Dependencies:  ci.Dependencies,
			Description:   ci.Description,
			Documentation: ci.Documentation,
			Homepage:      ci.Homepage,
			Issues:        ci.Issues,
			License:       nonNil(ci.License),
			LicenseFile:   ci.LicenseFile,
			Readme:        ci.Readme,
			Repository:    ci.Repository,
			Tags:          nonNil(ci.Tags),
		},
	}
	if rawRuntime != nil {
		var rt struct {
			RequiresAnsible string `yaml:"requires_ansible"`
		}
		if err := yaml.Unmarshal(rawRuntime, &rt); err != nil {
			return nil, fmt.Errorf("invalid meta/runtime.yml: %w", err)
		}
		c.RequiresAnsible = rt.RequiresAnsible
	}
	return c, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
