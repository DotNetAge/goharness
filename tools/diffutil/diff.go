// Package diffutil 提供文件差异生成工具，供 Edit 和 Write 共同使用。
// 见 FILE_TOOLS_ARCHITECTURE.md 缺口清单。
package diffutil

import (
	"fmt"
	"strings"
)

// Hunk 表示一个 diff 块（unified diff 格式中的一段连续变更）。
type Hunk struct {
	OldStart int    `json:"old_start"` // 原文件起始行
	OldLines int    `json:"old_lines"` // 原文件涉及行数
	NewStart int    `json:"new_start"` // 新文件起始行
	NewLines int    `json:"new_lines"` // 新文件涉及行数
	Content  string `json:"content"`   // diff 内容（含行号前缀和 +/- 标记）
}

// GenerateDiff 生成两个文件内容的 unified diff。
//
// 由于 Go 标准库不包含 diff 功能，这里使用简化的行级别 diff 算法。
// 对于多数编辑场景（单处替换、少数几处替换），unified diff 已足够。
//
// 参数：
//   - oldContent: 原始文件内容
//   - newContent: 编辑后文件内容
//
// 返回：
//   - []Hunk: diff hunk 列表
//   - string: human-readable 的 unified diff 字符串
//
// 截断策略：变化行数超过 50 行时只输出首尾各 10 行。
func GenerateDiff(oldContent, newContent string) ([]Hunk, string) {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	// LCS 算法找到最长公共子序列
	lcs := computeLCS(oldLines, newLines)

	if len(lcs) == len(oldLines) && len(lcs) == len(newLines) {
		return nil, "" // 无变化
	}

	// 从 LCS 反推出 diff hunk
	var hunks []Hunk
	var current *Hunk

	oi, ni := 0, 0
	li := 0
	oldStart, newStart := 1, 1

	for oi < len(oldLines) || ni < len(newLines) {
		if li < len(lcs) && oi < len(oldLines) && ni < len(newLines) && oldLines[oi] == lcs[li] && newLines[ni] == lcs[li] {
			// 共同行 → 关闭当前 hunk（如果有）
			if current != nil {
				current.Content = strings.TrimSuffix(current.Content, "\n")
				hunks = append(hunks, *current)
				current = nil
			}
			oi++
			ni++
			li++
			oldStart++
			newStart++
			continue
		}

		if current == nil {
			current = &Hunk{
				OldStart: oldStart,
				NewStart: newStart,
			}
		}

		if oi < len(oldLines) && (li >= len(lcs) || oldLines[oi] != lcs[li]) {
			// 删除行
			current.Content += "-" + oldLines[oi] + "\n"
			current.OldLines++
			oi++
			oldStart++
		} else if ni < len(newLines) && (li >= len(lcs) || newLines[ni] != lcs[li]) {
			// 新增行
			current.Content += "+" + newLines[ni] + "\n"
			current.NewLines++
			ni++
			newStart++
		}

		// 安全退出（防止无限循环）
		if oi >= len(oldLines) && ni >= len(newLines) {
			break
		}
	}

	if current != nil {
		current.Content = strings.TrimSuffix(current.Content, "\n")
		hunks = append(hunks, *current)
	}

	// 生成 human-readable 的 unified diff
	diffStr := buildUnifiedDiff(hunks)

	return hunks, diffStr
}

// computeLCS 计算两行集合的最长公共子序列。
func computeLCS(a, b []string) []string {
	m, n := len(a), len(b)
	if m == 0 || n == 0 {
		return nil
	}

	// 使用两行 DP（节省空间）
	prev := make([]int, n+1)
	curr := make([]int, n+1)

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				curr[j] = prev[j-1] + 1
			} else {
				if prev[j] > curr[j-1] {
					curr[j] = prev[j]
				} else {
					curr[j] = curr[j-1]
				}
			}
		}
		prev, curr = curr, prev
	}

	// 回溯
	lcs := make([]string, 0, prev[n])
	i, j := m, n
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			lcs = append([]string{a[i-1]}, lcs...)
			i--
			j--
		} else if prev[j-1] >= curr[j] {
			j-- // 此时 prev 实际上指向上一行
		} else {
			i--
		}
	}

	return lcs
}

// buildUnifiedDiff 将 []Hunk 格式化为 human-readable 的 unified diff 字符串。
// 截断策略：变化行数超过 50 行时只输出首尾各 10 行。
func buildUnifiedDiff(hunks []Hunk) string {
	if len(hunks) == 0 {
		return ""
	}

	var b strings.Builder
	for _, hunk := range hunks {
		lines := strings.Split(hunk.Content, "\n")
		if len(lines) > 50 {
			// 只显示首尾各 10 行
			firstLines := lines[:10]
			lastLines := lines[len(lines)-10:]

			fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", hunk.OldStart, hunk.OldLines, hunk.NewStart, hunk.NewLines)
			for _, l := range firstLines {
				b.WriteString(l)
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "... (%d 行变化，已截断) ...\n", len(lines))
			for _, l := range lastLines {
				b.WriteString(l)
				b.WriteByte('\n')
			}
		} else {
			fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", hunk.OldStart, hunk.OldLines, hunk.NewStart, hunk.NewLines)
			b.WriteString(hunk.Content)
			if !strings.HasSuffix(hunk.Content, "\n") {
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}
