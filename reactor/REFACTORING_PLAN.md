# Reactor 包重构规划书

> 基于逐行代码复检确认的 11 项问题，按依赖关系和风险等级分 **4 个阶段**实施。
> 创建时间: 2026-05-18
> 最后更新: 2026-05-18
> 涉及范围: `/goreact/reactor/` 全部 24 个 .go 文件

---

## 架构原则（重构决策依据）

**GoReact 是一个工具即编排的多 Agent 协作框架。**

核心含义：
- 任务编排完全依赖 LLM 决策，框架层不做中间干预
- 不存在 WBS 分解、责任链检查等框架级编排逻辑
- Reactor 是统一的 T-A-O 循环入口，22 个字段反映的是"能力汇聚于入口"而非"8 个职责域"
- 所有工具注册、执行、结果归集都是为 LLM 决策服务的无状态管道

**此原则对重构策略的影响：**
1. 与"框架层编排"相关的预留代码 → 确定死代码，可安全删除
2. God Object 按接口拆子结构体 → 不必要（Reactor 本质就是入口点）
3. 重构应聚焦于：消除重复、降低方法级复杂度、提升可读性 — 而非架构分解

---

## 总体策略

**核心原则：每次重构只改一个关注点，每阶段完成后全量测试通过再进入下一阶段。**

```
Phase 0 (基础设施清理) ──→ Phase 1 (方法级拆分) ──→ Phase 2 (数据结构独立) ──→ Phase 3 (大方法分解)
     无风险                    低风险                      低风险                     中风险
   可并行处理               可并行处理                   可并行处理                 需顺序执行
```

Phase 4（架构级分解）❌ **不执行** — 与架构原则不符，详见下文。

---

## 复检确认的问题清单（11 项）

| # | 问题 | 严重度 | 代码证据 | 判定 |
|---|------|--------|----------|------|
| 1 | Reactor God Object（22 字段 8 职责域） | 🟡 中 | reactor.go L92-L135 | ✅ 成立但属设计意图 — 入口点汇聚 |
| 2 | registerBundledTools 110 行 + sandbox 双分支重复 | 🔴 高 | reactor.go L364-L474, L388-404 vs L405-421 | ✅ 成立 |
| 3 | executeToolCalls 234 行混合 5 关注点 | 🔴 高 | think_act_observe.go L257-L490 | ✅ 成立 — 核心重构目标 |
| 4 | CloneReactor 12 字段逐 if 合并 | 🟡 中 | reactor.go L634-L678 | ✅ 成立 |
| 5 | runLoop 三阶段错误处理模式重复（×3） | 🟡 中 | reactor.go L931-L972 ×3 | ✅ 成立 |
| 6 | recordResult/buildErrorResult ~40 行完全重复 | 🟡 中 | llmcall.go L629-L668 vs L687-L726 | ✅ 成立 |
| 7 | offload 包级可变状态（3 个全局 var） | 🟡 中 | offload.go L33, L45-L46 | ✅ 成立 |
| 8 | buildResultFromContext Answer 回退链混入事件发射 | 🟢 低 | reactor.go L820-L884 | ✅ 成立 |
| 9 | coordination.go 含未使用的预留类型（死代码） | 🟢 低 | coordination.go L446-L468 零引用 | ✅ **成立 — 与架构原则相悖** |
| 10 | interfaces.go 四接口零外部使用（仅编译期断言） | ℹ️ 信息性 | 全项目 grep 零命中 | ⚠️ 保留，作为文档性接口契约 |
| 11 | 工具函数分散于 4 个文件 | 🟢 低 | truncate(thought.go), coalesce(tao.go), wrapError(reactor.go) 等 | ✅ 成立 |

### 关键判定说明

- **#1 God Object**：严重度从"🔴 高"降为"🟡 中"。入口点汇聚 22 个字段是 GoReact 架构的
  设计意图，不是设计缺陷。不进行架构级分解（Phase 4），仅做方法级清理。
- **#9 死代码**：`ResponsibilityCheck`/`AtomicityCheck`/`TaskDecomposition` 代表"框架层做
  WBS 分解"的思路，与"编排完全依赖 LLM"的架构原则相悖。**确认删除，无需确认。**
- **#10 interfaces.go**：保留。四个接口作为文档性契约，标明 Reactor 提供的能力域，
  对代码阅读者有导航价值。

---

## Phase 0：基础设施清理（无风险，可立即执行）

### 0.1 工具函数归拢到 `utils.go`

**现状**：散落在 4 个文件中

