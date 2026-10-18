// Package importer inspects collection and role archives.
package importer

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
)

// maxMetaFileSize bounds the size of metadata files read into memory.
const maxMetaFileSize = 10 << 20

// walkTarGz calls fn for every entry of the gzipped tar archive at file.
// Entries with absolute paths or ".." components are rejected.
func walkTarGz(file string, fn func(name string, hdr *tar.Header, r io.Reader) error) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("not a gzip compressed archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading archive: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if path.IsAbs(name) || hasDotDot(name) {
			return fmt.Errorf("archive contains unsafe path %q", hdr.Name)
		}
		if hdr.Typeflag == tar.TypeLink || hdr.Typeflag == tar.TypeSymlink {
			// This is the traversal check itself; nothing is extracted.
			if path.IsAbs(hdr.Linkname) || hasDotDot(path.Join(path.Dir(name), hdr.Linkname)) { //nolint:gosec // see above
				return fmt.Errorf("archive contains unsafe link %q -> %q", hdr.Name, hdr.Linkname)
			}
		}
		if err := fn(name, hdr, tr); err != nil {
			return err
		}
	}
}

func hasDotDot(p string) bool {
	return slices.Contains(strings.Split(path.Clean(p), "/"), "..")
}

func readSmall(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxMetaFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxMetaFileSize {
		return nil, errors.New("metadata file too large")
	}
	return b, nil
}
