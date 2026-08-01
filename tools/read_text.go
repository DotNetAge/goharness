package tools

import (
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/store"
	"github.com/DotNetAge/goharness/tools/filestate"
)

// docMeta 是文档转换元数据（仅文档格式读取时提供；纯文本时为 nil）。
type docMeta struct {
	format string // 文档格式（pdf/docx/xlsx/epub），空表示纯文本
	title  string
	author string
	pages  int
}

// buildTextResult 将已读取的原始内容（文本文件内容或文档转换后的 Markdown）按统一
// 文本处理流程生成读取结果：
//
//	参数解析（offset/limit）→ 动态默认行数 → 按行选择 → 输出字符预算检查（超限抛错）→ 结果构建
//
// 文档转换（PDF/DOCX/XLSX/EPUB）的输出也通过此方法按纯文本对待，而不是为每种格式
// 单独实现限制逻辑；超过输出字符预算时返回错误并引导使用 offset/limit 分页精读。
//
// 参数：
//   - resolvedPath: 已解析的绝对路径
//   - cleanPath:    去除前导斜杠的相对路径（用于 fs.Stat）
//   - data:         原始内容（文本文件内容或文档转换的 Markdown）
//   - sizeBytes:    源文件大小
//   - scope:        读取范围
//   - params:       工具参数（offset/limit）
//   - useStateCache:是否启用 DedupCache 与 filestate 状态跟踪（纯文本启用；
//     文档转换禁用，避免转换结果与文件原始状态混淆）
//   - meta:         文档转换元数据（纯文本时为 nil）
func (r *Read) buildTextResult(resolvedPath, cleanPath string, data []byte, sizeBytes int64, scope PathScope, params map[string]any, useStateCache bool, meta *docMeta) (*ReadResult, error) {
	// 获取分页参数
	startLine := 1
	if rawOffset, found := GetParam(params, "offset"); found {
		if offset, ok := ToFloat64(rawOffset); ok && offset > 0 {
			startLine = int(offset)
		}
	}
	maxLines := r.limits.DefaultLines
	if rawLimit, found := GetParam(params, "limit"); found {
		if limit, ok := ToFloat64(rawLimit); ok && limit > 0 {
			maxLines = int(limit)
		}
	}

	// B. DedupCache 检查（仅文本路径启用）
	if useStateCache {
		cacheKey := dedupCacheKey(resolvedPath, startLine, maxLines)
		if cached, ok := getReadFileState(cacheKey); ok {
			statInfo, statErr := fs.Stat(store.OS, cleanPath)
			if statErr == nil {
				s, ok := statInfo.(fs.FileInfo)
				if ok {
					currentMtimeMs := s.ModTime().UnixMilli()
					if cached.MtimeMs == currentMtimeMs && cached.LineCount >= maxLines {
						return &ReadResult{
							Data: &ReadData{
								Success:    true,
								Path:       resolvedPath,
								Scope:      string(scope),
								SizeBytes:  sizeBytes,
								Content:    "文件自上次读取后未发生变化。本对话中之前 Read 工具的结果仍然有效，请引用此前的结果。",
								Suggestion: SuggestionContentUnchanged,
								Note:       "文件未变化。引用之前的结果。",
							},
						}, nil
					}
				}
			}
		}
	}

	// E. 动态默认行数（当未指定 limit 且 DynamicDefaultLines 启用时）
	if _, hasLimit := GetParam(params, "limit"); !hasLimit && r.limits.DynamicDefaultLines {
		preTotalLines := len(strings.Split(string(data), "\n"))
		maxLines = dynamicDefaultLines(preTotalLines, r.limits.DefaultLines)
	}

	endLine := startLine + maxLines - 1

	// 按行分割并选择范围
	allLines := strings.Split(string(data), "\n")
	totalLines := len(allLines)

	var content strings.Builder
	lineNum := 0
	linesRead := 0
	for i, line := range allLines {
		lineNum = i + 1
		if lineNum < startLine {
			continue
		}
		if lineNum > endLine {
			break
		}
		content.WriteString(fmt.Sprintf("%d\t%s\n", lineNum, line))
		linesRead++
	}

	// E2. 输出预算检查：超过预算直接返回错误，引导 LLM 用 offset/limit 缩小范围重试，
	// 不返回接近预算上限的大段内容，避免无用的内容进入上下文缓存
	outputChars := content.Len()
	if r.limits.MaxOutputChars > 0 && outputChars > r.limits.MaxOutputChars {
		return nil, fmt.Errorf("%s",
			GuideReadOutputBudget(startLine, startLine+linesRead-1, outputChars, r.limits.MaxOutputChars, totalLines))
	}

	// B. 写入 DedupCache（仅文本路径启用）
	if useStateCache {
		state := &ReadFileState{
			FilePath:  resolvedPath,
			Offset:    startLine,
			Limit:     linesRead,
			Content:   content.String(),
			LineCount: linesRead,
			SizeBytes: sizeBytes,
		}
		if s, err := fs.Stat(store.OS, cleanPath); err == nil {
			if fi, ok := s.(fs.FileInfo); ok {
				state.MtimeMs = fi.ModTime().UnixMilli()
			}
		}
		setReadFileState(dedupCacheKey(resolvedPath, startLine, maxLines), state)
	}

	// 构建结果
	hasRemainingLines := linesRead >= maxLines && lineNum < totalLines
	noteParts := ""
	var suggestion string
	if hasRemainingLines {
		noteParts = fmt.Sprintf("在偏移量 %d 处有更多可用内容（行 %d-%d，共 %d 行）。",
			endLine+1, endLine+1, totalLines, totalLines)
		suggestion = SuggestionHasMoreLines
	} else {
		suggestion = SuggestionReadComplete
	}

	nextOffset := 0
	if hasRemainingLines {
		nextOffset = endLine + 1
	}

	// G. filestate: 记录读取状态（供 Edit/Write 做 staleness 检测，仅文本路径）
	if useStateCache {
		filestate.SetStale(resolvedPath, time.Now(), data)
	}

	dataBuilder := &ReadData{
		Success:    true,
		Path:       resolvedPath,
		Scope:      string(scope),
		SizeBytes:  sizeBytes,
		LinesRead:  linesRead,
		TotalLines: totalLines,
		StartLine:  startLine,
		Content:    content.String(),
		HasMore:    hasRemainingLines,
		NextOffset: nextOffset,
		Suggestion: suggestion,
		Note:       noteParts,
	}

	// 文档转换元数据合并（格式说明 + 元数据字段）
	if meta != nil {
		formatNote := fmt.Sprintf("已将 %s 文件转换为 Markdown 格式", strings.ToUpper(meta.format))
		if noteParts != "" {
			formatNote += "。" + noteParts
		}
		dataBuilder.Note = formatNote
		dataBuilder.Format = meta.format
		dataBuilder.Title = meta.title
		dataBuilder.Author = meta.author
		dataBuilder.Pages = meta.pages
	}

	return &ReadResult{Data: dataBuilder}, nil
}
