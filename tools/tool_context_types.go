package tools

import (
	"context"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/store"
)

// toolCtxKey 是存储/获取 ToolContext 的上下文键。
type toolCtxKeyType struct{}

var toolCtxKey = toolCtxKeyType{}

// ToolContext 为工具提供运行时依赖的访问能力。
//
// 所有工具通过 context 接收此对象。Session 指针是会话级属性
// （ID、ProjectDir、AgentName、Sponsor 等）的权威来源。
// 将个别 session 字段提取到 ToolContext 会产生泄漏点，
// 副本可能过期或与真实数据源不同步。
type ToolContext struct {
	EmitEvent   func(events.ReactEvent)
	SessionStore session.SessionStore
	KVStore     store.KVStore
	FileStore   store.FileStore
	Logger      logging.Logger

	// Session 是会话级状态的权威来源。
	// 工具通过其 getter 方法（ID、ProjectDir 等）访问会话属性，
	// 确保从单一数据源进行线程安全读取。
	Session *session.Session

	// SessionWhitelist 是会话级工具白名单的缓存（懒加载）引用。
	// PermissionRequired 工具的 Grant() 方法在提示用户前会检查此白名单。
	// nil 表示无可用白名单。
	SessionWhitelist *session.SessionWhitelist
}

// WithToolContext 将 ToolContext 存入给定 context。
func WithToolContext(ctx context.Context, tc *ToolContext) context.Context {
	return context.WithValue(ctx, toolCtxKey, tc)
}

// GetToolContext 从给定 context 中获取 ToolContext。
// 若未设置则返回空的 ToolContext，调用方可安全地
// 通过 tc.Session != nil 判断后再访问会话属性。
func GetToolContext(ctx context.Context) *ToolContext {
	tc, _ := ctx.Value(toolCtxKey).(*ToolContext)
	if tc == nil {
		return &ToolContext{}
	}
	return tc
}
