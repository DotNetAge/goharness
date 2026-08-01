package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// deviceFileBlacklist 是设备文件路径的黑名单。
// 读取这些文件会导致进程挂起或无输出（如 /dev/zero, /dev/random）。
// 这些是纯字符串匹配检查，零 I/O。
var deviceFileBlacklist = []string{
	"/dev/zero",
	"/dev/random",
	"/dev/urandom",
	"/dev/null",
	"/dev/tty",
	"/dev/stdin",
	"/dev/stdout",
	"/dev/stderr",
	"/dev/fd/0",
	"/dev/fd/1",
	"/dev/fd/2",
	"/proc/self/fd/0",
	"/proc/self/fd/1",
	"/proc/self/fd/2",
}

// binaryExtensions 是不允许读取的二进制文件扩展名（PDF/图片/SVG 除外）。
// 这些扩展名由对应的专用处理路径处理。
var binaryExtensions = map[string]bool{
	".exe":  true,
	".dll":  true,
	".so":   true,
	".dylib": true,
	".bin":  true,
	".dat":  true,
	".o":    true,
	".a":    true,
	".class": true,
	".pyc":  true,
	".pyo":  true,
	".pyd":  true,
	".deb":  true,
	".rpm":  true,
	".dmg":  true,
	".iso":  true,
	".img":  true,
	".zip":  true,
	".gz":   true,
	".bz2":  true,
	".xz":   true,
	".tar":  true,
	".7z":   true,
	".rar":  true,
}

// hasBinaryExtension 检查文件扩展名是否为不允许的二进制扩展名。
func hasBinaryExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return binaryExtensions[ext]
}

// validateReadPath 对文件路径进行零 I/O 的前置校验。
//
// 检查项（纯字符串操作，不访问文件系统）：
//  1. 设备文件黑名单
//  2. 二进制扩展名（PDF/图片/SVG 有专用处理路径，不受此限制）
//
// 返回：
//   - nil: 校验通过
//   - error: 描述拒绝原因
func validateReadPath(path string) error {
	// 检查设备文件黑名单
	for _, device := range deviceFileBlacklist {
		if path == device {
			return fmt.Errorf("%s", GuideReadInvalidPath(path, "该路径是设备文件，读取会导致进程挂起或输出无意义内容"))
		}
	}

	// 检查二进制扩展名
	if hasBinaryExtension(path) {
		return fmt.Errorf("%s", GuideReadInvalidPath(path, "该文件是二进制文件，Read 不支持读取此格式"))
	}

	return nil
}
