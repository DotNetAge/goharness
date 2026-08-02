package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/sandbox"
	"github.com/DotNetAge/goharness/tools/diffutil"
	"github.com/DotNetAge/goharness/tools/filestate"
)

// Write 实现了文件写入工具。
//
// 改进（见 WRITE_DESIGN.md）：
//   - 读前写约束：覆盖已有文件前必须已读取（A 节）
//   - 语义化输出：区分 create / overwrite / append（B 节）
//   - Create vs Update 语义：返回 WriteResult.Type（B 节）
//   - Diff 生成：overwrite 场景返回 unified diff（C 节，P2）
//   - 追加尾换行检测：追加时检测文件末尾换行（M3）
//
// 安全级别：LevelSensitive（敏感），因为会修改文件系统
type Write struct {
	info      *ToolInfo
	whitelist []string
}

// AddWhiteList 添加允许写入的目录前缀。
// 当目标路径匹配任一白名单前缀时，Grant() 会直接放行而无需用户确认。
// 通常在工具初始化后、注册到 ToolRegistry 之前调用。
func (w *Write) AddWhiteList(dirs ...string) *Write {
	w.whitelist = append(w.whitelist, dirs...)
	return w
}

// writeDescription 是 Write 工具的简短描述。
const writeDescription = `将内容写入文件。自动创建父目录。使用 append=true 进行追加而不是覆盖。`

// NewWriteTool 创建一个文件写入工具实例。
//
// 返回：
//   - *Write: 配置好的 Write 工具实例
func NewWriteTool() *Write {
	return &Write{
		info: &ToolInfo{
			Name:        "Write",
			Description: writeDescription,
			Prompt: `将文件写入本地文件系统。

**用法**
- Write 用于创建新文件或完全重写已有文件。
- 覆盖已有文件前必须先用 Read 工具读取文件。Write 会自动检测读取状态，未读取时拒绝操作。
- 修改已有文件的小部分内容时，优先使用 Edit 工具（只发送差异，更安全）。
- 使用 append=true 在文件末尾追加内容（追加模式不要求先读取）。
- 写入会返回变更差异（diff），请通过 diff 验证写入是否正确。
- 仅在用户明确要求时使用表情符号。`,
			Tags:          []string{"file", "filesystem", "write", "create"},
			SecurityLevel: events.LevelSensitive,
			Parameters: []Parameter{
				{Name: "filePath", Type: "string", Description: "要写入的绝对文件路径。", Required: true},
				{Name: "content", Type: "string", Description: "要写入的文件内容。", Required: true},
				{Name: "append", Type: "boolean", Description: "如果为 true，则追加到现有文件而不是覆盖。", Required: false},
			},
		},
	}
}

// Grant implements tools.PermissionRequired.
// 与设计保持一致：硬拒绝（敏感文件 .env/.ssh）在 Execute 中处理，不在 Grant 中重复。
//
// 注意：参数名必须与 ToolInfo.Parameters 定义（"filePath"）及 Execute 取参保持一致。
// 早期版本此处误用 "file_path" 导致 Grant 永远取不到路径，安全检查被绕过（沙箱启用时同样失效）。
func (w *Write) Grant(ctx context.Context, params map[string]any) (bool, string) {
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

	// 沙箱启用时，由沙箱统一做文件安全决策。
	// 注意：Write 语义是"创建或覆盖"，沙箱 CheckFile 对不存在的文件返回 Allow
	// （让 Execute 走创建路径），对存在的越界文件返回 AskUser。
	if sb := tc.Session.Sandbox(); sb != nil {
		dec := sb.CheckFile(resolved, tc.Session.ProjectDir())
		switch dec.Decision {
		case sandbox.DecisionAllow:
			return true, ""
		case sandbox.DecisionDeny:
			return false, dec.Reason
		case sandbox.DecisionAskUser:
			for _, dir := range w.whitelist {
				if pathWithinScope(dir, resolved) {
					return true, ""
				}
			}
			if tc.SessionWhitelist != nil {
				for _, allowed := range tc.SessionWhitelist.Write {
					if pathWithinScope(allowed, resolved) {
						return true, ""
					}
				}
			}
			return false, dec.Reason
		}
	}

	if err := ValidateFileSafety(resolved, tc.Session.ProjectDir()); err != nil {
		for _, dir := range w.whitelist {
			if pathWithinScope(dir, resolved) {
				return true, ""
			}
		}
		if tc.SessionWhitelist != nil {
			for _, allowed := range tc.SessionWhitelist.Write {
				if pathWithinScope(allowed, resolved) {
					return true, ""
				}
			}
		}
		return false, GuideWriteOutsideWorkspace(filePath, resolved, err)
	}
	return true, ""
}

// Info 返回 Write 工具的元信息。
func (w *Write) Info() *ToolInfo {
	return w.info
}

