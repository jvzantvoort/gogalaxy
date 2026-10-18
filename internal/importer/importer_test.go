package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jvzantvoort/gogalaxy/internal/testutil"
)

func write(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseCollectionFilename(t *testing.T) {
	fn, err := ParseCollectionFilename("my_ns-my_col-1.2.3-rc.1.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if fn.Namespace != "my_ns" || fn.Name != "my_col" || fn.Version != "1.2.3-rc.1" {
		t.Fatalf("unexpected %+v", fn)
	}
	for _, bad := range []string{"ns-col.tar.gz", "ns-col-1.0.tar.gz", "NS-col-1.0.0.tar.gz", "ns-col-1.0.0.zip"} {
		if _, err := ParseCollectionFilename(bad); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestInspectCollection(t *testing.T) {
	c, err := InspectCollection(write(t, testutil.Collection(t, "ns", "col", "1.0.0", map[string]string{"a.b": ">=1.0.0"})))
	if err != nil {
		t.Fatal(err)
	}
	if c.Namespace != "ns" || c.Name != "col" || c.Version != "1.0.0" {
		t.Fatalf("unexpected %+v", c)
	}
	if c.RequiresAnsible != ">=2.15.0" || c.Metadata.Dependencies["a.b"] != ">=1.0.0" {
		t.Fatalf("metadata not parsed: %+v", c)
	}
}

func TestInspectCollectionRejectsUnsafePaths(t *testing.T) {
	_, err := InspectCollection(write(t, testutil.TarGz(t, map[string]string{"../evil": "x"})))
	if err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("expected unsafe path error, got %v", err)
	}
}

func TestInspectRole(t *testing.T) {
	r, err := InspectRole(write(t, testutil.Role(t, "myrole-1.0.0")))
	if err != nil {
		t.Fatal(err)
	}
	if r.Description != "a test role" || r.MinAnsibleVersion != "2.15" || len(r.Tags) != 2 || r.Readme == "" {
		t.Fatalf("unexpected %+v", r)
	}
}

func TestPackDir(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"meta/main.yml": "galaxy_info:\n  role_name: x\n", ".git/config": "x", "tasks/main.yml": "[]"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "r.tar.gz")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := PackDir(dir, "ns.x-1.0.0", f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r, err := InspectRole(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "x" {
		t.Fatalf("unexpected %+v", r)
	}
}