| 函数 | 当前位置 | 行号 |
|------|----------|------|
| `truncate()` | thought.go | L112-118 |
| `coalesce()` | think_act_observe.go | L536-541 |
| `wrapError()` | reactor.go | L1190-1195 |
| `lookUpToolCallID()` | reactor.go | L1169-1187 |
| `collectUniqueToolNames()` | termination.go | L192-204 |
| `analyzeActionResult()` | termination.go | L181-190 |
| `formatConversationContext()` | llmcall.go | L739-L752 |
| `resolveSessionID()` | offload.go | L142-L150（属 Reactor 通用辅助方法，误放在 offload 域） |

**操作**：
1. 新建 `utils.go`，将以上 7 个函数移入
2. 全局搜索替换引用（IDE 重构即可）
3. 删除原位置空行

**风险**：✅ 纯文件移动，零行为变更
**验证**：`go build ./...` + `go test ./...` 通过

---

### 0.2 清理死代码

**现状**：coordination.go L442-L468 的三个类型无任何引用，且与架构原则相悖：

```go
type ResponsibilityCheck struct { ... }    // 零引用
type AtomicityCheck struct { ... }        // 零引用
type TaskDecomposition struct { ... }     // 零引用
```

以及 reactor.go L476-L483 被注释掉的 `toolHasTag()` 函数，plus `offload.go` 中的
`SetOffloadLogger()` 函数（全局变量日志设置器，OffloadManager 化后不再需要）。

**操作**：
1. 删除 `ResponsibilityCheck`、`AtomicityCheck`、`TaskDecomposition` 三个类型
2. 删除注释掉的 `toolHasTag()` 函数
3. 删除 `SetOffloadLogger()`（Phase 2.2 前置清理）

**风险**：✅ 零引用已确认。这三个类型代表的是"框架层做 WBS 分解"的旧思路，
GoReact 的架构原则是编排完全依赖 LLM，因此不可能是"为将来预留的功能"。
**验证**：`go build ./...` + `go test ./...` 通过

---

## Phase 1：方法级拆分（低风险，改动局部）

### 1.1 提取 `recordTokenUsage()` 消除 recordResult/buildErrorResult 重复

**现状**：llmcall.go 两方法共享 ~38 行相同代码

**目标结构**：

```go
// 新增内部方法：统一的 token 用量记录与持久化
func (c *LLMCaller) recordTokenUsage(ctx context.Context, input CallInput,
    inputTokens int, outputTokens int) core.TokenUsage {

    remainTokens := c.maxTokens
    c.mu.RLock()
    if c.contextWindow != nil {
        remainTokens = int(c.contextWindow.TokensRemaining())
    }
    c.mu.RUnlock()

    usage := core.TokenUsage{
        Timestamp:    time.Now(),
        InputTokens:  inputTokens,
        OutputTokens: outputTokens,
        RemainTokens: remainTokens,
    }

    c.mu.Lock()
    if c.contextWindow != nil && inputTokens > 0 {
        c.contextWindow.AddTokens(int64(inputTokens))
    }
    c.records = append(c.records, usage)
    c.mu.Unlock()

    sessionID := resolveSessionID(c, input)
    if c.sessionStore != nil && sessionID != "" {
        if err := c.sessionStore.AppendTokenUsage(ctx, sessionID, usage); err != nil {
            c.logger.Warn("failed to persist token usage",
                "session_id", sessionID, "error", err)
        }
    }

    return usage
}
```

**改造后的调用方**：

```go
func (c *LLMCaller) recordResult(...) CallResult {
    outputTokens := calcOutputTokens(respOrTokens, inputTokens)
    usage := c.recordTokenUsage(ctx, input, inputTokens, outputTokens)
    return CallResult{Content: content, ToolCalls: toolCalls, TokenUsage: usage, Error: nil}
}

func (c *LLMCaller) buildErrorResult(...) CallResult {
    usage := c.recordTokenUsage(ctx, input, inputTokens, 0)
    return CallResult{Content: fmt.Sprintf("[llmcaller error] %v", err),
        ToolCalls: nil, TokenUsage: usage, Error: err}
}
```

**涉及行数变更**：
- 新增 `recordTokenUsage()`: ~35 行
- `recordResult()`: 从 62 行 → ~15 行（减少 ~47 行）
- `buildErrorResult()`: 从 48 行 → ~6 行（减少 ~42 行）
- **净减少：~54 行**

**风险**：✅ 纯提取，行为不变

---

