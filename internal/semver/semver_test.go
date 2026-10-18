package semver

import (
	"slices"
	"testing"
)

func TestValid(t *testing.T) {
	for _, v := range []string{"1.0.0", "0.0.1", "1.2.3-alpha.1", "1.2.3+build.5", "10.20.30-rc.1+meta"} {
		if !Valid(v) {
			t.Errorf("Valid(%q) = false", v)
		}
	}
	for _, v := range []string{"1.0", "v1.0.0", "01.0.0", "1.0.0-", "latest"} {
		if Valid(v) {
			t.Errorf("Valid(%q) = true", v)
		}
	}
}

func TestSort(t *testing.T) {
	in := []string{"1.0.0", "1.0.0-rc.1", "0.9.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-beta.2", "1.0.0-beta.11", "2.0.0"}
	want := []string{"0.9.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "2.0.0"}
	Sort(in)
	if !slices.Equal(in, want) {
		t.Fatalf("Sort = %v, want %v", in, want)
	}
}

func TestLooseCompare(t *testing.T) {
	if Compare("v1.10", "v1.9") <= 0 {
		t.Error("v1.10 should sort after v1.9")
	}
	if Compare("1.2", "1.2.1") >= 0 {
		t.Error("1.2 should sort before 1.2.1")
	}
}
