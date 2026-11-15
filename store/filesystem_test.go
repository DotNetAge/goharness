package store

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// TestReadFileFromFS_AbsolutePath 验证绝对路径剥离前导 "/" 的核心语义：
// fs.FS 只接受相对路径，而调用方（工具层）持有的是绝对路径。
// 用 fstest.MapFS 模拟以根为基准的目录结构。
func TestReadFileFromFS_AbsolutePath(t *testing.T) {
	fsys := fstest.MapFS{
		"etc/hostname":    &fstest.MapFile{Data: []byte("goharness-dev\n")},
		"workspace/go.mod": &fstest.MapFile{Data: []byte("module example.com/app\n")},
	}

	data, err := ReadFileFromFS(fsys, "/etc/hostname")
	if err != nil {
		t.Fatalf("ReadFileFromFS(/etc/hostname) 失败: %v", err)
	}
	if string(data) != "goharness-dev\n" {
		t.Errorf("内容不匹配: %q", data)
	}

	data, err = ReadFileFromFS(fsys, "/workspace/go.mod")
	if err != nil {
		t.Fatalf("ReadFileFromFS(/workspace/go.mod) 失败: %v", err)
	}
	if string(data) != "module example.com/app\n" {
		t.Errorf("内容不匹配: %q", data)
	}
}

// TestReadFileFromFS_RelativePath 相对路径应原样使用（无需剥离）。
func TestReadFileFromFS_RelativePath(t *testing.T) {
	fsys := fstest.MapFS{
		"README.md": &fstest.MapFile{Data: []byte("# demo")},
	}
	data, err := ReadFileFromFS(fsys, "README.md")
	if err != nil {
		t.Fatalf("ReadFileFromFS(README.md) 失败: %v", err)
	}
	if string(data) != "# demo" {
		t.Errorf("内容不匹配: %q", data)
	}
}

// TestReadFileFromFS_NotFound 不存在的路径应返回错误（含 fs.ErrNotExist）。
func TestReadFileFromFS_NotFound(t *testing.T) {
	fsys := fstest.MapFS{}
	_, err := ReadFileFromFS(fsys, "/no/such/file.txt")
	if err == nil {
		t.Fatal("不存在的路径应返回错误")
	}
	if !fsErrNotFound(err) {
		t.Errorf("错误应含 fs.ErrNotExist，实际: %v", err)
	}
}

// TestOpenFromFS_OpenAndRead 验证 OpenFromFS 打开后可正常读取内容，
// 与 ReadFileFromFS 语义一致。
func TestOpenFromFS_OpenAndRead(t *testing.T) {
	fsys := fstest.MapFS{
		"logs/app.log": &fstest.MapFile{Data: []byte("2026-11-15 session started\n")},
	}
	f, err := OpenFromFS(fsys, "/logs/app.log")
	if err != nil {
		t.Fatalf("OpenFromFS 失败: %v", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(data) != "2026-11-15 session started\n" {
		t.Errorf("内容不匹配: %q", data)
	}

	if _, err := OpenFromFS(fsys, "/logs/missing.log"); err == nil {
		t.Error("不存在的文件应返回错误")
	}
}

// TestOS_DefaultReadsRealFS 验证默认 OS 文件系统（os.DirFS("/")）
// 能通过绝对路径读到真实文件——这是工具层读文件的实际路径。
func TestOS_DefaultReadsRealFS(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir) // macOS 上 /var → /private/var
	if err != nil {
		t.Fatalf("解析临时目录失败: %v", err)
	}
	target := filepath.Join(real, "sample.txt")
	if err := os.WriteFile(target, []byte("real fs content"), 0644); err != nil {
		t.Fatalf("写真实文件失败: %v", err)
	}

	data, err := ReadFileFromFS(OS, target)
	if err != nil {
		t.Fatalf("ReadFileFromFS(OS, %s) 失败: %v", target, err)
	}
	if string(data) != "real fs content" {
		t.Errorf("内容不匹配: %q", data)
	}
}

func fsErrNotFound(err error) bool {
	for e := err; e != nil; {
		if e == fs.ErrNotExist {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := e.(unwrapper)
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
