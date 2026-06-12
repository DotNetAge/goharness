# goharness TODO

## 架构定位

goharness = Agent Operating System。所有待办事项基于此定位组织。

---

## P0: 编译通过

- [x] 修复剩余编译错误（coordination.go 决策路径、RunResult.Intent 引用等）
- [x] 修复测试文件中的编译错误
- [x] go vet ./... 无错误

## P1: 核心功能

### Reactor 内核

- [x] **Think prompt 引导词** — think_prompt.tmpl 的 interaction_principles 中增加多 Agent 协作引导
- [x] **ToolContext 注入** — EventBus + ResultStore 通过 context 传递给工具
- [x] **Act 批量并行** — 同步工具等结果，异步工具 go goroutine
- [x] **ToolInfo.IsAsync** — 同步/异步标识
- [x] **ReactEvent.AgentID** — 父子 Agent 共享 EventBus
- [x] **移除 classifyIntent** — 直接进入 T-A-O
- [x] **移除 responsibility_gate** — Think 直接输出

### 编排工具

- [x] **SubAgent 工具** — 异步创建子 Agent，共享 EventBus
- [x] **collect_results 工具** — 阻塞等待异步结果
- [x] **find_agent 工具** — 按领域查找已注册的专家 Agent
- [x] **rank 工具** — 记录子 Agent 绩效评分

### Prompt 模板

- [x] 所有工具填充 ToolInfo.Prompt（从 Claude 抄写英文原文）
- [x] think_prompt.tmpl 引导词加入
- [x] default_system_prompt.tmpl 适配 v2 交互原则

## P2: 完善优化

- [x] **子 Agent 流式事件透传** — 共享 EventBus 到达客户端
- [x] **create_agent 工具** — 显式创建具有特定职责的 Agent
- [x] **query_agents 工具** — 查询所有可用 Agent
- [x] **终止条件可扩展** — 通过 RuleRegistry 注册
- [x] **增量 NativeTools schema** — 记录已补 schema 的工具列表
- [x] **结果卸载实现** — Observe 检测超大输出自动写入文件

## P3: 迁移与清理

- [x] 旧 Task/Skill/SubAgent 工具迁移到 SubAgent/CollectResults 模式
- [x] 废弃 core/orchestrator.go 中的 AgentOrchestrator 接口
- [x] 删除无用的 coordination.go 协调器代码
- [x] RunResult 移除 Intent、ClarificationNeeded 等遗留字段

## 设计文档清单

```
AGENT-OS.md                   ─── 北极星：整体架构与哲学
DESIGN-SIGNLE-AGENT.md        ─── Agent 定义：身份、SystemPrompt、Role
DESIGN-REACT.md               ─── 内核：T-A-O 循环、渐进式披露、并行执行
DESIGN-TOOL.md                ─── 命令系统：ToolInfo、IsAsync、ToolContext
DESGIN-MEMORY.md              ─── 存储系统：会话记忆 + 长期知识
DESIGN-LLMCALL.md             ─── LLM 调用层：gochat 适配、流式、Token 计算
```

## LLMCaller 重构进度 (2026-05-02)

### P1 基础设施 ✓
- [x] **TokenUsage 类型** — core/session.go: TokenUsage struct
- [x] **SessionStore 扩展** — AppendTokenUsage/GetTokenUsages 接口 + MemorySessionStore 实现
- [x] **LLMCaller 结构体** — reactor/llmcall.go: 完整实现所有方法
- [x] **RebuildContext(sessionID, agentName)** — 从 SessionStore 加载重建
- [x] **TotalInputTokens/TotalOutputTokens/RemainTokens/TokenRecords** — 全部实现

### P1 调用实现 ✓
- [x] **LLMCaller.Call()** — 合并 buildLLMBuilder + callLLMWithHistory + estimateInputTokens
- [x] **LLMCaller.CallStream()** — 合并 callLLMStream，streaming + 自动 Token 管理
- [x] **LLMCaller.CallGate()** — 轻量无历史/无工具/无滑动调用