// Execute 执行文件写入操作。
//
// 处理流程（严格遵循 WRITE_DESIGN.md 数据流图）：
//
//	params 进入 → Grant → 参数校验 → 路径解析 + 安全校验 → MkdirAll
//	    │
//	    ├── append=true → 追加模式
//	    │   ├── 跳过读前检查
//	    │   ├── M3：检测文件是否以 \n 结尾
//	    │   │   ├── 不以 \n 结尾 → 在 content 前加 "\n"
//	    │   │   └── 正常 → 原样追加
//	    │   └── os.OpenFile(O_APPEND|O_CREATE) → 写入 → 返回 WriteResult{Type:"append"}
//	    │
//	    └── append=false
//	        ├── 文件已存在？
//	        │   ├── 是（overwrite）
//	        │   │   ├── filestate.CheckStale 读前检查
//	        │   │   ├── 读取原内容用于 diff
//	        │   │   └── os.Create → 写入 → 生成 diff → 清除 StaleState
//	        │   │
//	        │   └── 否（create）
//	        │       ├── 跳过读前检查（审计 m4：记录 INFO 日志）
//	        │       └── os.Create → 写入 → 记录 StaleState
//	        │
//	        └── 返回 WriteResult{Type:"create"|"overwrite"}
//
// 参数：
//   - ctx: 上下文（包含 ToolContext）
//   - params: 必须包含 "filePath" 和 "content"，可选 "append"
//
// 返回：
//   - *WriteResult: 结构化的写入结果（见 content_types.go）
//   - error: 参数错误、路径验证失败、读前检查失败或 I/O 错误
func (w *Write) Execute(ctx context.Context, params map[string]any) (any, error) {
	filePath, err := ValidateRequiredString("Write", params, "filePath")
	if err != nil {
		return nil, err
	}

	content, err := ValidateRequiredString("Write", params, "content")
	if err != nil {
		return nil, err
	}

	logger := getLogger(ctx)

	tc := GetToolContext(ctx)
	resolvedPath, scope := ResolveTargetPath(filePath, tc.Session.ProjectDir(), tc.Session.SessionDir())

	logger.Info("writing file",
		"input_path", filePath,
		"resolved_path", resolvedPath,
		"scope", scope,
		"content_len", len(content),
	)

	// 安全校验：沙箱启用时由 EnforceFile 统一检查（含符号链接解析，防 TOCTOU）。
	if sb := tc.Session.Sandbox(); sb != nil {
		if err := sb.EnforceFile(resolvedPath, tc.Session.ProjectDir()); err != nil {
			return nil, err
		}
	} else if err := checkSensitiveFiles(resolvedPath); err != nil {
		return nil, err
	}

	// 确保父目录存在
	dir := filepath.Dir(resolvedPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("创建父目录", dir, err), err)
	}

	// 判断写入模式
	appendMode := false
	if raw, found := GetParam(params, "append"); found {
		if v, ok := raw.(bool); ok {
			appendMode = v
		} else if v, ok := raw.(string); ok {
			appendMode = v == "true" || v == "1"
		} else if v, ok := raw.(float64); ok {
			appendMode = v != 0
		}
	}

	var writeType string
	var diffStr string

	if appendMode {
		// ── 追加模式 ──
		writeType = "append"

		// M3：检测文件是否以 \n 结尾
		if info, statErr := os.Stat(resolvedPath); statErr == nil && info.Size() > 0 {
			orig, readErr := os.ReadFile(resolvedPath)
			if readErr == nil {
				origStr := string(orig)
				if !strings.HasSuffix(origStr, "\n") {
					content = "\n" + content
					logger.Info("append M3: prepended newline to content",
						"path", resolvedPath,
					)
				}
			}
		}

		// 写入（追加）
		file, openErr := os.OpenFile(resolvedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if openErr != nil {
			return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("打开文件以追加内容", resolvedPath, openErr), openErr)
		}
		bytesWritten, writeErr := file.WriteString(content)
		file.Close()
		if writeErr != nil {
			return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("向文件追加内容", resolvedPath, writeErr), writeErr)
		}

		info, statErr := os.Stat(resolvedPath)
		if statErr != nil {
			return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("获取文件状态", resolvedPath, statErr), statErr)
		}

		logger.Info("file appended",
			"path", resolvedPath,
			"bytes_written", bytesWritten,
		)

		return &WriteResult{
			Success:      true,
			Type:         writeType,
			FilePath:     resolvedPath,
			Scope:        string(scope),
			BytesWritten: bytesWritten,
			TotalSize:    info.Size(),
		}, nil
	}

	// ── 覆盖 / 创建模式 ──
	// 检查文件是否已存在
	_, statErr := os.Stat(resolvedPath)
	if statErr == nil {
		// 文件已存在 → overwrite 路径
		writeType = "overwrite"

		// A. 读前写约束：filestate.CheckStale
		if err := filestate.CheckStale(resolvedPath); err != nil {
			return nil, err
		}

		// 读取原始内容（用于 diff）
		orig, readErr := os.ReadFile(resolvedPath)
		if readErr == nil {
			origStr := string(orig)

			// C. 生成 unified diff（P2，超过 8KB 截断）
			if len(origStr) > 0 && len(origStr) <= 8*1024 {
				_, d := diffutil.GenerateDiff(origStr, content)
				diffStr = d
			}
		}
	} else if os.IsNotExist(statErr) {
		// 文件不存在 → create 路径
		writeType = "create"
		// 审计 m4：记录 INFO 日志
		logger.Info("creating new file",
			"path", resolvedPath,
		)
	} else {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("访问", resolvedPath, statErr), statErr)
	}

	// 执行写入
	if err := os.WriteFile(resolvedPath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("写入", resolvedPath, err), err)
	}

	// 写入后清除 StaleState（保证后续操作重新读取）
	filestate.DeleteStale(resolvedPath)

	// 写入后记录新的 StaleState（供后续 overwrite 使用）
	filestate.SetStale(resolvedPath, time.Now(), []byte(content))

	// 清除 NegativeCache
	invalidateNegativeCache(resolvedPath)

	// 获取写入后的文件大小
	newInfo, statErr := os.Stat(resolvedPath)
	totalSize := int64(0)
	if statErr == nil {
		totalSize = newInfo.Size()
	}

	logger.Info("file written",
		"path", resolvedPath,
		"type", writeType,
		"total_size", totalSize,
	)

	result := &WriteResult{
		Success:      true,
		Type:         writeType,
		FilePath:     resolvedPath,
		Scope:        string(scope),
		BytesWritten: len(content),
		TotalSize:    totalSize,
	}
	if diffStr != "" {
		result.Diff = diffStr
	}
	return result, nil
}
