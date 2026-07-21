package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/store"
	"github.com/DotNetAge/goharness/tools/filestate"
)

// Read 实现了文件读取工具。
// 支持从本地文件系统读取文件内容，具有以下特性：
//   - 分页读取：通过 offset 和 limit 参数读取指定行范围
//   - 文件大小限制：防止读取过大的文件
//   - Token 预算控制：根据 token 限制截断输出
//   - 多格式支持：文本文件、图片、PDF、DOCX、XLSX、EPUB 等
//   - 去重缓存：避免重复 I/O（见过度设计审计 #6：简化至全量 TTL 缓存）
//   - ENOENT 兜底：简化的 CWD 前缀建议（见过度设计审计 #7）
//   - 图片读取：通过 FileReadingLimits.EnableImageReading 启用，内联处理（见过度设计审计 #8）
//   - 动态默认行数：根据文件大小自动调整默认读取行数
//
// 安全级别：LevelSafe（安全），只读操作不会修改文件系统
type Read struct {
	info      *ToolInfo         // 工具元信息
	limits    FileReadingLimits // 文件读取限制配置
	whitelist []string          // 允许读取的目录前缀（绕过项目边界检查）
}

// AddWhiteList 添加允许读取的目录前缀。
// 当目标路径匹配任一白名单前缀时，Execute() 会跳过 ValidateFileSafety 检查，
// 允许读取项目目录之外的文件。通常在工具初始化后、注册到 ToolRegistry 之前调用。
func (r *Read) AddWhiteList(dirs ...string) *Read {
	r.whitelist = append(r.whitelist, dirs...)
	return r
}

// NewReadTool 创建一个使用默认限制的文件读取工具。
//
// 返回：
//   - FuncTool: 配置好的 Read 工具实例
func NewReadTool() *Read {
	return NewReadToolWithLimits(DefaultFileReadingLimits())
}

// NewReadToolWithLimits 创建一个使用自定义限制的文件读取工具。
//
// 参数：
//   - limits: 文件读取限制配置（大小、行数、token 等）
//
// 返回：
//   - *Read: 配置好的 Read 工具实例
func NewReadToolWithLimits(limits FileReadingLimits) *Read {
	return &Read{
		limits: limits,
		info: &ToolInfo{
			Name:        "Read",
			Description: "从本地文件系统读取文件。支持 PDF、DOCX、XLSX、EPUB 文档自动转换为 Markdown。",
			Prompt: `从本地文件系统读取文件。

**文本文件（.go, .py, .txt, .json 等）**
- 结果使用 cat -n 格式显示行号。
- 使用 offset/limit 读取特定范围，默认从开头读取最多 500 行。

**文档文件（.pdf, .docx, .xlsx, .epub）**
- 自动转换为 Markdown 格式返回。
- offset/limit 参数不适用（全文转换）。
- 结果中会包含 format、title、author、pages 等元数据字段。

**图片文件（.png, .jpg, .gif, .bmp, .webp, .svg）**
- 启用后自动读取并压缩图片，返回 base64 编码数据。
- 压缩最长边为 512px，JPEG 质量自适应（90/85/70）。
- SVG 文件不解码不压缩，直接返回 base64 编码。`,
			Tags:               []string{"file", "filesystem", "read", "content"},
			SecurityLevel:      events.LevelSafe,
			IsReadOnly:         true,
			MaxResultSizeChars: -1,
			Parameters: []Parameter{
				{
					Name:        "filePath",
					Type:        "string",
					Required:    true,
					Description: "要读取的文件的绝对路径。",
				},
				{
					Name:        "offset",
					Type:        "integer",
					Required:    false,
					Description: "开始读取的行号（从 1 开始）。",
				},
				{
					Name:        "limit",
					Type:        "integer",
					Required:    false,
					Description: "要读取的最大行数。默认为 500。",
				},
			},
		},
	}
}

// Info 返回 Read 工具的元信息。
func (r *Read) Info() *ToolInfo {
	return r.info
}

