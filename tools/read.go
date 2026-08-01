package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/store"
)

// Read 实现了文件读取工具。
// 支持从本地文件系统读取文件内容，具有以下特性：
//   - 分页读取：通过 offset 和 limit 参数读取指定行范围
//   - 文件大小限制：防止读取过大的文件
//   - 输出预算控制：根据输出字符数限制，超限返回错误并引导分页读取
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
// 当目标路径匹配任一白名单前缀时，Grant() 与 Execute() 会跳过项目边界检查，
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
//   - limits: 文件读取限制配置（大小、行数、输出字符预算等）
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
- 自动转换为 Markdown 后按纯文本处理，支持 offset/limit 分页精读。
- 超过输出字符预算时返回错误，并提示改用 offset/limit 分页读取后续部分。
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

// Limits 返回当前文件读取限制配置。
// 供增强版工具（如 mindx 的 ReadPro）读取 MaxSizeBytes 阈值，
// 判断何时需要对大文件启用知识库分块树预览。
func (r *Read) Limits() FileReadingLimits {
	return r.limits
}

// SetImageReading 启用或禁用图片文件读取。
// 仅当模型支持视觉理解（ModelConfig.Visioning 为 true）时应启用；
// 否则图片读取生成的 base64 数据对不支持视觉的模型没有意义，
// 且会白白占用上下文空间。
func (r *Read) SetImageReading(enabled bool) {
	r.limits.EnableImageReading = enabled
}

// CheckSafety 执行与 Execute 前置校验一致的安全预检：
//   - validateReadPath：设备文件黑名单、二进制扩展名拦截
//   - checkSensitiveFiles：敏感文件硬性拦截
//
// 项目边界检查由 Grant()（PermissionRequired）负责，Execute 侧不再重复校验，
// 避免授权（PermissionAllow / AllowSession）或白名单放行后执行被再次拦截。
// 供增强版工具（如 mindx 的 ReadPro）在自行实现大文件读取分支时复用。
func (r *Read) CheckSafety(resolvedPath string) error {
	if err := validateReadPath(resolvedPath); err != nil {
		return err
	}
	return checkSensitiveFiles(resolvedPath)
}

// Grant implements tools.PermissionRequired。
//
// 与 Edit / Bash 保持一致的授权语义：目标路径超出项目边界（ValidateFileSafety 失败）
// 时，先放行工具白名单（AddWhiteList）与会话级白名单（PermissionAllowSession 记忆）
// 内的路径，其余越界读取触发授权流程（返回 granted=false，运行时挂起思考循环等待
// 用户通过 PermissionAllow / PermissionAllowSession / PermissionDeny 魔法词回应）。
//
// 敏感文件（.env / .ssh 等）虽是硬性错误，但为了与 Edit 保持一致，
// 同样经 Grant 表达原因，Execute 侧仍会以 checkSensitiveFiles 硬性拦截。
func (r *Read) Grant(ctx context.Context, params map[string]any) (bool, string) {
	raw, _ := GetParam(params, "filePath")
	filePath, _ := raw.(string)
	if filePath == "" {
		return true, ""
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil {
		return true, ""
	}

	resolved, _ := ResolveTargetPath(filePath, tc.Session.ProjectDir(), tc.Session.SessionDir())
	if resolved == "" {
		return true, ""
	}

	if err := ValidateFileSafety(resolved, tc.Session.ProjectDir()); err != nil {
		for _, dir := range r.whitelist {
			if pathWithinScope(dir, resolved) {
				return true, ""
			}
		}
		if tc.SessionWhitelist != nil {
			for _, allowed := range tc.SessionWhitelist.Read {
				if pathWithinScope(allowed, resolved) {
					return true, ""
				}
			}
		}
		return false, GuideReadOutsideWorkspace(filePath, resolved, err)
	}
	return true, ""
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
//	    ├── 文档格式     → convertDocument → buildTextResult（统一文本处理）
//	    ├── 图片格式     → compressAndEncodeImage（内联压缩）
//	    └── 文本文件     → buildTextResult（分页 + DedupCache + 输出预算）
//
// 参数：
//   - ctx: 上下文（包含 ToolContext）
//   - params: 必须包含 "filePath"，可选 "offset" 和 "limit"
//
// 返回：
//   - *ReadResult: 结构化的读取结果
//   - error: 参数错误、安全校验失败、或文件系统错误
func (r *Read) Execute(ctx context.Context, params map[string]any) (any, error) {
	path, err := ValidateRequiredString("Read", params, "filePath")
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
				Note:       GuideReadFileNotFound(resolvedPath) + "\n（此前已确认）",
				Suggestion: SuggestionFileNotFound,
			},
		}, nil
	}

	// 安全校验：敏感文件硬性拦截。
	// 项目边界检查已由 Grant()（PermissionRequired）完成；授权（Allow / AllowSession）
	// 或白名单路径在此不再重复校验边界，否则授权后执行会被再次拦截。
	if err := checkSensitiveFiles(resolvedPath); err != nil {
		return nil, err
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
					Note:       GuideReadPermissionDenied(resolvedPath),
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
				Note:       GuideReadIsDirectory(resolvedPath),
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
				Note:       GuideReadEmptyFile(resolvedPath),
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
				Note: GuideReadFileTooLarge(
					resolvedPath,
					float64(info.Size())/1024,
					r.limits.MaxSizeBytes/1024,
				),
				Suggestion: SuggestionFileTooLarge,
			},
		}, nil
	}

	// 检测文档格式（PDF/DOCX/XLSX/EPUB），转换为 Markdown 后按纯文本统一处理
	if format := detectDocFormat(resolvedPath); format != "" {
		f, err := store.OS.Open(cleanPath)
		if err != nil {
			return nil, fmt.Errorf("%s", GuideReadIO(resolvedPath, err))
		}
		defer f.Close()

		doc, err := convertDocument(format, f)
		if err != nil {
			return nil, err
		}

		// 文档转换结果与文本文件一样走 buildTextResult 统一处理：
		// 支持 offset/limit 分页精读，超过输出字符预算时返回错误，
		// 不为每种格式单独实现限制逻辑。禁用 DedupCache 与 filestate，
		// 避免转换结果与文件原始状态混淆。
		return r.buildTextResult(resolvedPath, cleanPath, []byte(doc.content), info.Size(), scope, params, false, &docMeta{
			format: format,
			title:  doc.title,
			author: doc.author,
			pages:  doc.pages,
		})
	}

	// 图片文件读取（受 EnableImageReading 控制，内联处理，不写入 DedupCache）
	if r.limits.EnableImageReading && isImageFile(resolvedPath) {
		data, err := store.ReadFileFromFS(store.OS, resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("%s", GuideReadIO(resolvedPath, err))
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

	// 文本文件读取（与文档转换共用 buildTextResult 统一处理：offset/limit 分页、
	// 输出字符预算检查、DedupCache 与 filestate 状态跟踪）
	data, err := store.ReadFileFromFS(store.OS, resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("%s", GuideReadIO(resolvedPath, err))
	}
	return r.buildTextResult(resolvedPath, cleanPath, data, info.Size(), scope, params, true, nil)
}
