// Package store 提供文件系统抽象工具。
// 它包含从 fs.FS 实现读取文件的辅助函数，
// 支持通过将绝对路径转换为相对路径来处理绝对路径。
package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// OS 是用于读操作的默认文件系统。
// 它指向 OS 根文件系统，支持绝对路径。
// 在测试中可用 fstest.MapFS 覆盖以进行内存测试。
var OS fs.FS = os.DirFS("/")

// ReadFileFromFS 从给定文件系统读取文件，通过
// 去除开头的 "/" 以兼容 fs.FS 来处理绝对路径。
func ReadFileFromFS(fsys fs.FS, absPath string) ([]byte, error) {
	rel := strings.TrimLeft(filepath.ToSlash(absPath), "/")
	return fs.ReadFile(fsys, rel)
}

// OpenFromFS 从给定文件系统打开文件，通过
// 去除开头的 "/" 以兼容 fs.FS 来处理绝对路径。
func OpenFromFS(fsys fs.FS, absPath string) (fs.File, error) {
	rel := strings.TrimLeft(filepath.ToSlash(absPath), "/")
	return fsys.Open(rel)
}
