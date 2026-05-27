// Package store provides filesystem abstraction utilities.
// It includes helpers for reading files from fs.FS implementations,
// with support for handling absolute paths by converting them to relative paths.
package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// OS is the default filesystem for read operations.
// It points to the OS root filesystem, supporting absolute paths.
// Override in tests with fstest.MapFS for in-memory testing.
var OS fs.FS = os.DirFS("/")

// ReadFileFromFS reads a file from the given filesystem, handling
// absolute paths by stripping the leading "/" for fs.FS compatibility.
func ReadFileFromFS(fsys fs.FS, absPath string) ([]byte, error) {
	rel := strings.TrimLeft(filepath.ToSlash(absPath), "/")
	return fs.ReadFile(fsys, rel)
}

// OpenFromFS opens a file from the given filesystem, handling
// absolute paths by stripping the leading "/" for fs.FS compatibility.
func OpenFromFS(fsys fs.FS, absPath string) (fs.File, error) {
	rel := strings.TrimLeft(filepath.ToSlash(absPath), "/")
	return fsys.Open(rel)
}