### 1.2 registerBundledTools() 表驱动化

**现状**：reactor.go L364-L474，110 行，sandbox 双分支重复

**目标结构**：

```go
type toolRegistration struct {
    name     string
    factory  func(mgr *tools.SessionSandboxManager) core.FuncTool
    skipName string
}

var defaultBundledTools = []toolRegistration{
    {"Grep", func(_ *tools.SessionSandboxManager) core.FuncTool {
        return tools.NewGrepTool()
    }, "Grep"},
    {"Glob", func(_ *tools.SessionSandboxManager) core.FuncTool {
        return tools.NewGlobTool()
    }, "Glob"},
    {"Read", func(_ *tools.SessionSandboxManager) core.FuncTool {
        return tools.NewReadTool()
    }, "Read"},
    {"Write", func(_ *tools.SessionSandboxManager) core.FuncTool {
        return tools.NewWriteTool()
    }, "Write"},
    {"FileEdit", func(_ *tools.SessionSandboxManager) core.FuncTool {
        return tools.NewFileEditTool()
    }, "FileEdit"},
    {"Bash", func(mgr *tools.SessionSandboxManager) core.FuncTool {
        if mgr != nil { return tools.NewBashToolWithSessionSandbox(mgr) }
        return tools.NewBashTool()
    }, "Bash"},
    {"RunScript", func(mgr *tools.SessionSandboxManager) core.FuncTool {
        if mgr != nil { return tools.NewRunScriptToolWithSessionSandbox(mgr) }
        return tools.NewRunScriptTool()
    }, "RunScript"},
    {"PowerShell", func(mgr *tools.SessionSandboxManager) core.FuncTool {
        if !tools.IsWindowsPlatform() { return nil }
        if mgr != nil { return tools.NewPowerShellToolWithSessionSandbox(mgr) }
        return tools.NewPowerShellTool()
    }, "PowerShell"},
    // WebSearch, WebFetch, TodoWrite/Read/Execute, AskUser, Ls ...
    // Delegate, CollectResults, Skill, TaskCreate/List/Get/Update/Stop, TeamCreate
}

func (r *Reactor) registerBundledTools(setup *reactorSetup) {
    if setup.skipAllBundled {
        return
    }

    for _, reg := range defaultBundledTools {
        if setup.skipTools[reg.skipName] {
            continue
        }
        tool := reg.factory(setup.sandboxMgr)
        if tool == nil {
            continue
        }
        if err := r.RegisterTool(tool); err != nil {
            r.getLogger().Warn("failed to register bundled tool",
                "name", reg.name, "error", err)
        }
    }

    r.registerOrchestrationTools(setup)
}

func (r *Reactor) registerOrchestrationTools(setup *reactorSetup) {
    spawn := r.SpawnFunc
    orchestrationTools := []struct {
        name string
        tool core.FuncTool
    }{
        {"Delegate", tools.NewDelegateTool(func(ctx context.Context, agentName, task string) (string, error) {
            if spawn != nil { return spawn(ctx, agentName, task) }
            return "", fmt.Errorf("delegate: SpawnFunc not configured")
        })},
        // TaskCreate, TeamCreate 同理...
    }
    for _, ot := range orchestrationTools {
        if !setup.skipTools[ot.name] {
            if err := r.RegisterTool(ot.tool); err != nil {
                r.getLogger().Warn("failed to register orchestration tool", "name", ot.name, "error", err)
            }
        }
    }
}
```

**涉及行数变更**：
- `registerBundledTools()`: 从 110 行 → ~20 行
- 新增 `defaultBundledTools` 表: ~80 行（声明式，易读易维护）
- 新增 `registerOrchestrationTools()`: ~30 行
- **消除 sandbox if/else 双分支**

**风险**：✅ 行为等价，新增工具只需追加一条表项

---

### 1.3 CloneReactor() 配置合并提取为 ReactorConfig.Merge()

**现状**：reactor.go L634-L678，12 个字段逐 if 覆盖

**操作**：

```go
func (c ReactorConfig) Merge(override ReactorConfig) ReactorConfig {
    if override.Model != ""             { c.Model = override.Model }
    if override.APIKey != ""             { c.APIKey = override.APIKey }
    if override.BaseURL != ""            { c.BaseURL = override.BaseURL }
    if override.AuthToken != ""           { c.AuthToken = override.AuthToken }
    if override.Temperature > 0          { c.Temperature = override.Temperature }
    if override.TopP > 0                 { c.TopP = override.TopP }
    if override.TopK > 0                 { c.TopK = override.TopK }
    if override.PresencePenalty != 0      { c.PresencePenalty = override.PresencePenalty }
    if override.FrequencyPenalty != 0     { c.FrequencyPenalty = override.FrequencyPenalty }
    if override.MaxTokens > 0            { c.MaxTokens = override.MaxTokens }
    if override.IsLocal                  { c.IsLocal = override.IsLocal }
    return c
}
```

