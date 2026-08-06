package session

// ToolCall 表示助手消息中的工具调用。
// 与 gochat/core.ToolCall 镜像，保持线格式兼容。
type ToolCall struct {
	ID        string `json:"id" yaml:"id"`
	Name      string `json:"name" yaml:"name"`
	Arguments string `json:"arguments" yaml:"arguments"` // JSON 编码的参数映射
}

// CompactedMeta 存储 MicroCompact 压缩的工具消息的元数据。
// JSON 编码并存储在 Message.Compacted 中。
type CompactedMeta struct {
	Path       string `json:"path"`        // 缓存文件绝对路径
	ToolName   string `json:"tool_name"`   // 原始工具名称（当 ToolCallID 查找失败时的后备）
	TokenCount int64  `json:"token_count"` // 压缩时的 token 估算
}

// ImageBlock 是嵌入消息中的图片内容块。
// 图片以 image_url 消息的形式进入上下文（而非混入工具结果的文本内容）：
// 发送前会拼接为 data:<media_type>;base64,<data> 的 data URI。
type ImageBlock struct {
	MediaType  string `json:"media_type" yaml:"media_type"`     // MIME 类型，如 image/png、image/jpeg
	Base64Data string `json:"base64_data" yaml:"base64_data"`   // base64 编码的图片数据
	AltText    string `json:"alt_text,omitempty" yaml:"alt_text,omitempty"` // 图片说明（如尺寸、来源）
}

// Message 表示对话中的单条消息。
// 与 OpenAI 聊天完成格式兼容：
//   - role: 角色（"system" | "user" | "assistant" | "tool"）
//   - content: 消息文本
//   - reasoning_content: 模型的思考/推理流（如 DeepSeek-R1）
//   - tool_calls: 助手消息中的工具调用
//   - tool_call_id: 工具结果消息的关联 ID
//   - compacted: JSON 编码的 CompactedMeta，当内容归档到磁盘时非空
type Message struct {
	Role             string      `json:"role" yaml:"role"`
	Content          string      `json:"content" yaml:"content"`
	Compacted        string      `json:"compacted,omitempty" yaml:"compacted,omitempty"`                 // JSON(CompactedMeta)，非空 → 内容已归档
	ReasoningContent string      `json:"reasoning_content,omitempty" yaml:"reasoning_content,omitempty"` // 思考流（DeepSeek-R1 等）
	Images           []ImageBlock `json:"images,omitempty" yaml:"images,omitempty"`                     // 图片内容块（以 image_url 消息进入上下文）
	Timestamp        int64       `json:"timestamp" yaml:"timestamp"`
	ToolCallID       string      `json:"tool_call_id,omitempty" yaml:"tool_call_id,omitempty"` // 用于 role="tool" 消息
	ToolCalls        []ToolCall  `json:"tool_calls,omitempty" yaml:"tool_calls,omitempty"`     // 用于 role="assistant" 消息
	Usage            *TokenUsage `json:"token_usage,omitempty" yaml:"token_usage,omitempty"`   // token 消耗（仅助手消息）
}

// GetID 返回此消息的唯一标识符。
// 注意：Timestamp 为 Unix 秒级时间戳，并发保存时可能与其他消息相同，
// 定位消息时（如 DeleteRound）需结合 Role 判断，不能假定其全局唯一。
func (m Message) GetID() int64 { return m.Timestamp }