### P1 调用方迁移 ✓
- [x] **Reactor 字段迁移** — llmClient/tokenEstimator/contextWindow/sessionStore/mockLLM → llmCaller
- [x] **Think 改为 LLMCaller.CallStream()** — 消除手动 Token 汇总 + checkSlide
- [x] **generateSummary 改为 LLMCaller.Call()** — 消除对 callLLMWithHistory 的直接调用

### 遗留项目

- [x] P2: 滑动时阻止 SystemPrompt 被挤掉
- [x] P2: Token 用量在 CallResult / 事件中的可视性增强
- [x] 测试文件清理: dataflow_test.go, tao_integration_test.go, reactor_test.go, e2e_test.go, skill_registry_test.go 引用已废弃类型
- [x] 旧文件清理: reactor/session.go, reactor/llm.go 已缩减为占位，确认无外部引用后可删除

---

- [x] 在 SystemPrompt 中增加 SessionId, Pwd 等上下文信息
- [x] 一切的脚本，运行都是基于会话上下文运行的，因此运行的工作目录就是会话的存储目录
- [x] 对于基于命令行的工具要增加基于沙箱 env 的运行环境，Seatbelt 提供的沙箱环境
- [x] 研究一下 Claude 与 CodeBuddy 的 Plugin 机制，
  - [x] 以最小的入侵方式增加对 Plugin 模式的支持（Agent 作为入口类，插件扩展资源）
  - [x] 实现 agents 目录支持，允许开发者以现有方式定义 Agent 配置
  - [ ] 找出是否可以将它们的插件直接复制就能使用的方案

---

- [x] 增强搜索插件（混合搜索），
  - [x] 通过搜索适配器增强，多个适配器以并行方式运行，舍弃失败(可能会因为GWF而失败)的结果；
  - [x] 百度+360（Haosou搜索）+搜狗（狗狗搜索）+ DuckDuckGo搜索，
  - [x] 混合搜索采用对各取搜索结果的前5个结果进行混合；
- [x] 采用统一的日志接口，由外部实现日志，只要大家接口方法相同就算是用其它库的接口名实现也可以兼容;

---

- [ ] 自动摘要
- [ ] 思维循环的细粒度控制
- [ ] 简化Task
  
--- 

- [x] 增加对TokenUsage的独立管理。提取 TokenUsageStore 接口
- [x] 增加独立的摘要器, 构造时必须输入Model配置，可用WithSystemPrompt传入自定义SystemPrompt，调用gochat对输入内容进行摘要化处理。
- [ ] Session功能要进行增强，
  - 1. Session要增加一个ModifyFiles的字符串数组，用于记录会话中修改的文件路径。当会话中使用到Write,Replace一类会修改原目录内容的工具前，先将原有文件备份到Session的Backup目录中，然后将修改的文件路径保存到ModifyFiles数组中，如果Backup文件已存在，而又发生第二次修改就不要再重新备份，备份只保留一次就可了，也就是说ModifyFiles数组中如果已经有文件路径存在就不进行备份处理了。
  - 2. Session 要增加一个 ComfirmModify 方法与 Rollback 方法 ConfirmModify 是将备份文件的内容直接删除；Rollback 方法就是将的备份文件恢复到原有位置上，这两个方法执行都会移除ModifyFiles数组中相对应的文件路径。ConfirmModify 与 Rollback是可以同时对多个文件进行操作的。
  - 3. 凡是 ModifyFiles中有新的文件加入都要发出一个对应事件；
- 客户端 Mindx 
  - [ ] 1. 在SessionStore实现上可以获取存储SessionModifyFiles数组中的文件，同时Daemon也要增加对应的接口，在SessionInfo中携带ModifyFiles数组，这样和 TUI, WebUI 就可以获得会话中修改的文件路径。
  - [ ] 2. Daemon 可以删除HistoryFile的监控服务，因为daemon 不需要监听 HistoryFile 的变化，这样会减少 daemon 的资源消耗。
  - [ ] 3. Daemon 要将文件修改事件透传给客户端；
  - [ ] 4. 当界面收到文件修改事件又或者加载Session时如果modify_files不为空，就在输入框的上方显示一个提标框，布局如下： 图标 + Dropdown(文件名) + 全部撤销 全部保留 (右侧对齐)。右则的两个按钮分别向Daemon发送 ConfirmModify 与 Rollback指令以操作服务端的Session进行同步操作；
  
