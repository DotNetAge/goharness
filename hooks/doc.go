// Package hooks 提供 gochat 框架的钩子系统：进程内的循环拦截器。
//
// # 定位
//
// 与进程外事件钩子（spawn 命令、exit code 决定放行）不同，本系统的钩子
// 直接持有循环的一等状态（输入指针、结果快照、迭代序号），可对决策序列
// 做改写、短路、打断三种干预。事件钩子治理副作用边界（approve / deny），
// 本系统治理决策序列本身。
//
// # 挂点
//
//	BeforeLLM → LLM 调用 → 工具执行（ToolHook.Before → 执行 → After）→ AfterLLM → 下一轮
//
// LoopHook 挂迭代边界，ToolHook 挂副作用边界，单 goroutine 顺序执行无需加锁。
//
// # 三种干预（HookResult）
//
//   - 改写：修改 *CallInput / *ToolResult 指针，中途改动后续流程可见的内容
//     （如 MemoryThoughtHook 向 SystemPromptSections 注入项目记忆，写回后
//     下一轮 LLM 请求可见）；
//   - 短路：SkipWithResult 以提供的结果替换工具执行（去重钩子返回缓存）；
//   - 打断：Abort + AbortReason 有序终止循环。
//
// # 优先级
//
// 数值越小越先执行：权限 41 → 循环日志 45 → 工具日志 46 → 图片 47 → 收敛 49。
// 优先级让钩子之间形成编排：权限先行，收敛垫底裁决本轮是否继续。
//
// 完整说明（含挂点图解与内核桥接时机）见 mindx 仓库 docs/thinkloop-hooks.md。
package hooks
