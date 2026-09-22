package events

// ReactEventType 标识代理级别事件的类型。
// 它与 gochat/core.StreamEventType 不同，后者工作在 LLM token 级别。
// ReactEvent 工作在代理业务逻辑级别（Think-Act 循环、工具调用、子任务）。
type ReactEventType string

const (
	// ThinkingDelta 是 Think 阶段输出的一个片段（流式）。
	ThinkingDelta ReactEventType = "thinking_delta"

	// ContentDelta 是来自 LLM 响应的流式文本内容片段。
	ContentDelta ReactEventType = "content_delta"

	// ToolUseDelta 是来自 LLM 响应的流式工具调用参数片段。
	ToolUseDelta ReactEventType = "tool_use_delta"

	// ThinkingDone 表示 Think 阶段已完成，完整思考内容可用。
	ThinkingDone ReactEventType = "thinking_done"

	// ToolExecStart 表示某个工具即将开始执行。
	ToolExecStart ReactEventType = "tool_exec_start"

	// ToolExecEnd 表示某个工具已完成执行。
	ToolExecEnd ReactEventType = "tool_exec_end"

	// SubtaskSpawned 表示子代理任务已被创建。
	SubtaskSpawned ReactEventType = "subtask_spawned"

	// SubtaskCompleted 表示子代理任务已完成（成功或失败）。
	SubtaskCompleted ReactEventType = "subtask_completed"

	// FinalAnswer 表示 Reactor 已产生最终答案。
	FinalAnswer ReactEventType = "final_answer"

	// PermissionRequest 表示工具在执行前需要用户授权。
	PermissionRequest ReactEventType = "permission_request"

	// PermissionDenied 表示工具执行被权限系统拒绝。
	PermissionDenied ReactEventType = "permission_denied"

	// AskUserRequest 表示 LLM 正在向用户提出一个或多个问题。
	// 与 PermissionRequest（安全相关）不同，这是一种对话式交互。
	AskUserRequest ReactEventType = "ask_user_request"

	// ExecutionSummary 表示 reactor 已完成，并提供使用统计信息。
	ExecutionSummary ReactEventType = "execution_summary"

	// Error 表示 reactor 级别发生错误。
	Error ReactEventType = "error"

	// LLMTimeout 表示 LLM 调用（Think 阶段）超过了时间限制。
	LLMTimeout ReactEventType = "llm_timeout"

	// LLMCancelled 表示 LLM 调用被用户取消。
	// 与 LLMTimeout（真实超时）不同，这是用户主动发起的中断。
	LLMCancelled ReactEventType = "llm_cancelled"

	// LLMRetry 表示 LLM 建流请求失败后进入退避重试（如服务商限流 429 / 5xx）。
	// 这是可预知的等待，必须冒泡给用户而非静默处理——否则前端会一直
	// 显示「正在处理」，用户无从得知服务端正在重试、还要等多久。
	//
	// Phase 为 "retry"（即将退避等待后重试）或 "recovered"（重试后成功建流，
	// 前端收到后应自动消除重试警告）。重试耗尽仍失败时走 Error 事件收尾。
	//
	// 数据：LLMRetryData
	LLMRetry ReactEventType = "llm_retry"

	// LoopEnd 表示一个完整的 Think-Act 循环已结束。
	// 命名取 loop 而非 cycle：cycle 在计算原语中易与「死循环」混淆，
	// loop 更准确地表达 agent 的「思考-行动循环」语义。
	LoopEnd ReactEventType = "loop_end"

	// TaskSummary 表示已完成任务的自然语言摘要。
	// 这是在 Think-Act 循环结束后为非平凡任务发出的事件。
	TaskSummary ReactEventType = "task_summary"

	// Compaction 表示会话的上下文窗口已被压缩
	// （旧消息被滑出）。当活动窗口超过 maxWindowSize 的 80%
	// 时触发，消息会被裁剪到约 60%。
	Compaction ReactEventType = "compaction"

	// MaxTurnsReached 表示 Think-Act 循环已达到最大迭代次数
	// （MaxTurns）但未产生最终答案。
	// 这不是错误 —— 这是一个正常的边界条件，表示
	// 代理需要更具体的指令，或者任务应被分解为更小的步骤。
	//
	// UI 应将其作为信息性提示（而非错误）显示。
	// 用户可以通过后续消息继续对话。
	//
	// 数据：MaxTurnsReachedData
	MaxTurnsReached ReactEventType = "max_turns_reached"

	// FileModified 表示某个文件已被纳入修改跟踪。
	// 当修改文件的工具（Write、FileEdit）即将执行，
	// 且该文件是新加入会话 ModifyFiles 列表时发出。
	//
	// 数据：FileModifiedData
	FileModified ReactEventType = "file_modified"

	// FileConfirmed 表示备份文件已被删除（变更已确认）。
	// 在会话上调用 ConfirmModify 时发出。
	//
	// 数据：FileConfirmData
	FileConfirmed ReactEventType = "file_confirmed"

	// FileRolledBack 表示文件已从备份恢复。
	// 在会话上调用 Rollback 时发出。
	//
	// 数据：FileRollbackData
	FileRolledBack ReactEventType = "file_rolled_back"

	// TokenUsageRecorded 表示一次 LLM 调用已完成，且其 token
	// 使用量已持久化到 TokenUsageStore。在 Think-Act 循环中
	// 每次单独的 LLM API 调用之后发出。
	//
	// 数据：session.TokenUsageRecord
	TokenUsageRecorded ReactEventType = "token_usage_recorded"

	// AskUserPending 表示 LLM 已调用 AskUser 工具，问题
	// 正在等待用户响应。与 AskUserRequest（阻塞式）不同，
	// 这是一种非阻塞通知 —— 思考循环已被暂停，
	// 用户的回答将作为常规用户消息到达。
	//
	// 数据：AskUserPendingData
	AskUserPending ReactEventType = "ask_user_pending"

	// PermissionPending 表示工具在执行前需要用户授权。
	// 与 PermissionRequest（通过 channel+channel 阻塞）不同，
	// 这是一种非阻塞通知 —— 思考循环已被暂停。
	// 用户通过 UI 上的 Agree/Deny 进行回应，daemon 重新触发循环。
	//
	// 数据：PermissionPendingData
	PermissionPending ReactEventType = "permission_pending"

	// UserMessageSaved 表示用户消息已被追加到会话并持久化。
	// 在用户消息追加后立即发出（仅针对真实用户消息 ——
	// 魔法词不会被追加，因此不会触发此事件）。携带后端消息
	// Timestamp，以便前端将其存储为 backendTimestamp，
	// 并用于 session.delete_round。
	//
	// 数据：UserMessageSavedData
	UserMessageSaved ReactEventType = "user_message_saved"
)

