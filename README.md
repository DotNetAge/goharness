<div align="center">

# goharness

**Go 语言的 AI Agent 运行时（Harness）框架。**

[![Go Report Card](https://goreportcard.com/badge/github.com/DotNetAge/goharness)](https://goreportcard.com/report/github.com/DotNetAge/goharness)
[![Go Version](https://img.shields.io/badge/go-1.25+-blue.svg)](https://golang.org/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[**Website**](https://goharness.rayainfo.cn) | [**English**](./README.md) | [**中文说明**](./README_zh-CN.md)

</div>

---

## goharness 是什么

goharness 是构建在 [goagent](https://github.com/DotNetAge/goagent) 引擎之上的智能体运行时。执行内核（Think Loop 循环骨架、LLM 交互、工具调度）由 goagent 负责，goharness 专注于运行时的全部工程化关切：

- **内置工具集**：文件、终端、搜索、交互等 20+ 开箱即用的工具
- **统一沙箱安全**：所有工具的安全决策收口到一处，Grant / Execute 两阶段一致
- **子代理编排**：异步派发、控制平面结算、结果收集、级联强停
- **会话与上下文**：持久化 SPI、上下文压缩、Token 用量追踪
- **事件总线**：全链路 ReactEvent 事件流，支撑 UI 实时渲染

宿主应用（CLI、服务端、桌面端）通过 `agents.Runtime` 一个入口组装自己的 Agent，其余皆可插拔。

### 依赖分层

```text
宿主应用（CLI / 服务端 / 桌面端）
        │
        ▼
    goharness   ← 本仓库：运行时编排 / 工具 / 沙箱 / 会话 / 事件 / 子代理
        │
        ▼
     goagent    ← 执行内核：Think Loop / LLMClient / Runtime 控制平面 / subagent 协议
        │
        ▼
     gochat     ← LLM 流式客户端（OpenAI 兼容协议）
```

---

## 核心特性

**goagent 内核驱动**
思考-工具循环（Think Loop）由 goagent 引擎驱动。goharness 侧（`loop.go`）只负责会话上下文准备、Agent 装配、事件桥接、消息回写与终止原因映射——内核与运行时职责清晰分离。

**统一沙箱安全**
所有工具的安全决策统一收口到 `sandbox.Sandbox`，工具自身不做任何授权检查：工作区目录边界、敏感文件拦截、危险命令模式、命令白名单、网络 URL 预检（SSRF 防护）。安全决策分两阶段——**Grant 阶段**（工具执行前：Allow / Deny / AskUser）与 **Execute 阶段**（实际执行时的边界强校验），两阶段判定严格一致。会话未注入沙箱时一律拒绝执行，不存在"未授权即放行"的分支。

**SubAgent 异步编排**
`SubAgent` 工具同步受理派发请求，立即返回含跟踪句柄（`task_id`）的回执；子任务在后台独立运行，经 goagent 控制平面（`RuntimeManager`）结算为 Completed / Failed / Cancelled。`CollectResults` 按句柄精确读取结果——等待控制平面终态信号，**零轮询、零状态猜测**。主会话被用户停止时，其派生的全部子代理级联强停。

**中断-恢复交互**
子代理执行中遇到需授权的工具或需要用户澄清的问题时，**挂起等待**而非直接终止：授权请求 / 提问经旁路通道直达前端，用户决策（允许 / 拒绝 / 回答）送达后子代理继续执行循环。主会话则采用「结束本轮 → 魔法词 / 用户消息恢复」的回合制交互。

**全链路事件流**
完整的 ReactEvent 事件体系：`ThinkingDelta`（思维链片段）、`ToolExecStart/End`（工具执行）、`PermissionPending`（待授权）、`AskUserPending`（待提问）、`SubtaskSpawned/Completed`（子任务生命周期）、`LLMRetry`（重试）、`ExecutionSummary`（执行汇总）等，宿主可据此实时渲染 Agent 的完整执行过程。

**Hook 扩展体系**
`LoopHook`（BeforeLLM / AfterLLM / Abort：干预每轮 LLM 调用前后，支持中止循环）与 `ToolHook`（Grant / Before / After：干预工具授权与执行的完整链路），均支持优先级排序。

**上下文工程**
- `Compactor` SPI：上下文逼近窗口上限时由宿主实现的摘要压缩器接管
- KV 前缀缓存友好：消息序列严格稳定（DeepSeek / 通义千问 / 豆包官方前缀缓存要求）
- Token 用量逐次持久化（`TokenUsageStore`），成本可追溯

**SPI 收窄设计**
会话持久化（`SessionStore`）、记忆（`Memory`）、技能检索（`SkillRegistry`）、模型提供商（`ProviderRegistry`）、LLM 客户端（`LLMClient`）全部由宿主注入实现——goharness 对具体应用零依赖，只定义契约。

**渐进式技能加载**
SKILL.md 格式（YAML frontmatter + Markdown 正文），支持文件系统目录与 Go embed 两种来源；技能由宿主注册，goharness 仅在需要时经 `GetSkill` 检索。

**模型无关**
`ModelConfig` + `ProviderRegistry` 描述多提供商模型（能力标记：函数调用 / 结构化输出 / 联网搜索 / 视觉理解 / 本地部署）；也可用 `WithLLMClient` 直接替换底层客户端，便于测试 mock 与协议适配。

---

## 快速开始

### 安装

```bash
go get github.com/DotNetAge/goharness
```

### 最小示例

```go
package main

import (
	"fmt"

	"github.com/DotNetAge/goharness/agents"
	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
	"github.com/DotNetAge/goharness/session"
)

func main() {
	logger := logging.DefaultLogger()
	workDir := "/path/to/project"

	// 1. 沙箱：限定工作区边界（所有工具的安全决策都由它统一做出）
	sb, err := sandbox.NewSandbox(&sandbox.SandboxPolicy{
		AllowedDirs: []string{workDir},
	}, logger)
	if err != nil {
		panic(err)
	}

	// 2. Runtime：装配模型、沙箱与会话存储
	//    （SessionStore 为宿主实现的 SPI，负责消息持久化，此处以 myStore 代替）
	rt := agents.NewRuntime(
		agents.WithModel(config.ModelConfig{
			Name:         "gpt-4o",
			Provider:     "openai", // BaseURL / APIKey 可显式指定，否则从 Provider 继承
			ContextLength: 128000,
			FuncCalling:  true,
		}),
		agents.WithSandbox(sb),
		agents.WithSessionStore(myStore),
		agents.WithLogger(logger),
	)

	// 3. 会话：绑定 Agent 与工作目录，注入沙箱（主会话由调用方创建）
	sess, err := session.New("main-agent", "", workDir, myStore, logger,
		session.WithSandbox(rt.Sandbox()))
	if err != nil {
		panic(err)
	}

	// 4. 提问：AskBuilder 链式订阅事件，Run 返回完整结果
	result, err := rt.Ask("main-agent", "分析这个项目的目录结构并给出改进建议", sess).
		OnThinking(func(chunk string) { fmt.Print(chunk) }).
		OnToolStart(func(d events.ToolExecStartData) {
			fmt.Printf("\n[工具 %s]\n", d.ToolName)
		}).
		Run()
	if err != nil {
		panic(err)
	}

	fmt.Printf("\n答案: %s\n轮次: %d  耗时: %s  终止原因: %s\n",
		result.Answer, result.Iterations, result.Duration, result.TerminationReason)
}
```

### 订阅完整事件流

`OnEvent` 捕获全部事件，也可用 `OnXXX` 按类型订阅：

```go
rt.Ask("main-agent", "重构 auth 模块", sess).
	OnEvent(func(e events.ReactEvent) {
		// 全事件出口：可统一转发到前端事件通道
	}).
	OnContent(func(chunk string) { fmt.Print(chunk) }).
	OnToolEnd(func(d events.ToolExecEndData) {
		fmt.Printf("[%s 完成] %s\n", d.ToolName, d.Summary)
	}).
	OnSubtaskSpawned(func(info events.SubtaskInfo) {
		fmt.Printf("子代理已受理: task_id=%s session=%s\n", info.TaskID, info.SessionID)
	}).
	OnSubtaskCompleted(func(r events.SubtaskResult) {
		fmt.Printf("子代理完成: %s → %v\n", r.AgentName, r.Success)
	}).
	Run()
```

### 自定义工具

实现 `tools.FuncTool` 接口并注册：

```go
type WeatherTool struct{}

func (t *WeatherTool) Info() *tools.ToolInfo {
	return &tools.ToolInfo{
		Name:        "Weather",
		Description: "查询指定城市的实时天气",
		Parameters: []tools.Parameter{
			{Name: "city", Type: "string", Description: "城市名称", Required: true},
		},
	}
}

func (t *WeatherTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	city, _ := params["city"].(string)
	// 通过 ToolContext 访问会话 / 日志 / KV 存储 / 事件发射器
	tc := tools.GetToolContext(ctx)
	tc.Logger.Info("查询天气", "city", city)
	return map[string]any{"city": city, "weather": "晴 26°C"}, nil
}

// 注册：在默认注册表之上追加
reg := tools.NewDefaultToolRegistry()
_ = reg.Register(&WeatherTool{})
rt := agents.NewRuntime(agents.WithToolRegistry(reg), /* ... */)
```

### 沙箱策略

```go
policy := &sandbox.SandboxPolicy{
	AllowedDirs:      []string{workDir},                  // 工作区边界（符号链接解析后比较）
	DeniedFileGlobs:  []string{".env*", "*.pem", "id_rsa*"}, // 敏感文件拦截
	// 命令白名单、危险命令模式、SSRF 子网黑名单等均有安全默认值
}
sb, _ := sandbox.NewSandbox(policy, logger)
```

安全决策的三种结果：
- **Allow**：直接放行
- **Deny**：硬性拦截（如命中危险命令模式、SSRF 目标），白名单仅豁免目录边界，不豁免硬性禁止
- **AskUser**：冒泡到主会话 / 前端请求授权，用户决策后继续

---

## SubAgent 编排

### 派发与收集

LLM 通过两个配对工具使用子代理（本地模型场景自动排除）：

1. **SubAgent**：同步受理，立即返回回执
   ```json
   {"status": "running", "agent_name": "researcher", "task_id": "task-3f9a…"}
   ```
   子任务在后台独立运行，不阻塞主会话；主会话可继续并行派发多个子代理。

2. **CollectResults**：按 `task_ids` 精确等待控制平面终态
   - Completed → 返回子代理最终答案
   - Failed / Cancelled → 返回失败原因
   - 句柄不存在（如宿主重启）→ 明确提示重新派发

会话管理策略（全自动）：空闲会话复用延续上下文 → 活跃会话新建并行分身 → 持久化会话跨进程恢复。同一会话同一时刻至多被一个任务认领，天然规避并行任务串行撞车。

### 授权与提问冒泡

子代理运行中的两处人工介入点均为**挂起-恢复**语义：

```
子代理 → 遇需授权工具 → 挂起（permissionCh）
                       → 授权请求旁路直达前端
                       → 用户 允许/拒绝
                       → 子代理恢复执行（拒绝时合成"权限被拒绝"工具结果，LLM 自行调整）

子代理 → 调用 AskUser → 挂起（askCh）
                      → 问题直达前端
                      → 用户回答（可多轮）
                      → 回答以 user 消息注入，子代理继续
```

主会话停止时调用 `rt.CancelSubAgents(mainSessionID)`，级联强停其派生的全部子代理。

---

## 内置工具清单

| 类别           | 工具                                            | 说明                                       |
| -------------- | ----------------------------------------------- | ------------------------------------------ |
| **文件操作**   | `Read` `Write` `Edit` `Glob` `Grep` `Ls`        | 读 / 写 / 搜索替换编辑 / 模式搜索 / 内容搜索 / 目录列举 |
| **终端与执行** | `Bash` `RunScript`                              | Shell 命令（超时、输出截断、进程组击杀）/ 脚本执行 |
| **信息获取**   | `WebSearch` `WebFetch`                          | 互联网搜索 / 网页抓取（SSRF 预检 + DNS rebinding 双层防护） |
| **人机交互**   | `AskUser`                                       | 向用户提问（多轮澄清，中断-恢复）          |
| **任务管理**   | `TaskCreate` `TaskList` `TaskGet` `TaskUpdate`  | 会话级任务规划（KV 持久化）                |
| **子代理编排** | `SubAgent` `CollectResults`                     | 异步派发（受理回执）/ 按句柄收集结果       |
| **团队协作**   | `TeamCreate` `TeamDelete` `TeamList` `TeamGetTasks` | 团队登记（成员不自动执行，需 SubAgent 显式派发） |
| **技能**       | `Skill`                                         | 按名称检索 SKILL.md（注入 `SkillRegistry` 时注册） |

> 本地模型（`ModelConfig.IsLocal`）自动排除多 Agent 与任务管理类工具——本地模型通常无法可靠执行多 Agent 并行任务。

---

## Runtime 配置选项

| Option                        | 说明                                                                 |
| ----------------------------- | -------------------------------------------------------------------- |
| `WithModel`                   | 模型配置（`config.ModelConfig`，Provider 继承连接信息）              |
| `WithProviderRegistry`        | 模型提供商注册表（多提供商连接信息）                                 |
| `WithLLMClient`               | 直接替换底层 LLM 客户端（测试 mock / 多协议适配）                    |
| `WithSandbox`                 | 会话级沙箱（**必注入**：未注入时所有工具拒绝执行）                   |
| `WithSessionStore`            | 会话存储 SPI（消息持久化、子会话加载）                               |
| `WithKVStore`                 | 会话级 KV 存储（Task 系列等工具的持久化后端）                        |
| `WithTokenUsageStore`         | Token 用量存储（逐次 LLM 响应后持久化）                              |
| `WithToolRegistry`            | 工具注册表（默认含全部内置工具，可替换 / 扩展）                      |
| `WithSkillRegistry`           | 技能检索注册表（注入后注册 Skill 工具）                              |
| `WithMemory`                  | 记忆体（`memory.Memory` SPI）                                        |
| `WithBaseSystemPrompt`        | 基础系统提示词构造器（身份 / 领域 / 环境等应用语义段唯一入口）       |
| `WithAgentExists`             | 子代理派发前的目标 Agent 存在性校验回调                              |
| `WithExcludeTools`            | 按 Agent 名称解析排除工具集合的回调                                  |
| `WithLoopHooks`               | 循环钩子（BeforeLLM / AfterLLM / Abort）                             |
| `WithToolHooks`               | 工具钩子（Grant / Before / After）                                   |
| `WithLogger`                  | 结构化日志器（默认标准输出 JSON）                                    |

---

## 宿主集成 SPI

goharness 只定义契约，实现由宿主注入：

| SPI                 | 包       | 职责                                                   |
| ------------------- | -------- | ------------------------------------------------------ |
| `session.SessionStore` | `session` | 消息持久化、会话元数据、游标、文件修改追踪         |
| `memory.Memory`     | `memory` | 长期记忆检索（注入 Think 上下文）                      |
| `skill.SkillRegistry` | `skill` | 技能发现与检索（`GetSkill`）                           |
| `agents.LLMClient`  | `agents` | LLM 流式客户端（默认 gochat 实现，可替换）             |
| `session.Compactor` | `session` | 上下文摘要压缩（LLM 摘要，含内部重试）                 |
| `store.KVStore`     | `store`  | 会话级键值持久化（内置文件系统实现 `FileSystemKVStore`） |
| `session.TokenUsageStore` | `session` | Token 用量记录（内置内存 / Noop 实现）            |

`Runtime` 同时暴露宿主管控入口：`Ask`（会话内提问）、`CancelSubAgents`（级联强停）、`Sandbox`（沙箱实例，供主会话注入）。

---

## 项目结构

```
goharness/
├── agents/     # Runtime 编排器、AskBuilder、SubAgent 派发、授权/提问冒泡、压缩
├── tools/      # FuncTool SPI 与 20+ 内置工具
├── sandbox/    # 统一沙箱安全（策略编译、Grant/Execute 两阶段决策、SSRF 防护）
├── session/    # 会话持久化 SPI、消息模型、上下文压缩、Token 用量
├── events/     # ReactEvent 事件总线与全量事件类型
├── hooks/      # LoopHook / ToolHook 扩展体系
├── skill/      # 技能检索契约（SKILL.md 渐进式加载）
├── config/     # ModelConfig / ProviderRegistry
├── memory/     # 记忆体契约
├── store/      # KVStore / FileStore（内置文件系统实现）
└── logging/    # 结构化日志
```

---

## 文档

| 文档                                            | 说明                             |
| ----------------------------------------------- | -------------------------------- |
| [任务机制](./docs/task-mechanism.md)            | Task 系列工具的会话级任务管理    |
| [SubAgent 新机制](./docs/PR-SubAgent-New-Mechanism.md) | 异步派发与控制平面结算设计 |
| [SubAgent 控制平面](./docs/PR-SubAgent-Control-Plane.md) | goagent RuntimeManager 设计 |
| [TODO](./TODO.md)                               | 待办与演进计划                   |

---

## 许可证

[MIT License](./LICENSE)
