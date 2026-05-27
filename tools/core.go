// Package tools 提供了一套可扩展的工具系统，用于 AI 代理执行各种操作。
//
// 该包实现了工具注册、执行和权限管理的完整框架，支持：
//   - 文件系统操作（读取、写入、编辑、搜索）
//   - Shell 命令执行（带安全白名单）
//   - Web 搜索和内容获取
//   - 用户交互（提问、权限请求）
//   - 子代理调度
//
// 核心概念：
//   - FuncTool: 工具接口，定义工具的元信息和执行逻辑
//   - ToolInfo: 工具描述信息，包含名称、参数、安全级别等
//   - ToolRegistry: 工具注册表，管理工具的注册和查找
//   - ToolExecutor: 工具执行器，负责工具的调用和结果处理
package tools

import (
	"context"

	"github.com/DotNetAge/goreact/events"
)

// FuncTool 定义了工具的基本接口。
// 所有工具都必须实现此接口，以便在 ToolRegistry 中注册和通过 ToolExecutor 执行。
//
// 实现此接口的类型需要提供：
//   - Info(): 返回工具的元信息（名称、描述、参数等）
//   - Execute(): 执行工具逻辑，接收上下文和参数，返回结果或错误
type FuncTool interface {
	// Info 返回工具的元信息，包括名称、描述、参数定义和安全级别。
	// 此方法在工具注册时被调用，用于构建工具索引。
	Info() *ToolInfo

	// Execute 执行工具的核心逻辑。
	//
	// 参数：
	//   - ctx: 上下文，支持取消信号和超时控制，可通过 WithToolContext 注入工具上下文
	//   - params: 工具参数映射，键为参数名，值为参数值（类型为 any）
	//
	// 返回：
	//   - any: 执行结果，可以是字符串、map 或任意类型
	//   - error: 执行过程中的错误
	Execute(ctx context.Context, params map[string]any) (any, error)
}

// ToolInfo 包含工具的完整元信息描述。
// 这些信息用于工具注册表索引、LLM 工具选择、权限检查和用户界面展示。
//
// JSON/YAML 标签支持序列化，便于配置文件和 API 交互。
type ToolInfo struct {
	// Name 是工具的唯一标识符。
	// 在注册表中必须唯一，用于工具查找和调用。
	Name string `json:"name" yaml:"name"`

	// Description 是工具的功能描述。
	// 用于 LLM 理解工具用途，应简洁明了地说明工具的作用。
	Description string `json:"description" yaml:"description"`

	// Prompt 是给 LLM 的详细使用说明。
	// 包含使用示例、最佳实践、注意事项等指导性内容。
	Prompt string `json:"prompt,omitempty" yaml:"prompt,omitempty"`

	// Tags 是工具的分类标签。
	// 用于工具过滤和分组，如 ["file", "search", "web"]。
	Tags []string `json:"tags" yaml:"tags"`

	// SecurityLevel 定义工具的安全级别。
	// 决定是否需要用户授权才能执行。
	SecurityLevel events.SecurityLevel `json:"security_level" yaml:"security_level"`

	// IsIdempotent 表示工具是否幂等。
	// 幂等工具重复执行不会产生副作用，可以安全重试。
	IsIdempotent bool `json:"is_idempotent" yaml:"is_idempotent"`

	// IsAsync 表示工具是否异步执行。
	// 异步工具立即返回 task_id，结果需要后续获取。
	IsAsync bool `json:"is_async,omitempty" yaml:"is_async,omitempty"`

	// Parameters 定义工具接受的参数列表。
	// 每个参数包含名称、类型、是否必需等信息。
	Parameters []Parameter `json:"parameters" yaml:"parameters"`

	// ReturnType 描述返回值的类型。
	// 用于类型检查和文档生成。
	ReturnType string `json:"return_type" yaml:"return_type"`

	// Examples 是使用示例列表。
	// 展示典型调用场景，帮助理解工具用法。
	Examples []string `json:"examples" yaml:"examples"`

	// MaxResultSizeChars 是结果的最大字符数限制。
	// -1 表示无限制，0 使用默认值，正值表示具体限制。
	MaxResultSizeChars int `json:"max_result_size_chars,omitempty" yaml:"max_result_size_chars,omitempty"`

	// IsReadOnly 表示工具是否只读。
	// 只读工具不会修改任何外部状态，安全性更高。
	IsReadOnly bool `json:"is_read_only,omitempty" yaml:"is_read_only,omitempty"`
}

// Parameter 定义工具的单个参数信息。
// 用于自动生成参数验证、文档和 UI 表单。
type Parameter struct {
	// Name 是参数名。
	// 必须与 Execute 方法 params map 中的键对应。
	Name string `json:"name" yaml:"name"`

	// Type 是参数的数据类型。
	// 常见值：string, number, boolean, integer, array, object。
	Type string `json:"type" yaml:"type"`

	// Required 表示参数是否必需。
	// 必需参数缺失时应返回错误。
	Required bool `json:"required" yaml:"required"`

	// Default 是参数的默认值。
	// 当参数未提供且非必需时使用。
	Default any `json:"default" yaml:"default"`

	// Description 是参数的功能描述。
	// 应清晰说明参数的用途、取值范围和格式要求。
	Description string `json:"description" yaml:"description"`

	// Enum 是允许的取值枚举列表。
	// 如果设置，参数值必须是其中之一。
	Enum []any `json:"enum" yaml:"enum"`
}
