package skill

import "errors"

// Skill 相关错误
var (
	ErrSkillNotFound    = errors.New("技能未找到")
	ErrSkillExecution   = errors.New("技能执行失败")
	ErrSkillCompilation = errors.New("技能编译失败")
)

// Skill 表示扩展 agent 行为的专用能力。
// 它遵循 Agent Skills 规范（agentskills.io）进行发现和加载。
//
// 职责切分（PR-PROMPTS）：SKILL.md 的目录扫描与 frontmatter 解析由应用侧
// （mindx）的技能存储负责；goharness 只保留运行时检索所需的最小字段，
// 供 Skill 工具按名称加载技能指令。
//
// 规范定义了三级渐进式披露：
//  1. 元数据（约 100 tokens）：启动时加载的名称和描述
//  2. 指令（建议 < 5000 tokens）：激活时加载的 SKILL.md 正文
//  3. 资源（按需）：scripts/、references/、assets/ 中按需加载的文件
type Skill struct {
	// Name 技能唯一名称。必需。最长 64 字符。仅允许小写字母、数字、连字符。
	Name string `json:"name" yaml:"name"`

	// Description 描述技能的用途及使用时机（用于技能目录摘要）。
	Description string `json:"description" yaml:"description"`

	// Instructions 从 SKILL.md 正文加载的 Markdown 格式指令（Skill 工具返回给 LLM）。
	Instructions string `json:"instructions" yaml:"-"`

	// AllowedTools 空格分隔的预批准工具列表（实验性，Skill 工具随结果透传）。
	AllowedTools string `json:"allowed_tools,omitempty" yaml:"allowed_tools,omitempty"`

	// RootDir 磁盘上技能目录的绝对路径（内置/嵌入式技能为空）。
	RootDir string `json:"-"`

	// Source 技能来源："bundled" 或 "filesystem"。
	Source string `json:"source,omitempty"`
}
