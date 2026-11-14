package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 回归：超过 Scanner 默认 64KB 上限的单行文件必须能扫描到命中。
func TestGrepScanFile_LongLine(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "long.txt")
	if err := os.WriteFile(file, []byte(strings.Repeat("a", 100*1024)+"NEEDLE\n"), 0644); err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile("NEEDLE")
	res, count, truncated, err := grepScanFile(file, "long.txt", re, "content", 100)
	if err != nil {
		t.Fatalf("长行扫描报错: %v", err)
	}
	if count != 1 || len(res) != 1 || truncated {
		t.Fatalf("长行未命中: count=%d len=%d truncated=%v", count, len(res), truncated)
	}
}

// 回归：命中行保存受上限约束，但计数仍为全量。
func TestGrepScanFile_BoundedResults(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "many.txt")
	if err := os.WriteFile(file, []byte(strings.Repeat("hit line\n", 100000)), 0644); err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile("hit")
	res, count, truncated, err := grepScanFile(file, "many.txt", re, "content", 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 100000 {
		t.Fatalf("计数应覆盖全量: %d", count)
	}
	if len(res) != 10 || !truncated {
		t.Fatalf("保存未受上限约束: len=%d truncated=%v", len(res), truncated)
	}
}
