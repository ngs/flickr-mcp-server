// Package localfs confines the files the tools read and write to one
// directory chosen when the server starts.
package localfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutside is returned for a path that leaves the directory.
var ErrOutside = errors.New("path is outside the allowed directory")

// Dir is a directory the tools may use.
type Dir struct {
	root string // absolute, symlinks resolved
}

// Open returns a Dir for path, which must be an existing directory.
func Open(path string) (Dir, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Dir{}, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Dir{}, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return Dir{}, err
	}
	if !info.IsDir() {
		return Dir{}, fmt.Errorf("%s is not a directory", path)
	}
	return Dir{root: real}, nil
}

// Root is the resolved directory.
func (d Dir) Root() string { return d.root }

func (d Dir) inside(p string) bool {
	rel, err := filepath.Rel(d.root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (d Dir) join(rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", fmt.Errorf("%q: give a path relative to %s", rel, d.root)
	}
	p := filepath.Join(d.root, filepath.Clean(rel))
	if !d.inside(p) || p == d.root {
		return "", fmt.Errorf("%q: %w", rel, ErrOutside)
	}
	return p, nil
}

// ReadPath resolves rel for reading: the file, after following symlinks,
// must be a regular file inside the directory.
func (d Dir) ReadPath(rel string) (string, error) {
	p, err := d.join(rel)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if !d.inside(real) {
		return "", fmt.Errorf("%q: %w", rel, ErrOutside)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", rel)
	}
	return real, nil
}

// Create opens rel for writing. Missing parent directories are created; the
// parent, after following symlinks, must be inside the directory, and the
// file itself must not be a symlink. An existing file is replaced only when
// overwrite is true.
func (d Dir) Create(rel string, overwrite bool) (*os.File, string, error) {
	p, err := d.join(rel)
	if err != nil {
		return nil, "", err
	}
	parent := filepath.Dir(p)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return nil, "", err
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, "", err
	}
	if !d.inside(realParent) && realParent != d.root {
		return nil, "", fmt.Errorf("%q: %w", rel, ErrOutside)
	}
	target := filepath.Join(realParent, filepath.Base(p))
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil, "", fmt.Errorf("%q is a symlink; refusing to write through it", rel)
		}
		if !overwrite {
			return nil, "", fmt.Errorf("%q already exists (pass overwrite to replace it)", rel)
		}
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !overwrite {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(target, flags, 0o640) // #nosec G304 -- confined above
	if err != nil {
		return nil, "", err
	}
	return f, target, nil
}
