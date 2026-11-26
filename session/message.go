package session

import "strings"

// ToolCall 表示助手消息中的工具调用。
// 与 gochat/core.ToolCall 镜像，保持线格式兼容。
type ToolCall struct {
	ID        string `json:"id" yaml:"id"`
	Name      string `json:"name" yaml:"name"`
	Arguments string `json:"arguments" yaml:"arguments"` // JSON 编码的参数映射
}

// ImageBlock 是嵌入消息中的图片内容块。
// 图片以 image_url 消息的形式进入上下文（而非混入工具结果的文本内容）：
// 发送前会拼接为 data:<media_type>;base64,<data> 的 data URI。
// Path 与 Base64Data 二选一：Path 非空时按文件路径引用（组装时才读文件转 base64），
// 使持久化消息只存路径引用而不膨胀存储；Base64Data 用于内联场景（如工具结果图片）。
type ImageBlock struct {
	MediaType  string `json:"media_type" yaml:"media_type"`                       // MIME 类型，如 image/png、image/jpeg
	Base64Data string `json:"base64_data,omitempty" yaml:"base64_data,omitempty"` // base64 编码的图片数据（与 Path 二选一）
	Path       string `json:"path,omitempty" yaml:"path,omitempty"`               // 图片文件绝对路径（与 Base64Data 二选一）
	AltText    string `json:"alt_text,omitempty" yaml:"alt_text,omitempty"`       // 图片说明（如尺寸、来源）
}

// Message 表示对话中的单条消息。
// 与 OpenAI 聊天完成格式兼容：
//   - role: 角色（"system" | "user" | "assistant" | "tool"）
//   - content: 消息文本
//   - reasoning_content: 模型的思考/推理流（如 DeepSeek-R1）
//   - tool_calls: 助手消息中的工具调用
//   - tool_call_id: 工具结果消息的关联 ID
type Message struct {
	Role             string       `json:"role" yaml:"role"`
	Content          string       `json:"content" yaml:"content"`
	ReasoningContent string       `json:"reasoning_content,omitempty" yaml:"reasoning_content,omitempty"` // 思考流（DeepSeek-R1 等）
	Images           []ImageBlock `json:"images,omitempty" yaml:"images,omitempty"`                       // 图片内容块（以 image_url 消息进入上下文）
	Timestamp        int64        `json:"timestamp" yaml:"timestamp"`
	ToolCallID       string       `json:"tool_call_id,omitempty" yaml:"tool_call_id,omitempty"` // 用于 role="tool" 消息
	ToolCalls        []ToolCall   `json:"tool_calls,omitempty" yaml:"tool_calls,omitempty"`     // 用于 role="assistant" 消息
	// FinishReason 记录本次 LLM 调用（assistant 消息）的终止原因，取 OpenAI 协议原值：
	// "stop"（最终回答）| "tool_calls"（需继续循环）| "length" | "content_filter"。
	// 这是协议内建的「答案边界」信号：前端恢复历史时据此精确区分过程 content 与最终答案，
	// 无需依赖「最后一条无 tool_calls 的 assistant」这类位置启发式。
	FinishReason string      `json:"finish_reason,omitempty" yaml:"finish_reason,omitempty"`
	Usage        *TokenUsage `json:"token_usage,omitempty" yaml:"token_usage,omitempty"` // token 消耗（仅助手消息）
	// Source 标记这条消息的真实来源，供前端区分「谁在说话」：
	//   "" | "user"——用户/助手正常消息；
	//   "tool_vision"——工具读取的图片以视觉附件形式注入，内容不是用户写的，
	//                  前端应渲染成工具产出而不是用户气泡；
	//   "task_board_nudge"——宿主机追加的任务板提醒，只在请求与历史里生效，
	//                  界面不渲染（它是对模型说的话，不是对用户说的话）。
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
}

// 消息来源标记取值。空值表示用户与助手的正常发言。
const (
	// SourceToolVision 标记工具读取的图片附件：内容由宿主生成，不是用户写的。
	SourceToolVision = "tool_vision"
	// SourceTaskBoardNudge 标记宿主机追加的任务板提醒：对模型说的话，不是用户发言。
	SourceTaskBoardNudge = "task_board_nudge"
	// SourceAskAnswer 标记用户对挂起提问（AskUser）的回答。正常路径下回答以
	// AskUser 的工具结果落库，本就不构成轮边界；只有写入闸门降级（重复应答、
	// 缺 ToolCallID）时才落成 user 消息，靠这个标记继续维持「同一轮」语义。
	SourceAskAnswer = "ask_answer"
	// ToolVisionPrefix 是工具视觉附件消息的固定开头，只用于给模型和用户看的文案。
	// 来源判定优先走结构标记（Message.Origin / Source）；文本前缀只在识别
	// 改动前已落盘的旧数据时作为兜底，见 Message.IsLegacyVisionInjection。
	ToolVisionPrefix = "[工具产出的图像]"
	// legacyVisionPrefix 是改动前旧版视觉注入文案的开头（"以下是工具 Read
	// 读取到的图片内容（视觉消息）…"）。历史数据没有来源标记，只能按文案识别。
	legacyVisionPrefix = "以下是工具"
	// legacyVisionKeyword 与 legacyVisionPrefix 联合判定，降低误命中概率。
	legacyVisionKeyword = "读取到的图片内容"
)

// IsLegacyVisionInjection 识别改动前落盘的旧格式视觉注入消息。
//
// 旧版把工具读到的图片写成一条 role=user 的「用户发言」，没有任何来源标记。
// 这类历史数据如果按 Role=="user" 判轮，刷新后会多切一轮、撤销轮次会截错
// 位置、界面上还会冒充用户气泡。判据要求同时满足：
//   - user 角色、来源为空（新数据一定带 Source）；
//   - 带图片附件（用户自己发的图也有图，但文案不会长这样）；
//   - 正文是两版固定文案之一。
func (m Message) IsLegacyVisionInjection() bool {
	if m.Role != "user" || m.Source != "" || len(m.Images) == 0 {
		return false
	}
	c := strings.TrimSpace(m.Content)
	if strings.HasPrefix(c, ToolVisionPrefix) {
		return true
	}
	return strings.HasPrefix(c, legacyVisionPrefix) && strings.Contains(c, legacyVisionKeyword)
}

// IsRoundAnchor 判定这条消息是否是一次「真实用户发言」，即轮次边界。
//
// 宿主注入的消息在协议上都以 user 角色传输——多模态图片只能挂在 user 消息上，
// 而 OpenAI 兼容层只允许 messages[0] 为 system，所以「提醒」也只能借 user 通道。
// 它们带来源标记（SourceToolVision / SourceTaskBoardNudge），内容由宿主生成。
// 轮次切分、删除轮次、配对修复、用量归组等所有「按 user 找轮边界」的地方都必须
// 用本方法，不能用 Role == "user" 代判，否则会把注入消息当成新一轮的起点。
func (m Message) IsRoundAnchor() bool {
	if m.Role != "user" {
		return false
	}
	switch m.Source {
	case SourceToolVision, SourceTaskBoardNudge, SourceAskAnswer:
		return false
	}
	if m.IsLegacyVisionInjection() {
		return false
	}
	return true
}

// GetID 返回此消息的唯一标识符。
// 注意：Timestamp 为 Unix 秒级时间戳，并发保存时可能与其他消息相同，
// 定位消息时（如 DeleteRound）需结合 Role 判断，不能假定其全局唯一。
func (m Message) GetID() int64 { return m.Timestamp }
