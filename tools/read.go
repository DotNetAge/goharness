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
	"github.com/DotNetAge/goharness/sandbox"
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
			Description: "从本地文件系统读取文件。支持文本、PDF/DOCX/XLSX/EPUB 文档、图片。",
			Prompt: `从本地文件系统读取文件。

支持三类文件：
- **文本文件**（.go, .py, .txt, .json 等）：返回带行号的内容，可用 offset/limit 分页。
- **文档文件**（.pdf, .docx, .xlsx, .epub）：返回文本内容与元数据（title/author/pages 等），可用 offset/limit 分页精读。
- **图片文件**（.png, .jpg, .gif, .bmp, .webp, .svg）：返回 base64 编码数据。`,
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
//   - validateReadPath：设备文件黑名单、二进制扩展名拦截（功能保护，非安全策略）
//   - 沙箱 EnforceFile：工作区边界、敏感文件等安全策略统一由沙箱强制检查
//
// 项目边界检查由 Grant()（PermissionRequired）负责，Execute 侧不再重复校验，
// 避免授权（PermissionAllow / AllowSession）或白名单放行后执行被再次拦截。
// 会话未注入沙箱时返回错误（安全决策统一收口到沙箱，工具自身不做授权检查）。
// 供增强版工具（如 mindx 的 ReadPro）在自行实现大文件读取分支时复用。
func (r *Read) CheckSafety(ctx context.Context, resolvedPath string) error {
	if err := validateReadPath(resolvedPath); err != nil {
		return err
	}
	sb, err := requireSandbox(ctx, "Read")
	if err != nil {
		return err
	}
	// 透传工具白名单 + 会话白名单：授权（PermissionAllowSession）与
	// 工具白名单（AddWhiteList）放行的读取在 Execute 阶段同样豁免边界检查。
	extra := r.appendReadWhitelists(ctx)
	return sb.EnforceFileWithWhitelist(resolvedPath, GetToolContext(ctx).Session.ProjectDir(), extra)
}

// appendReadWhitelists 合并工具白名单（AddWhiteList）与会话白名单，供 Execute 阶段
// 的沙箱边界检查豁免。返回新切片，避免修改白名单内部共享切片。
func (r *Read) appendReadWhitelists(ctx context.Context) []string {
	extra := make([]string, 0, len(r.whitelist)+2)
	extra = append(extra, r.whitelist...)
	return append(extra, sessionWhitelistDirs(ctx, "read")...)
}

// Grant 实现 tools.PermissionRequired 接口。
//
// 与 Edit / Bash 保持一致的授权语义：文件安全决策（工作区边界、敏感文件拦截）
// 统一由沙箱 CheckFile 负责。越界访问（AskUser）时先放行工具白名单
// （AddWhiteList）与会话级白名单（PermissionAllowSession 记忆）内的路径，
// 其余越界读取触发授权流程（返回 granted=false，运行时挂起思考循环等待
// 用户通过 PermissionAllow / PermissionAllowSession / PermissionDeny 魔法词回应）。
//
// 会话未注入沙箱时直接放行，由 Execute 阶段拒绝执行（配置错误，授权无意义）。
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

	// 沙箱启用时，由沙箱统一做文件安全决策；
	// 未注入沙箱时直接放行，由 Execute 阶段拒绝执行（配置错误，授权无意义）。
	sb := tc.Session.Sandbox()
	if sb == nil {
		return true, ""
	}

	dec := sb.CheckFile(resolved, tc.Session.ProjectDir())
	switch dec.Decision {
	case sandbox.DecisionAllow:
		return true, ""
	case sandbox.DecisionDeny:
		return false, dec.Reason
	case sandbox.DecisionAskUser:
		// 越界访问，检查白名单
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
		return false, dec.Reason
	}
	return true, ""
}

// authorizeRead 解析文件路径并执行前置校验（设备/二进制）与沙箱强制检查。
func (r *Read) authorizeRead(ctx context.Context, path string) (resolvedPath string, scope PathScope, err error) {
	tc := GetToolContext(ctx)
	resolvedPath, scope = ResolveTargetPath(path, tc.Session.ProjectDir(), tc.Session.SessionDir())

	// A. 零 I/O 前置校验（设备文件黑名单、二进制扩展名）
	// 沙箱启用时设备文件由沙箱 EnforceFile 接管，但二进制扩展名检查保留（功能保护非安全决策）。
	if err = validateReadPath(resolvedPath); err != nil {
		return "", "", err
	}

	// B. 安全校验：工作区边界、敏感文件等策略统一由沙箱 EnforceFile 强制检查
	// （含符号链接解析，防 TOCTOU）。未注入沙箱时拒绝执行。
	sb, err := requireSandbox(ctx, "Read")
	if err != nil {
		return "", "", err
	}
	// 透传工具白名单 + 会话白名单：授权（PermissionAllowSession）与
	// 工具白名单（AddWhiteList）放行的读取在 Execute 阶段同样豁免边界检查。
	if err := sb.EnforceFileWithWhitelist(resolvedPath, tc.Session.ProjectDir(), r.appendReadWhitelists(ctx)); err != nil {
		return "", "", err
	}
	return resolvedPath, scope, nil
}

// performRead 执行文件读取核心逻辑：NegativeCache、stat 检查、权限验证、格式检测与内容读取。
func (r *Read) performRead(ctx context.Context, params map[string]any, resolvedPath string, scope PathScope) (any, error) {
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

	// Pre-read: stat 检查
	cleanPath := strings.TrimLeft(filepath.ToSlash(resolvedPath), "/")
	info, err := fs.Stat(store.OS, cleanPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// C. ENOENT 兜底（简化版，见过度设计审计 #7）
			setNegativeCache(resolvedPath)
			message := ENOENTSuggestion(resolvedPath, GetToolContext(ctx).Session.ProjectDir())
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

// Execute 编排 Read 工具执行流程：validate → authorize → perform。
func (r *Read) Execute(ctx context.Context, params map[string]any) (any, error) {
	path, err := ValidateRequiredString("Read", params, "filePath")
	if err != nil {
		return nil, err
	}

	resolvedPath, scope, err := r.authorizeRead(ctx, path)
	if err != nil {
		return nil, err
	}

	logger := getLogger(ctx)
	logger.Info("reading file",
		"input_path", path,
		"resolved_path", resolvedPath,
		"scope", scope,
	)

	return r.performRead(ctx, params, resolvedPath, scope)
}
