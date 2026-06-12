<div align="center">

# goharness

**高性能、模式驱动的 ReAct (Reasoning + Acting) 引擎，为 Go 而生。**

[![Go Report Card](https://goreportcard.com/badge/github.com/DotNetAge/goharness)](https://goreportcard.com/report/github.com/DotNetAge/goharness)
[![Go Version](https://img.shields.io/badge/go-1.25+-blue.svg)](https://golang.org/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[**Website**](https://goharness.rayainfo.cn) | [**English**](./README.md) | [**中文说明**](./README_zh-CN.md)

</div>

---

## goharness 是什么

goharness 是一个用纯 Go 实现的 AI Agent 框架，核心采用 **T-A-O (Think-Act-Observe)** 循环模式驱动 LLM 进行推理与行动。它的设计目标是帮助开发人员**专注于 Tools 与 Skills 的开发与运用**，内核机制与性能由 goharness 负责，保证以最少量的 Token 收获最大的价值。

### 核心特性

**T-A-O 推理引擎**：Think（意图分析 + 推理决策）→ Act（工具执行）→ Observe（结果评估），循环迭代直到得出最终答案。支持流式输出思维链，客户端可实时展示推理过程。

**五类型记忆系统**：Session（临时）、User（用户偏好）、LongTerm（知识库）、Refactive（工具/技能语义索引）、Experience（成功执行经验自动保存）。Memory 由外部调用者实现，简单 InMemory 到完整 RAG 均可接入。Think 阶段自动检索相关记忆注入 Prompt，抑制幻觉。

**ReNew 语义上下文重建**：当上下文窗口逼近极限时，优先通过 Memory 的 ReNew 接口进行语义重建（从记忆中检索相关上下文重新组装），而非暴力截断。优先级链：ReNew → MicroCompact → Full Compact (LLM 摘要)。

**经验自成长**：任务成功完成后自动将问题描述 + 解决方案保存为经验记忆。Memory 实现可以将经验转化为 Skill（SKILL.md），使 goharness 越用越强。

**三层上下文防御**：Layer 1 工具结果截断与持久化 → Layer 2 大结果溢写到磁盘 → Layer 3 上下文压缩/摘要。防止上下文爆炸。

**多 Agent 团队协作**：支持创建团队、异步派发 SubAgent、Channel 式消息通信、结果收集与综合。

**中断-恢复交互模式**：`ask_user` 工具支持多轮对话澄清，`ask_permission` 工具支持高风险工具授权。

**事件总线**：完整的 ReactEvent 事件流（ThinkingDelta、ActionStart、ToolExecStart/ToolExecEnd、ActionEnd、ExecutionSummary 等），支持客户端实时渲染。

**渐进式 Skill 加载**：YAML Frontmatter + Markdown Body 的 SKILL.md 格式，支持文件系统目录加载和 Go embed 内置加载。

**MCP 协议支持**：通过 MCP 接口接入外部 MCP 服务器，将 MCP 工具自动转换为标准 FuncTool 注册。

---

## 快速开始

### 安装

```bash
go get github.com/DotNetAge/goharness
```

### 入口说明

`goharness.Agent` 是面向开发者的**唯一入口**。Agent 内部自动构造 Reactor（T-A-O 引擎）和所有子系统，开发者通过 `WithXXX` Option 注入配置，不需要了解 Reactor 的存在。

### 5 分钟 Hello World

```go
package main

import (
    "fmt"
    "github.com/DotNetAge/goharness"
)

func main() {
    // 一行创建 Agent，只需 API Key
    agent := goharness.DefaultAgent("your-api-key")

    // 提问，返回 Result（含答案、Token 消耗、步数、耗时）
    result, err := agent.Ask("你好，请介绍一下你自己")
    if err != nil {
        panic(err)
    }

    fmt.Println("Answer:", result.Answer)
    fmt.Println("Tokens:", result.Tokens)
    fmt.Println("Steps:", result.Steps)
    fmt.Println("Duration:", result.Duration)
}
```

### 自定义 Agent（WithConfig + WithModel）

Agent 的多样性由两个核心实体定义：`AgentConfig`（身份与领域）和 `ModelConfig`（LLM 后端）。

```go
package main

import (
    "fmt"
    "github.com/DotNetAge/goharness"
    "github.com/DotNetAge/goharness/core"
)

func main() {
    // 定义 Agent 身份
    config := &core.AgentConfig{
        Name:        "code-reviewer",
        Domain:      "software-engineering",
        Description: "A senior code review assistant",
        SystemPrompt: "You are a senior software engineer who reviews code with rigor.",
    }

    // 定义 LLM 后端
    model := &core.ModelConfig{
        Name:      "gpt-4o",
        APIKey:    "your-api-key",
        BaseURL:   "https://api.openai.com/v1",
        MaxTokens: 16384,
    }

    // 一行创建
    agent := goharness.NewAgent(
        goharness.WithConfig(config),
        goharness.WithModel(model),
    )

    result, _ := agent.Ask("Review this Go function for potential bugs")
    fmt.Println(result.Answer)
    fmt.Println("Tokens used:", result.Tokens)
}
```

### 带记忆的多轮对话

通过 `WithMemory` 注入记忆体，`WithSession` 启用会话管理：

```go
package main

import (
    "fmt"
    "github.com/DotNetAge/goharness"
    "github.com/DotNetAge/goharness/core"
)

func main() {
    // Memory 由外部实现（RAG、向量数据库等），nil 表示不启用记忆
    // agent := goharness.NewAgent(
    //     goharness.WithModel(goharness.DefaultModel()),
    //     goharness.WithMemory(yourMemory),  // 注入你的 Memory 实现
    //     goharness.WithSession("my-session", 8192),
    // )

    // DefaultAgent 已内置会话管理，直接使用即可
    agent := goharness.DefaultAgent("your-api-key")

    // 第一轮
    agent.Ask("记住：我最喜欢的编程语言是 Go")

    // 第二轮：会话上下文自动管理
    result, _ := agent.Ask("我最喜欢什么编程语言？")
    fmt.Println(result.Answer)

    // 查看 Agent 信息
    fmt.Println("Name:", agent.Name())
    fmt.Println("Session:", agent.SessionID())

    // 查看已注册的工具和技能
    tools := agent.Tools()
    fmt.Printf("Registered tools: %d\n", len(tools))
    skills := agent.Skills()
    fmt.Printf("Loaded skills: %d\n", len(skills))
}
```

> 注意：`DefaultAgent` 已内置会话管理，不需要额外传 `WithSession`。上面的示例展示了显式配置的方式。

### 流式输出 + 事件监听

`AskStream` 流式输出文本，`Events` 接收结构化事件流：

```go
package main

import (
    "fmt"
    "github.com/DotNetAge/goharness"
    "github.com/DotNetAge/goharness/core"
)

func main() {
    agent := goharness.DefaultAgent("your-api-key")

    // 订阅事件流（在 Ask 之前调用）
    events, cancel := agent.Events()
    defer cancel()

    // 后台监听事件
    go func() {
        for event := range events {
            switch event.Type {
            case core.ThinkingDelta:
                // 思维片段（流式）
                fmt.Print(event.Data)
            case core.ToolExecStart:
                data := event.Data.(core.ToolExecStartData)
                fmt.Printf("\n[Tool: %s]\n", data.ToolName)
            case core.ExecutionSummary:
                data := event.Data.(core.ExecutionSummaryData)
                fmt.Printf("\n=== Summary: %d iterations, %d tokens ===\n",
                    data.TotalIterations, data.TokensUsed)
            }
        }
    }()

    // 流式输出
    stream, streamCancel, _ := agent.AskStream("帮我写一个 Go 的 HTTP server")
    defer streamCancel()
    for text := range stream {
        fmt.Print(text)
    }

    // 也可以用 Ask 获取完整结果
    result, _ := agent.Ask("你好")
    fmt.Printf("\nAnswer: %s\nTokens: %d\n", result.Answer, result.Tokens)
}
```

---

## Agent 完整 API

### 构造

| 方法                             | 说明                      |
| -------------------------------- | ------------------------- |
| `goharness.DefaultAgent(apiKey)` | 一行创建，默认配置        |
| `goharness.NewAgent(opts...)`    | 通过 Option 精确配置      |
| `goharness.DefaultModel()`       | 预设模型（qwen3.5-flash） |
| `goharness.DefaultConfig()`      | 预设 AgentConfig          |

### Option 清单

| Option                | 类型                               | 说明                                  |
| --------------------- | ---------------------------------- | ------------------------------------- |
| `WithConfig`          | `*core.AgentConfig`                | Agent 身份、领域、SystemPrompt        |
| `WithModel`           | `*core.ModelConfig`                | LLM 后端、API Key、BaseURL、MaxTokens |
| `WithMemory`          | `core.Memory`                      | 知识检索记忆体                        |
| `WithSession`         | `(string, int64)`                  | 会话 ID 和 Token 预算                 |
| `WithExtraTools`      | `...core.FuncTool`                 | 注入自定义工具                        |
| `WithoutBundledTools` | —                                  | 禁用全部内置工具                      |
| `WithoutTool(name)`   | `string`                           | 禁用指定工具                          |
| `WithSkillDir(dir)`   | `string`                           | 加载外部 Skill 目录                   |
| `WithEventBus`        | `reactor.EventBus`                 | 自定义事件总线                        |
| `WithSecurityPolicy`  | `func(string, SecurityLevel) bool` | 工具执行安全策略                      |

### 对话

| 方法                  | 返回                             | 说明                   |
| --------------------- | -------------------------------- | ---------------------- |
| `Ask(question)`       | `(*Result, error)`               | 同步提问，返回完整结果 |
| `AskStream(question)` | `(<-chan string, func(), error)` | 流式输出文本片段       |

### 结果查询

| 方法                     | 返回                          | 说明                                                        |
| ------------------------ | ----------------------------- | ----------------------------------------------------------- |
| `LastResult()`           | `*Result`                     | 最近一次调用的结果（含 Tokens、Steps、Duration、ToolsUsed） |
| `Events()`               | `(<-chan ReactEvent, func())` | 订阅所有事件                                                |
| `EventsFiltered(filter)` | `(<-chan ReactEvent, func())` | 按条件过滤事件                                              |

### 只读查询

| 方法          | 返回                | 说明                                         |
| ------------- | ------------------- | -------------------------------------------- |
| `Tools()`     | `[]ToolInfo`        | 已注册工具列表（名称、描述、参数、安全级别） |
| `Skills()`    | `[]*core.Skill`     | 已加载 Skill 列表                            |
| `Config()`    | `*core.AgentConfig` | Agent 配置                                   |
| `Model()`     | `*core.ModelConfig` | 模型配置                                     |
| `Name()`      | `string`            | Agent 名称                                   |
| `Domain()`    | `string`            | 领域                                         |
| `Memory()`    | `core.Memory`       | 记忆体实例                                   |
| `SessionID()` | `string`            | 当前会话 ID                                  |

### 会话管理

| 方法                        | 说明                     |
| --------------------------- | ------------------------ |
| `NewSession(id, maxTokens)` | 开启新会话，丢弃旧上下文 |

### 高级

| 方法        | 说明                                          |
| ----------- | --------------------------------------------- |
| `Reactor()` | 获取内部 Reactor 引用（高级场景，通常不需要） |

---

## 进阶：直接使用 Reactor

> 通常不需要直接使用 Reactor。`goharness.Agent` 已封装了完整的智能体生命周期。以下场景才需要直接操作 Reactor：
> - 需要完全控制 T-A-O 循环的每次迭代
> - 需要在无状态模式下使用（不管理会话上下文）
> - 需要深度定制 Prompt、工具管线、压缩策略等引擎内部行为

### 无状态调用

```go
package main

import (
    "context"
    "fmt"
    "github.com/DotNetAge/goharness/reactor"
)

func main() {
    r := reactor.NewReactor(reactor.ReactorConfig{
        APIKey: "your-api-key",
        Model:  "qwen3.5-flash",
    })

    // 无状态调用：每次 Run 独立，无会话记忆
    result, err := r.Run(context.Background(), "你好", nil)
    if err != nil {
        panic(err)
    }
    fmt.Println(result.Answer)
}
```

---

## 内置工具清单

| 类别              | 工具名             | 说明                      |
| ----------------- | ------------------ | ------------------------- |
| **文件操作**      | `read`             | 读取文件内容              |
|                   | `write`            | 写入/创建文件             |
|                   | `file_edit`        | 编辑文件（基于搜索替换）  |
|                   | `replace`          | 全局搜索替换              |
|                   | `ls`               | 列出目录内容              |
|                   | `grep`             | 正则搜索文件内容          |
|                   | `glob`             | Glob 模式搜索文件名       |
| **终端与执行**    | `bash`             | 执行 Shell 命令           |
|                   | `repl`             | 交互式代码执行环境        |
|                   | `calculator`       | 数学计算                  |
|                   | `echo`             | 输出文本                  |
| **信息获取**      | `web_search`       | 互联网搜索                |
|                   | `web_fetch`        | 网页内容抓取              |
| **任务编排**      | `task_create`      | 创建并同步执行子任务      |
|                   | `task_result`      | 获取子任务结果            |
|                   | `task_list`        | 列出所有子任务            |
| **多 Agent 团队** | `subagent`         | 派发独立异步 Agent        |
|                   | `subagent_result`  | 获取 SubAgent 结果        |
|                   | `subagent_list`    | 列出所有 SubAgent         |
|                   | `team_create`      | 创建团队                  |
|                   | `send_message`     | 发送团队消息              |
|                   | `receive_messages` | 接收团队消息              |
|                   | `team_status`      | 查看团队状态              |
|                   | `team_delete`      | 删除团队                  |
|                   | `wait_team`        | 等待团队全部完成          |
| **记忆系统**      | `memory_save`      | 保存知识到长期记忆        |
|                   | `memory_search`    | 搜索长期记忆              |
| **任务管理**      | `todo_write`       | 创建/更新任务计划         |
|                   | `todo_read`        | 读取当前任务列表          |
|                   | `todo_execute`     | 执行任务计划              |
| **交互工具**      | `ask_user`         | 向用户提问（中断-恢复）   |
|                   | `ask_permission`   | 请求用户授权（中断-恢复） |
| **其他**          | `cron`             | 定时任务                  |
|                   | `skill_create`     | 动态创建 Skill            |
|                   | `skill_list`       | 列出可用 Skill            |

---

## 架构概览

```
┌──────────────────────────────────────────────────┐
│                   Agent (门面)                     │
│  Ask / AskStream / Events / Tools / Skills        │
│  WithConfig / WithModel / WithMemory / ...        │
└────────────────────┬─────────────────────────────┘
                     │ 内部自动构造
┌────────────────────▼─────────────────────────────┐
│                  Reactor (T-A-O)                  │
│                                                    │
│  Run(ctx, input, history) → RunResult             │
│    ├─ Phase 1: classifyIntent (意图分类)           │
│    ├─ Phase 2: T-A-O Loop                         │
│    │    ├─ Think (LLM推理 + Memory检索 + Skill匹配) │
│    │    ├─ Act   (工具执行 / 权限检查)              │
│    │    ├─ Observe (结果评估)                       │
│    │    └─ maybeCompact (上下文压缩)                │
│    ├─ Phase 3: saveExperience (经验保存)           │
│    └─ Phase 4: Emit ExecutionSummary               │
│                                                    │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────┐ │
│  │ToolRegistry│  │SkillRegistry│ │  Memory (外部注入)│ │
│  │(含权限管线)│  │(渐进式加载) │ │(5种记忆类型)      │ │
│  └──────────┘  └──────────┘  └──────────────────┘ │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────┐ │
│  │ EventBus │  │MessageBus│  │  MCPToolRegistry  │ │
│  │(事件流)   │  │(团队通信) │ │  (外部工具协议)    │ │
│  └──────────┘  └──────────┘  └──────────────────┘ │
└──────────────────────────────────────────────────┘
```

---

## 文档

| 文档                                          | 说明                                   |
| --------------------------------------------- | -------------------------------------- |
| [功能描述与设计理念](./docs/记忆体的设计.md)  | Memory 子系统设计文档                  |
| [Memory 开发指南](./docs/memory-dev-guide.md) | 如何实现自定义 Memory                  |
| [集成指南](./docs/integration-guide.md)       | 事件流、流式输出、Token 追踪等集成细节 |
| [工具开发指南](./docs/tool-dev-guide.md)      | 如何开发自定义工具                     |
| [Skill 开发指南](./docs/skill-dev-guide.md)   | 如何开发 Skill                         |
| [MCP 开发指南](./docs/mcp-dev-guide.md)       | 如何接入 MCP 服务器                    |

---

## 许可证

[MIT License](./LICENSE)