// Execute 执行文件读取操作。
//
// 处理流程（严格遵循 DESIGN.md 数据流图）：
//
//	PreValidate → NegativeCache → 路径解析 → 安全校验 → fs.Stat
//	    ├── ENOENT？    → NegativeCache + 渐进兜底
//	    ├── 权限拒绝？   → SuggestionPermissionDenied
//	    ├── 空文件？     → SuggestionEmptyFile
//	    ├── 目录？       → SuggestionIsDirectory
//	    ├── 文件过大？   → SuggestionFileTooLarge
//	    ├── 文档格式     → convertDocument
//	    ├── 图片格式     → readImageWithHook
//	    └── 文本文件     → readFileInRange + DedupCache
//
// 参数：
//   - ctx: 上下文（包含 ToolContext）
//   - params: 必须包含 "filePath"，可选 "offset" 和 "limit"
//
// 返回：
//   - *ReadResult: 结构化的读取结果
//   - error: 参数错误、安全校验失败、或文件系统错误
func (r *Read) Execute(ctx context.Context, params map[string]any) (any, error) {
	path, err := ValidateRequiredString(params, "filePath")
	if err != nil {
		return nil, err
	}

	logger := getLogger(ctx)

	tc := GetToolContext(ctx)
	resolvedPath, scope := ResolveTargetPath(path, tc.Session.ProjectDir(), tc.Session.SessionDir())

	logger.Info("reading file",
		"input_path", path,
		"resolved_path", resolvedPath,
		"scope", scope,
	)

	// A. 零 I/O 前置校验（设备文件黑名单、二进制扩展名）
	if err = validateReadPath(resolvedPath); err != nil {
		return nil, err
	}

	// B. NegativeCache：快速拒绝
	if checkNegativeCache(resolvedPath) {
		return &ReadResult{
			Data: &ReadData{
				Success:    false,
				Path:       resolvedPath,
				Scope:      string(scope),
				Note:       "文件不存在：" + resolvedPath + "（此前已确认）",
				Suggestion: SuggestionFileNotFound,
			},
		}, nil
	}

	// 安全校验：白名单或项目边界检查
	whitelisted := false
	for _, dir := range r.whitelist {
		if strings.HasPrefix(resolvedPath, dir) {
			whitelisted = true
			break
		}
	}
	if whitelisted {
		if err := checkSensitiveFiles(resolvedPath); err != nil {
			return nil, err
		}
	} else {
		if err := ValidateFileSafety(resolvedPath, tc.Session.ProjectDir()); err != nil {
			return nil, err
		}
	}

	// Pre-read: stat 检查
	cleanPath := strings.TrimLeft(filepath.ToSlash(resolvedPath), "/")
	info, err := fs.Stat(store.OS, cleanPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// C. ENOENT 兜底（简化版，见过度设计审计 #7）
			setNegativeCache(resolvedPath)
			message := ENOENTSuggestion(resolvedPath, tc.Session.ProjectDir())
			return &ReadResult{
				Data: &ReadData{
					Success:    false,
					Path:       resolvedPath,
					Scope:      string(scope),
					Note:       message,
					Suggestion: SuggestionFileNotFound,
				},
			}, nil
		}
		return nil, fmt.Errorf("标记文件状态错误: %w", err)
	}

	// 权限检查：在 stat 之后尝试打开文件验证读权限
	if _, err := os.Stat(resolvedPath); err == nil {
		f, err := os.OpenFile(resolvedPath, os.O_RDONLY, 0)
		if err != nil {
			return &ReadResult{
				Data: &ReadData{
					Success:    false,
					Path:       resolvedPath,
					Scope:      string(scope),
					SizeBytes:  info.Size(),
					Note:       "无读取权限：" + resolvedPath + "。请检查文件权限或使用 shell 命令读取。",
					Suggestion: SuggestionPermissionDenied,
				},
			}, nil
		}
		f.Close()
	}

	// 目录检查
	if info.IsDir() {
		return &ReadResult{
			Data: &ReadData{
				Success:    false,
				Path:       resolvedPath,
				Scope:      string(scope),
				Note:       "路径是一个目录，不是文件：" + resolvedPath,
				Suggestion: SuggestionIsDirectory,
			},
		}, nil
	}

	// 空文件检查
	if info.Size() == 0 {
		return &ReadResult{
			Data: &ReadData{
				Success:    true,
				Path:       resolvedPath,
				Scope:      string(scope),
				SizeBytes:  0,
				Content:    "",
				Suggestion: SuggestionEmptyFile,
				Note:       "文件为空，无内容可读取",
			},
		}, nil
	}

	// 文件大小检查
	if r.limits.MaxSizeBytes > 0 && info.Size() > r.limits.MaxSizeBytes {
		return &ReadResult{
			Data: &ReadData{
				Success:   false,
				Path:      resolvedPath,
				Scope:     string(scope),
				SizeBytes: info.Size(),
				Note: fmt.Sprintf(
					"文件太大（%.2f KB），最大允许 %d KB。使用 offset 和 limit 参数读取特定部分，或先使用 grep/glob 定位相关部分。",
					float64(info.Size())/1024, r.limits.MaxSizeBytes/1024),
				Suggestion: SuggestionFileTooLarge,
			},
		}, nil
	}

	// 检测文档格式（PDF/DOCX/XLSX/EPUB），直接转换为 Markdown 返回
	if format := detectDocFormat(resolvedPath); format != "" {
		f, err := store.OS.Open(cleanPath)
		if err != nil {
			return nil, fmt.Errorf("无法打开文件: %w", err)
		}
		defer f.Close()

		doc, err := convertDocument(format, f)
		if err != nil {
			return nil, err
		}

		return &ReadResult{
			Data: &ReadData{
				Success:    true,
				Path:       resolvedPath,
				Scope:      string(scope),
				SizeBytes:  info.Size(),
				Content:    doc.content,
				Format:     format,
				Title:      doc.title,
				Author:     doc.author,
				Pages:      doc.pages,
				Note:       fmt.Sprintf("已将 %s 文件转换为 Markdown 格式", strings.ToUpper(format)),
				Suggestion: SuggestionDocConverted,
			},
		}, nil
	}

	// 图片文件读取（受 EnableImageReading 控制，内联处理，不写入 DedupCache）
	if r.limits.EnableImageReading && isImageFile(resolvedPath) {
		data, err := store.ReadFileFromFS(store.OS, resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("无法读取文件: %w", err)
		}

		// 内联图片处理（见过度设计审计 #8：直接调用 compressAndEncodeImage，不再走 Hook）
		ic := compressAndEncodeImage(data, resolvedPath, 512)
		summary := fmtImageSummary(ic, filepath.Base(resolvedPath))

		return &ReadResult{
			Data: &ReadData{
				Success:    true,
				Path:       resolvedPath,
				Scope:      string(scope),
				SizeBytes:  info.Size(),
				Content:    summary,
				Suggestion: SuggestionImageRead,
				Note:       fmt.Sprintf("图片已读取并压缩：%s (%d×%d px, 原始 %d 字节, 压缩后 %d 字节)", filepath.Base(resolvedPath), ic.Width, ic.Height, ic.RawSize, ic.CompressedSize),
			},
			Images: []ImageContent{ic},
		}, nil
	}

	// 文本文件读取
	data, err := store.ReadFileFromFS(store.OS, resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("无法读取文件: %w", err)
	}

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

	// B. DedupCache 检查
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
							SizeBytes:  info.Size(),
							Content:    "文件自上次读取后未发生变化。本对话中之前 Read 工具的结果仍然有效，请引用此前的结果。",
							Suggestion: SuggestionContentUnchanged,
							Note:       "文件未变化。引用之前的结果。",
						},
					}, nil
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

	// E2. 两级 Token 估算（第二级按比例截断，无需 tokenizer）
	tokenTruncated := false
	outputChars := content.Len()
	estimatedTokens := outputChars / 3
	if r.limits.MaxTokens > 0 && estimatedTokens > r.limits.MaxTokens {
		// 第二级：按比例截断
		ratio := float64(r.limits.MaxTokens) / float64(estimatedTokens)
		if ratio < 1.0 && linesRead > 0 {
			keepLines := int(float64(linesRead) * ratio)
			if keepLines < 1 {
				keepLines = 1
			}
			truncationNote := fmt.Sprintf(
				"\n[... 内容被截断：超过 %d 个 token 的预算。"+
					" 剩余行：%d-%d，共 %d 行。"+
					" 使用 offset 和 limit 缩小范围。...]\n",
				r.limits.MaxTokens, startLine+keepLines, totalLines, totalLines)
			content.Reset()
			for i := 0; i < keepLines && i < linesRead; i++ {
				content.WriteString(fmt.Sprintf("%d\t%s\n", startLine+i, allLines[startLine-1+i]))
			}
			content.WriteString(truncationNote)
			tokenTruncated = true
		}
	}

	// B. 写入 DedupCache
	state := &ReadFileState{
		FilePath:  resolvedPath,
		Offset:    startLine,
		Limit:     linesRead,
		Content:   content.String(),
		LineCount: linesRead,
		SizeBytes: info.Size(),
	}
	if s, err := fs.Stat(store.OS, cleanPath); err == nil {
		if fi, ok := s.(fs.FileInfo); ok {
			state.MtimeMs = fi.ModTime().UnixMilli()
		}
	}
	setReadFileState(cacheKey, state)

	// 构建结果
	hasRemainingLines := linesRead >= maxLines && lineNum < totalLines
	noteParts := ""
	var suggestion string
	if tokenTruncated {
		noteParts = fmt.Sprintf("在 %d 个 token 处截断。剩余行 %d-%d。使用更小的 offset/limit。",
			r.limits.MaxTokens, endLine+1, totalLines)
		suggestion = SuggestionTruncatedByToken
	} else if hasRemainingLines {
		noteParts = fmt.Sprintf("在偏移量 %d 处有更多可用内容（行 %d-%d，共 %d 行）。",
			endLine+1, endLine+1, totalLines, totalLines)
		suggestion = SuggestionHasMoreLines
	} else {
		suggestion = SuggestionReadComplete
	}

	nextOffset := 0
	if hasRemainingLines || tokenTruncated {
		if tokenTruncated && !hasRemainingLines {
			nextOffset = startLine
		} else {
			nextOffset = endLine + 1
		}
	}

	// G. filestate: 记录读取状态（供 Edit/Write 做 staleness 检测）
	filestate.SetStale(resolvedPath, time.Now(), data)

	return &ReadResult{
		Data: &ReadData{
			Success:    true,
			Path:       resolvedPath,
			Scope:      string(scope),
			SizeBytes:  info.Size(),
			LinesRead:  linesRead,
			TotalLines: totalLines,
			StartLine:  startLine,
			Content:    content.String(),
			HasMore:    hasRemainingLines || tokenTruncated,
			NextOffset: nextOffset,
			Truncated:  tokenTruncated,
			TruncatedAt: func() int {
				if tokenTruncated {
					return r.limits.MaxTokens
				}
				return 0
			}(),
			Suggestion: suggestion,
			Note:       noteParts,
		},
	}, nil
}