**注意**：SystemPrompt 有特殊语义——CloneReactor 在 override 为空时会显式清零（防止父身份泄漏），Merge 不自动处理此语义，由 CloneReactor 显式处理。

**涉及行数变更**：
- `CloneReactor()`: 从 76 行 → ~25 行
- 新增 `ReactorConfig.Merge()`: ~16 行
- **净减少：~35 行**

**风险**：⚠️ 需特别注意 SystemPrompt 清零语义的兼容性

**OffloadManager 传递**：CloneReactor 时 `child.offloadMgr = r.offloadMgr`（共享引用，
详见 Phase 2.2 CloneReactor 兼容说明）。

---

### 1.4 runLoop() 错误处理模式提取

**现状**：reactor.go L931-L972，三段相同的错误终止逻辑

**操作**：

```go
func (r *Reactor) abortCycle(reactCtx *ReactContext, phase string, err error,
    cycleStart time.Time, cycleNum int, sessionID string) {
    reactCtx.TerminationReason = fmt.Sprintf("%s error: %v", phase, err)
    reactCtx.EmitEvent(core.Error, reactCtx.TerminationReason)
    r.getLogger().Error("cycle abort", err,
        "session_id", sessionID, "iteration": cycleNum,
        "phase": phase, "elapsed_ms": time.Since(cycleStart).Milliseconds())
}
```

**涉及行数变更**：
- 新增 `abortCycle()`: ~12 行
- `runLoop()`: 减少 ~30 行（三段 × ~10 行）
- **净减少：~18 行**

**风险**：✅ 纯提取

---

## Phase 2：数据结构独立（低风险，文件拆分）

### 2.1 TaskProgressTable + 协调类型 → 独立文件 coordinator_types.go

**现状**：coordination.go 636 行中，纯数据结构占 ~350 行（55%）

**拆分方案**：

| 新文件 | 内容 | 来源行 |
|--------|------|--------|
| `coordinator_types.go` | AgentMode, LifecycleState, TaskStatus, TaskEntry, TaskResultHolder, TaskProgressTable(全部方法), TaskUpdateOption 系列 | coordination.go L22-351 |
| `coord_state.go` | CoordState 结构体、NewCoordState()、生命周期方法(Interrupt/Resume/Cancel/MarkCompleted/RegisterSubTask/UnregisterSubTask)、内部 helper | coordination.go L352-636（*C 注意：删除 L442-468 的三个死类型后行号偏移） |

**操作**：
1. 创建 `coordinator_types.go`，移动上述类型定义
2. 原 `coordination.go` 重命名为 `coord_state.go`（或保留为 coordination.go 只保留 CoordState 部分）
3. 删除 L442-468 的三个死类型
4. `goimports` 自动处理 import

**风险**：✅ 纯文件拆分，包内类型移动不影响 API

---

### 2.2 offload 全局状态实例化 → OffloadManager 结构体

**现状**：offload.go L33-L47 的 3 个包级变量 + 一个包级函数 `SetOffloadLogger`

**目标结构**：

```go
type OffloadManager struct {
    logger    core.Logger
    cleanupMu sync.Mutex
    started   bool
}

func NewOffloadManager(logger core.Logger) *OffloadManager {
    return &OffloadManager{logger: logger}
}

func (m *OffloadManager) StartBackgroundCleanup() {
    m.cleanupMu.Lock()
    defer m.cleanupMu.Unlock()
    if m.started { return }
    m.started = true
    go m.periodicOffloadCleanup()
}
```

**操作**：
1. 新增 `OffloadManager` 结构体
2. 现有包级函数改为 `OffloadManager` 方法
3. 包级全局变量（`offloadLogger`、`offloadCleanupOnce`、`offloadCleanupMu`）删除
4. 包级函数 `SetOffloadLogger` 删除（Phase 0.2 中已标注）
5. Reactor 结构体新增 `offloadMgr *OffloadManager` 字段
6. 所有 offload 操作通过 `r.offloadMgr.xxx()` 调用