// MaxTurnsReachedData 包含最大轮次事件的详细信息。
type MaxTurnsReachedData struct {
	// TurnsCompleted 是实际执行的迭代次数。
	TurnsCompleted int `json:"turns_completed"`

	// MaxTurns 是配置的最大迭代限制。
	MaxTurns int `json:"max_turns"`

	// Suggestion 是面向用户的可读提示，说明如何继续。
	// 这应在 UI 中作为友好提示（而非错误）显示。
	Suggestion string `json:"suggestion"`
}

// FileModifiedData 携带文件被新纳入修改跟踪时的详细信息。
type FileModifiedData struct {
	// FilePath 是被备份并跟踪的文件的绝对路径。
	FilePath string `json:"file_path"`

	// BackupPath 是原始内容保存的位置。
	BackupPath string `json:"backup_path"`
}

// FileConfirmData 携带文件修改被确认时的详细信息。
type FileConfirmData struct {
	// FilePaths 是被确认的文件（备份已删除）。
	FilePaths []string `json:"file_paths"`
}

// FileRollbackData 携带文件修改被回滚时的详细信息。
type FileRollbackData struct {
	// FilePaths 是从备份恢复的文件。
	FilePaths []string `json:"file_paths"`
}

// UserMessageSavedData 携带刚追加的用户消息的后端 Timestamp。
// 前端将其存储为 metadata.backendTimestamp，以便"撤销本轮"按钮
// 可以使用该 id 调用 session.delete_round。
type UserMessageSavedData struct {
	// Timestamp 是后端消息 id（Message.Timestamp），
	// 供 session.delete_round 用于定位并删除整个轮次。
	Timestamp int64 `json:"timestamp"`
}
