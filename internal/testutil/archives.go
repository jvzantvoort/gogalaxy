// Package testutil builds collection and role archives for tests.
package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"testing"
)

// TarGz builds a gzipped tar archive from name -> content.
func TarGz(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Collection builds a minimal collection artifact.
func Collection(t testing.TB, ns, name, version string, deps map[string]string) []byte {
	t.Helper()
	if deps == nil {
		deps = map[string]string{}
	}
	manifest, err := json.Marshal(map[string]any{
		"collection_info": map[string]any{
			"namespace": ns, "name": name, "version": version,
			"authors": []string{"Test <test@example.com>"}, "description": "test collection",
			"license": []string{"MIT"}, "tags": []string{"demo"}, "dependencies": deps,
			"repository": "https://example.com/repo",
		},
		"format": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return TarGz(t, map[string]string{
		"MANIFEST.json":    string(manifest),
		"FILES.json":       `{"files":[],"format":1}`,
		"meta/runtime.yml": "requires_ansible: '>=2.15.0'\n",
		"README.md":        "# test\n",
	})
}

// Role builds a minimal role archive below a top level directory.
func Role(t testing.TB, dir string) []byte {
	t.Helper()
	return TarGz(t, map[string]string{
		dir + "/meta/main.yml": "galaxy_info:\n  author: tester\n  description: a test role\n  license: MIT\n" +
			"  min_ansible_version: 2.15\n  galaxy_tags: [web, demo]\ndependencies: []\n",
		dir + "/tasks/main.yml": "- debug: msg=hi\n",
		dir + "/README.md":      "role readme\n",
	})
}
