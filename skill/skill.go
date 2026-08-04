package skill

import "errors"

// Skill 相关错误
var (
	ErrSkillNotFound    = errors.New("技能未找到")
	ErrSkillExecution   = errors.New("技能执行失败")
	ErrSkillCompilation = errors.New("技能编译失败")

// Skill 表示扩展 agent 行为的专用能力。
// 它遵循 Agent Skills 规范（agentskills.io）进行发现和加载。
//
// Skill 从包含 SKILL.md 文件的目录中加载，该文件包含 YAML 前置元数据
// 和 Markdown 正文（指令）。规范定义了三级渐进式披露：
//  1. 元数据（约 100 tokens）：启动时加载的名称和描述
//  2. 指令（建议 < 5000 tokens）：激活时加载的 SKILL.md 正文
//  3. 资源（按需）：scripts/、references/、assets/ 中按需加载的文件
)

// Requires 声明技能的运行时依赖。
// 如果声明的依赖未满足，技能在加载时会被跳过。
type Requires struct {
	Bins []string `json:"bins,omitempty" yaml:"bins,omitempty"` // PATH 中必需的可执行文件，例如 ["python3", "git"]
	Env  []string `json:"env,omitempty" yaml:"env,omitempty"`   // 必需的环境变量，例如 ["MY_API_KEY"]
}

type Skill struct {
	// --- 规范必需字段（来自 SKILL.md 前置元数据） ---

	Name        string `json:"name" yaml:"name"`               // 必需。最长 64 字符。仅允许小写字母、数字、连字符。
	Description string `json:"description" yaml:"description"` // 必需。最长 1024 字符。描述技能的用途及使用时机。

	// --- 规范可选字段 ---

	Paths         []string          `json:"paths,omitempty" yaml:"paths,omitempty"`                 // 可选。要加载的文件或目录的相对路径列表。
	License       string            `json:"license,omitempty" yaml:"license,omitempty"`             // 许可证名称或对内置许可证文件的引用。
	Compatibility string            `json:"compatibility,omitempty" yaml:"compatibility,omitempty"` // 环境要求（最长 500 字符）。
	Metadata      map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`               // 任意键值对元数据。
	AllowedTools  string            `json:"allowed_tools,omitempty" yaml:"allowed_tools,omitempty"` // 空格分隔的预批准工具（实验性）。

	// --- MindX 扩展 ---

	Requires *Requires `json:"requires,omitempty" yaml:"requires,omitempty"` // 运行时依赖声明。未满足时技能在加载时被跳过。

	// --- 指令（前置元数据之后的 Markdown 正文） ---

	Instructions string `json:"instructions" yaml:"-"` // 从 SKILL.md 正文加载的 Markdown 格式指令。

	// --- 运行时字段（非规范定义，内部使用） ---

	RootDir string `json:"-"`                // 磁盘上技能目录的绝对路径（内置/嵌入式技能为空）。
	Source  string `json:"source,omitempty"` // "bundled" 或 "filesystem"。
}
