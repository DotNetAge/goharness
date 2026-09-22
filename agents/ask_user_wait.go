package agents

import (
	"context"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/session"
)

// askSinkKeyType 用于在 context 中传递子会话 AskUser 提问的旁路发送器。
// 子会话（b.askCh != nil）触发提问时，优先经它把问题直达前端，而不依赖
// 父 exec EventBus 的存活——父 exec 结束/被取消后其 EventBus 订阅已销毁，
// 原 parentEmit 转发链路会静默丢事件，导致前端收不到提问、子会话只能干等。
type askSinkKeyType struct{}

// AskUserSink 是子会话 AskUser 提问直达前端的发送器类型（镜像 PermissionSink）。
// 由宿主（如 mindx daemon）注入：接收子会话的提问数据（含发起会话 ID），
// 负责广播给前端渲染提问卡片。返回后调用方仍会执行挂起等待逻辑
// （waitForAskUserDecision），回答经 askCh 送达。
type AskUserSink func(data events.AskUserPendingData)

// WithAskSink 将子会话 AskUser 提问的旁路发送器注入 ctx。
func WithAskSink(ctx context.Context, sink AskUserSink) context.Context {
	return context.WithValue(ctx, askSinkKeyType{}, sink)
}

// askWaitTimeout 是子智能体等待用户回答提问的超时时间。
// 与授权等待（permissionWaitTimeout = 10 分钟）同级：超时后子会话以
// ask_timeout 终止并写入终止标记，CollectResults 据此快速判定失败，
// 而不是让主回合无限挂起。
const askWaitTimeout = 10 * time.Minute

// waitForAskUserDecision 挂起子智能体的执行循环，等待用户回答 AskUser 提问。
//
// 子代理提问冒泡的核心（镜像 waitForPermissionDecision）：子会话调用 AskUser
// 时，不在本会话内以 ask_user_pending 结束（那样子 exec 就结束了，主会话的
// CollectResults 会死等，且子任务上下文无法延续），而是经 askSink 直达前端
// 挂起等待。用户作答后 daemon 经 dispatchAskAnswer 把答案送入本通道，
// 答案以 user 消息注入会话后继续循环；超时则以 ask_timeout 终止。
func (rt *Runtime) waitForAskUserDecision(
	ctx context.Context,
	b *AskBuilder,
	askUserData events.AskUserPendingData,
	emit func(events.ReactEventType, any),
) {
	logger := rt.logger

	// 问题直达前端：优先经旁路发送器（不依赖父 exec 存活），
	// 未注入旁路（如测试环境）时退回原 parentEmit 转发链路，行为保持不变。
	if sink := b.askSink; sink != nil {
		sink(askUserData)
	} else {
		emit(events.AskUserPending, askUserData)
	}

	// 登记挂起：daemon 收到用户回答时据此定位本子会话并路由答案。
	rt.subAgents.registerAskWait(b.session.ID(), b.askCh)
	defer rt.subAgents.clearAskWait(b.session.ID(), b.askCh)

	timeout := time.NewTimer(askWaitTimeout)
	defer timeout.Stop()

	logger.Info("子智能体循环挂起: 等待用户回答提问",
		"session", b.session.ID(),
		"timeout", askWaitTimeout.String(),
	)

	select {
	case <-ctx.Done():
		b.resultErr = ctx.Err()
		b.resultTerminationReason = "cancelled"
	case answer := <-b.askCh:
		logger.Info("子智能体收到用户回答",
			"session", b.session.ID(),
			"answer_len", len(answer),
		)
		// 回答以 user 消息注入会话（与主会话「用户回答作为普通消息到达」
		// 的恢复语义一致），循环继续由 LLM 消化回答。
		if err := b.session.Append(ctx, session.Message{
			Role:      "user",
			Content:   answer,
			Timestamp: time.Now().Unix(),
		}); err != nil {
			logger.Error("追加用户回答失败", err, "session", b.session.ID())
			emit(events.Error, "追加用户回答失败: "+err.Error())
			b.resultErr = err
			b.resultTerminationReason = "error"
		}
	case <-timeout.C:
		logger.Warn("子智能体提问等待超时，终止执行",
			"session", b.session.ID(),
			"timeout", askWaitTimeout.String(),
		)
		b.resultTerminationReason = "ask_timeout"
	}
}
