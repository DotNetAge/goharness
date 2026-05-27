package config

// AgentConfig 定义了 AI Agent 的完整配置结构。
// 它包含了 Agent 的身份信息、能力描述、模型绑定以及运行时行为配置。
// AgentConfig 通常从 Markdown 文件（带有 YAML frontmatter）中加载，
// 也可以通过 AgentRegistry.SaveTo 方法持久化到文件系统。
type AgentConfig struct {
	// Name 是 Agent 的唯一标识符，用于在注册表中查找和管理 Agent。
	Name string `json:"name" yaml:"name"`

	// Role 描述了 Agent 的角色定位，例如 "代码助手"、"数据分析师" 等。
	Role string `json:"role" yaml:"role"`

	// Description 提供了 Agent 功能的简要描述，用于展示和搜索匹配。
	Description string `json:"description" yaml:"description"`

	// Introduction 包含 Agent 的详细介绍文本，通常作为 Agent 的默认 Body 内容。
	Introduction string `json:"introduction" yaml:"introduction"`

	// Model 指定了该 Agent 默认使用的模型名称，对应 ModelRegistry 中的模型配置。
	Model string `json:"model" yaml:"model"`

	// Skills 列出了 Agent 具备的技能集合，用于能力匹配和任务路由。
	Skills []string `json:"skills,omitempty" yaml:"skills,omitempty"`

	// Body 存储了 Agent 的详细说明内容，如果为空则使用 Introduction 作为替代。
	Body string `json:"body,omitempty" yaml:"body,omitempty"`

	// EnableOrchestration 控制是否启用多 Agent 协作编排功能。
	EnableOrchestration bool `json:"enable_orchestration" yaml:"enable_orchestration"`

	// MaxDecomposeDepth 定义了任务分解的最大深度层级，0 表示不限制。
	MaxDecomposeDepth int `json:"max_decompose_depth,omitempty" yaml:"max_decompose_depth,omitempty"`

	// Meta 存储了扩展的元数据键值对，可以用于自定义属性和第三方集成。
	Meta map[string]any `json:"meta,omitempty" yaml:"meta,omitempty"`
}
