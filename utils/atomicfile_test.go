package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	if err := WriteFileAtomic(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("内容不符：期望 hello，实际 %q", string(got))
	}
}

func TestWriteFileAtomic_ReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(path, []byte("old content that is longer"), 0o644); err != nil {
		t.Fatalf("准备旧文件: %v", err)
	}

	if err := WriteFileAtomic(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("覆盖后内容不符：期望 new，实际 %q", string(got))
	}
}

// 原子写用 CreateTemp 建临时文件（默认 0600），必须显式 chmod 回目标权限，
// 否则会把原本可执行/可读的文件权限收紧。
func TestWriteFileAtomic_PreservesPermission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")

	if err := WriteFileAtomic(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("权限不符：期望 0755，实际 %o", perm)
	}
}

func TestWriteFileAtomic_LeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")

	if err := WriteFileAtomic(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "target.txt" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("目录中不应残留临时文件，实际: %v", names)
	}
}

// 关键属性：写入路径失败时，目标文件必须保持原样。
// 这是 os.WriteFile 做不到的——它先截断，失败后目标已是半截或空文件。
func TestWriteFileAtomic_KeepsTargetWhenDirUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 可绕过目录权限，无法构造失败场景")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("准备旧文件: %v", err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	if err := WriteFileAtomic(path, []byte("new"), 0o644); err == nil {
		t.Fatal("目录不可写时应返回错误")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取目标文件失败: %v", err)
	}
	if string(got) != "old" {
		t.Errorf("写入失败时目标文件不应被改动：期望 old，实际 %q", string(got))
	}
}
