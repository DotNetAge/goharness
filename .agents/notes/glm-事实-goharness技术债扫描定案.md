# goharness 技术债扫描定案（2026-11-03）

- 分类：事实 / 待办线索
- 扫描范围：core/goharness 全部非 test .go（agents/session/events/rpc/tools），三维度：自认降级注释、结构字段零值孤儿、静默吞错

## 真债（唯一实证，2026-11-03 已修复并发版 v0.2.55）

- **usage 估算未下沉**：agents/event_bridge.go recordUsage——provider 未返回 usage 时仅跳过记账与持久化（注释自认"阶段一降级"）。已修：writeBackNewMessages 对位估算补齐（prompt=响应前全量对话窗口、completion=响应文本+思考流，复用新导出的 session.EstimateTokens 与压缩估算同口径），Estimated 标记（TokenUsage/TokenUsageRecord 各加字段，additive JSON），消息回填、记账、TokenUsageRecorded 广播三处同源。已知低估项：prompt 不含系统提示与工具 schema。回归测试 TestWriteBackNewMessagesEstimatesMissingUsage。mindx 已升 v0.2.55。

## 已知限制（文档化，非债）

- tool_adapter.go「阶段一语义说明」：旧核 sync/async 双超时与 IsAsync 并发调度**未在适配器复刻**，超时保护由工具自身与 ctx 取消链路承载——并行工具执行能力与旧核存在差异。
- runtime.go：本地模型（IsLocal）跳过 SubAgent/Team 工具注册（有意约束）。
- prompt_assembler.go：图片文件缺失降级为文本占位不阻断对话（有意、行为明确）。

## 排除项（疑似非债）

- loop.go「goagent 的 System 留空」：效果一致（system 随 History 注入），已闭环。
- permission.go 多处「静默消费」：有意的授权语义设计，带日志。
- event_bridge Emit 直通「满足阶段一」：当前设计选择，非欠账。
- session.Message 字段赋值覆盖：Usage 修复（v0.2.54）后无零值孤儿字段；RunResult 字段完整。

## 下次我看到「token 统计对不上 / 某模型用量缺失」，我将

先查该 provider 是否返回 usage（recordUsage 的 ev.Usage == nil 跳过分支），再决定是否把内容长度估算下沉到 clientAdapter 层。
