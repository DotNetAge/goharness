package utils

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic 以「临时文件 + fsync + rename」的方式原子替换 path 的内容。
//
// 为什么需要它：os.WriteFile 会先截断目标文件再写入。若进程在写入中途崩溃
// 或断电，目标文件会停在半截状态——对源码文件是内容损坏，对状态 sidecar
// （pending_ask / session_wait / whitelist / perm）是留下无法解析的 JSON，
// 下次加载直接失败。rename 在同一文件系统内是原子操作，读者要么看到旧内容、
// 要么看到新内容，不会看到中间态。
//
// 约束与取舍：
//   - 临时文件必须建在目标同目录，跨文件系统 rename 会失败（EXDEV）。
//   - 写入后对文件做 fsync，确保数据真的落盘而不是停在页缓存里；
//     未对父目录 fsync，因此极端掉电下「文件存在但目录项未落盘」仍可能发生，
//     但不会出现内容截断——这是与 os.WriteFile 的本质差别。
//   - 显式 chmod 到 perm：CreateTemp 默认 0600，不 chmod 会把原本 0644 的
//     文件权限收紧。
//   - 任一步失败都清理临时文件，且绝不触碰目标文件。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	f, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := f.Name()

	// 失败路径统一清理：先关再删，避免在 Windows 上删不掉被占用的文件。
	fail := func(cause error) error {
		_ = f.Close()
		_ = os.Remove(tmpName)
		return cause
	}

	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