**风险**：⚠️ 需确保 Reactor 初始化时创建 OffloadManager。由于 offload 函数当前全部
通过包级变量访问，不存在外部直接调用 `SetOffloadLogger()` 的风险（`grep -r
SetOffloadLogger` 验证）。

**CloneReactor 兼容**：子 Reactor 复用 parent 的 `offloadMgr` 引用（方案 A: 共享）。
OffloadManager 本质无状态，cleanup 通过 sync.Once 保证只启动一次，多实例共享
不会重复启动。子 Agent 的 offload 结果本就在同一 projectDir 下。

---

## Phase 3：大方法分解（中风险，核心重构）

### 3.1 executeToolCalls() 拆分为子方法

**现状**：think_act_observe.go L257-L490，234 行混合 5 个关注点（解析调用、分区、
同步执行、异步执行、结果组装）。**这是整个重构中收益最高的单项改动。**

**拆分方案**：

```go
// executeToolCalls 变为编排器（~30 行）
func (r *Reactor) executeToolCalls(ctx, thought, start) error {
    calls := r.parseToolCalls(thought)                              // ~20 行
    if len(calls) == 0 { return r.handleEmptyCalls(ctx, thought) }  // ~10 行

    syncCalls, asyncCalls := r.partitionByAsync(calls)              // ~10 行
    var allResults []string
    var successCount int

    syncResults, syncSuccess, err := r.executeSyncTools(ctx, syncCalls, sessionID, start)
    // ...
    asyncResults, asyncSuccess, err := r.executeAsyncTools(ctx, asyncCalls, sessionID, start)
    // ...
    return r.assembleActionResult(ctx, allResults, successCount, calls, start)
}
```

**新增方法**：

| 方法 | 职责 | 估算行数 | 提取来源 |
|------|------|----------|----------|
| `parseToolCalls()` | 从 Thought 提取 toolCall 列表 | ~20 | L258-271 |
| `handleEmptyCalls()` | 无工具调用时的 Answer 回退 | ~10 | L272-278 |
| `partitionByAsync()` | 按 IsAsync 分区同步/异步 | ~10 | L280-291 |
| `executeSyncTools()` | 串行执行同步工具 | ~70 | L304-376（含事件发射） |
| `executeAsyncTools()` | 并行执行异步工具（goroutine+wait） | ~80 | L378-459 |
| `assembleActionResult()` | 合并结果写入 ctx.LastAction | ~20 | L460-490 |

**总行数**：拆分前 234 行 → 拆分后 ~160 行（编排器 30 + 各子方法合计 ~130）

**风险**：⚠️ 中等风险。executeSyncTools 和 executeAsyncTools 内部涉及事件发射和
offload 逻辑，提取时需保持事件顺序和行为一致。建议：先提取纯数据处理方法
（parseToolCalls / partitionByAsync / handleEmptyCalls），再提取带副作用的方法。

**PR Review Checklist 补充**：
- [ ] **人工确认**：拆分后 executeToolCalls 的事件发射顺序（ActionStart → ToolExecStart → ToolExecEnd → ActionProgress → ActionEnd）与拆分前一致

---

### 3.2 buildResultFromContext() 拆分

**现状**：reactor.go L820-L884，65 行混合了答案提取+事件发射+统计摘要

**拆分**：

```go
// 提取答案（纯数据处理）
func extractAnswer(reactCtx *ReactContext, terminationReason string) string { ... }

// 构建统计摘要
func buildExecutionSummary(reactCtx *ReactContext, totalTokens int,
    totalDuration time.Duration) core.ExecutionSummaryData { ... }

// 构建 TaskSummary
func buildTaskSummary(result *RunResult,
    totalDuration time.Duration) core.TaskSummaryData { ... }
```

**改造后的 buildResultFromContext**: 变为 ~25 行的纯编排方法

**风险**：✅ 纯提取

---

## ❌ 不做（架构层面已排除）

### ~~Phase 4：架构级分解（不做）~~

原计划中的 Phase 4（God Object 组合模式分解 + runLoop/runCoordinatorLoop 统一骨架）
**确认不做**，理由：

1. **架构原则冲突**：Reactor 是 T-A-O 循环的统一入口，22 个字段反映的是"能力汇聚"
   而非"职责过多"。拆分子结构体引入双向依赖，收益不足以抵消复杂度。
2. **interfaces.go 保留但不拆分文件**：四个接口作为文档性契约保留，但不按接口拆分
   到独立文件（原 Phase 3.1）。拆文件带来的导航开销 > 收益。
