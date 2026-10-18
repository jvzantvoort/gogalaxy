// Package semver implements the subset of Semantic Versioning 2.0.0 needed to
// validate and order collection and role versions.
package semver

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var re = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
	`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
	`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// Version is a parsed semantic version.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
	Build               string
	Original            string
}

// Parse parses a strict semantic version string.
func Parse(s string) (Version, error) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("invalid semantic version %q", s)
	}
	v := Version{Original: s, Build: m[5]}
	v.Major, _ = strconv.ParseUint(m[1], 10, 64)
	v.Minor, _ = strconv.ParseUint(m[2], 10, 64)
	v.Patch, _ = strconv.ParseUint(m[3], 10, 64)
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
	}
	return v, nil
}

// Valid reports whether s is a strict semantic version.
func Valid(s string) bool { return re.MatchString(s) }

// IsPrerelease reports whether the version carries a pre-release tag.
func (v Version) IsPrerelease() bool { return len(v.Pre) > 0 }

// Compare returns -1, 0 or 1 when v is lower, equal or higher than o.
func (v Version) Compare(o Version) int {
	for _, d := range [][2]uint64{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		if c := comparePre(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(v.Pre), len(o.Pre))
}

func comparePre(a, b string) int {
	ai, aerr := strconv.ParseUint(a, 10, 64)
	bi, berr := strconv.ParseUint(b, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		if ai == bi {
			return 0
		}
		if ai < bi {
			return -1
		}
		return 1
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Compare compares two version strings. Strict semantic versions sort by
// precedence; anything else (role tags such as "v1.2" or "master") falls back
// to a loose comparison of numeric and textual components.
func Compare(a, b string) int {
	va, errA := Parse(a)
	vb, errB := Parse(b)
	if errA == nil && errB == nil {
		return va.Compare(vb)
	}
	return looseCompare(a, b)
}

var looseSplit = regexp.MustCompile(`\d+|[a-zA-Z]+`)

func looseCompare(a, b string) int {
	pa := looseSplit.FindAllString(strings.TrimPrefix(a, "v"), -1)
	pb := looseSplit.FindAllString(strings.TrimPrefix(b, "v"), -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if c := comparePre(pa[i], pb[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(pa), len(pb))
}

// Sort sorts version strings in ascending order.
func Sort(versions []string) {
	sort.SliceStable(versions, func(i, j int) bool { return Compare(versions[i], versions[j]) < 0 })
}