3. **runLoop 统一骨架**：runLoop 和 runCoordinatorLoop 的差异反映了 coordinator 模式
   和普通模式的真实区别，强制统一反而降低可读性。

### 执行计划时间线（调整后）

```
Week 1:
├── Day 1:   Phase 0 (工具函数归拢 + 死代码清理)
├── Day 2:   Phase 1.1 (recordTokenUsage 提取)
├── Day 3:   Phase 1.2 (registerBundledTools 表驱动化)
├── Day 4:   Phase 1.3 (CloneReactor Merge) + 1.4 (abortCycle)
└── Day 5:   Phase 2.1 (coordination 类型拆文件)

Week 2:
├── Day 1:   Phase 2.2 (OffloadManager 实例化)
├── Day 2-3: Phase 3.1 (executeToolCalls 拆分) ← 核心改动，重点投入
└── Day 4:   Phase 3.2 (buildResultFromContext 拆分)
  └── Day 5: 缓冲日（处理测试失败 / PR 准备）
```

---

## 每阶段验证清单

每个 Phase 完成后必须全部通过：

- [ ] `go build ./...` 编译成功
- [ ] `go test ./... -race -count=1` 全部通过
- [ ] `go vet ./...` 无警告
- [ ] 现有的 reactor_test.go、coordination_test.go、eventbus_test.go 全部通过
- [ ] 手动冒烟测试：创建 Reactor → Run 一个简单输入 → 检查结果结构完整性

建议在 Phase 0 开始前先记录测试覆盖率快照：
```bash
go test -coverprofile=/tmp/coverage_before.out ./...
```
每个 Phase 完成后记录包级别覆盖率百分比，确保不下降：
```bash
go test -cover ./goreact/reactor/  # 记录百分比
```

### 关键覆盖路径（重点关注）

| # | 路径 | 说明 | 覆盖风险 |
|---|------|------|----------|
| 1 | Think → Act(ToolCall) → Observe → persistStep | 完整 T-A-O 循环 | 低（主路径） |
| 2 | Think → Act(Answer) → 直接终止 | 无工具调用的短循环 | 中 |
| 3 | Coordinator: DecisionCoordinate → runCoordinatorLoop → poll → complete | 协调器完整生命周期 | **高**（条件多） |
| 4 | 错误路径: Think 返回 error → abortCycle | 异常终止 | 中 |
| 5 | CloneReactor → 子 Reactor Run | 子 Agent 创建与执行 | **高**（依赖链长） |

---

## 明确不做的事（边界）

1. **不改公共 API**：Runner/TAORunner 接口签名不变，NewReactor() 签名不变
2. **不改外部依赖关系**：core 包、tools 包、gochat 包的用法不变
3. **不做架构级分解**：不将 Reactor 拆为组合子结构体，保留统一入口点设计
4. **不按接口拆分文件**：interfaces.go 保留原始位置，不做文件级拆分
5. **不做性能优化**：本次纯粹是代码质量改进，不碰并发模型、缓存策略等
6. **不删 interfaces.go**：保留作为文档性接口契约

---

## 预期收益量化

| 指标 | 重构前 | 重构后（Phase 3 完成） |
|------|--------|----------------------|
| reactor.go 行数 | 1195 行 | ~550 行（工具函数移出 + CloneReactor 精简 + 错误处理提取 + buildResultFromContext 拆分） |
| think_act_observe.go 行数 | 741 行 | ~300 行（executeToolCalls 拆为 6 个子方法） |
| coordination.go 行数 | 636 行 | ~280 行（类型拆为 2 个文件，死代码删除） |
| llmcall.go 行数 | 752 行 | ~700 行（消除重复） |
| 单个方法最大行数 | 234 行（executeToolCalls） | < 90 行 |
| 包级可变状态数 | 3 个全局 var | 0（OffloadManager 实例化） |
| 死代码/注释代码 | 4 处 | 0 |
| 工具函数集中度 | 散落 4 个文件 | 统一在 utils.go |

### 方法级质量（圈复杂度）

| 方法 | 重构前 | 重构后 |
|------|--------|--------|
| `executeToolCalls()` | ~18-20 (双 for + 分区 if ×2 + 异步 goroutine + 事件条件 + offload) | ~4 (编排器本身) |
| `registerBundledTools()` | ~12 (嵌套 if + for + sandbox 双分支) | ~4 (单层 for + factory 调用) |
| `buildResultFromContext()` | ~8 (4 级 Answer 回退 + 事件条件) | ~3 (纯编排) |
