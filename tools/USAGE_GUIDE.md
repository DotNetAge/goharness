# GoHarness 工具使用指南：任务管理 / 团队编排 / 子代理调度

> 本指南面向需要在 SystemPrompt 中配置 LLM 使用这些工具的开发者。
> 目标：让 LLM 能够正确、高效地组合使用全部工具完成复杂任务。

---

## 目录

1. [工具体系总览](#1-工具体系总览)
2. [第一层：任务管理工具（Task CRUD）](#2-第一层任务管理工具task-crud)
3. [第二层：团队编排工具（Team Orchestration）](#3-第二层团队编排工具team-orchestration)
4. [第三层：子代理调度工具（SubAgent）](#4-第三层子代理调度工具subagent)
5. [三层协作：编排协议](#5-三层协作编排协议)
6. [完整实战示例](#6-完整实战示例)
7. [错误处理与边界情况](#7-错误处理与边界情况)
8. [推荐的 SystemPrompt 片段](#8-推荐的-systemprompt-片段)

---

## 1. 工具体系总览

### 1.1 架构分层

```
┌─────────────────────────────────────────────┐
│              第三层：子代理调度                │
│   SubAgent  ──异步派发──→  后台执行           │
│   （实际干活的执行单元）                        │
├─────────────────────────────────────────────┤
│              第二层：团队编排                  │
│   TeamCreate / TeamList / TeamDelete         │
│   TeamGetTasks                               │
│   （多 Agent 协作的容器）                      │
├─────────────────────────────────────────────┤
│              第一层：任务管理                  │
│   TaskCreate / TaskGet / TaskList / TaskUpdate│
│   （规划、跟踪、状态管理的核心）                │
└─────────────────────────────────────────────┘
          ↕ 全部基于 KVStore 持久化，按 Session 隔离
```

### 1.2 工具清单速查

| 工具名 | 层级 | 同步/异步 | 核心职责 |
|--------|------|-----------|----------|
| `TaskCreate` | L1 | 同步 | 创建任务记录 |
| `TaskGet` | L1 | 同步 | 查询单个任务详情 |
| `TaskList` | L1 | 同步 | 列出所有任务（支持过滤） |
| `TaskUpdate` | L1 | 同步 | 更新状态/元数据/依赖关系 |
| `TeamCreate` | L2 | 同步 | 创建团队 + 可选地预建任务 |
| `TeamList` | L2 | 同步 | 列出所有团队 |
| `TeamGetTasks` | L2 | 同步 | 查询团队下的所有任务 |
| `TeamDelete` | L2 | 同步 | 删除团队及清理数据 |
| `SubAgent` | L3 | **异步** | 派发子代理到后台执行 |

### 1.3 核心数据模型

**Task（任务）**

```go
type Task struct {
    ID          string         // UUID，创建时自动生成
    Subject     string         // 简短标题（必填）
    Description string         // 详细描述（必填）
    ActiveForm  string         // 执行时展示的进行时态，如 "Running tests"
    Status      TaskStatus     // pending → in_progress → completed / cancelled
    Owner       string         // 负责此任务的 Agent 名称
    Blocks      []string       // 此任务阻塞了哪些任务（它们依赖我）
    BlockedBy   []string       // 此任务被哪些任务阻塞（我依赖它们）
    Metadata    map[string]any // 任意附加信息
    CreatedAt   time.Time      // 创建时间
}
```

**Team（团队）**

```go
type Team struct {
    Name        string    // 团队唯一标识（kebab-case，如 "data-analysis-team"）
    Description string    // 团队工作内容描述
    Leader      string    // 团队领导 Agent 名称
    Members     []string  // 成员 Agent 名称列表
    TaskIDs     []string  // 分配给此团队的任务 ID 列表
    CreatedAt   time.Time
    Status      string    // "active" 或 "completed"
}
```

### 1.4 状态机规则

```
                    ┌─────────────┐
                    │   pending   │ ← 创建时的初始状态
                    └──────┬──────┘
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
      ┌──────────────┐  ┌────────┐  ┌───────────┐
      │ in_progress  │  │completed│  │ cancelled │
      └──────┬───────┘  └────────┘  └───────────┘
             │
        ┌────┴────┐
        ▼         ▼
   ┌────────┐ ┌───────────┐
   │completed│ │ cancelled │
   └────────┘ └───────────┘

合法转换：
  pending    → in_progress | completed | cancelled
  in_progress→ completed | cancelled
  completed  → （终态，不可再变）
  cancelled  → （终态，不可再变）
```

---

## 2. 第一层：任务管理工具（Task CRUD）

### 2.1 TaskCreate — 创建任务

**用途**：在任务列表中创建一条规划记录。仅用于跟踪，不执行任何操作。

**参数**：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `subject` | string | **是** | 任务简短标题 |
| `description` | string | **是** | 详细描述需要做什么 |
| `active_form` | string | 否 | 进行时态文本，如 "正在分析数据"、"运行测试中" |
| `metadata` | object | 否 | 任意键值对附件 |

**返回值**：

```json
{
  "task_id": "uuid-string",
  "status": "pending",
  "subject": "你填的标题",
  "description": "你填的描述",
  "active_form": "你填的进行时态或空",
  "metadata": {} 或你传的值
}
```

**正确用法**：

```
用户：帮我重构 auth 模块，需要做以下几件事：
     1. 分析现有代码结构
     2. 提取公共验证逻辑
     3. 编写单元测试
     4. 更新文档

LLM 应依次调用：

TaskCreate(subject="分析 auth 模块代码结构",
          description="阅读 src/auth/ 下所有文件，梳理模块结构和依赖关系",
          active_form="正在分析 auth 模块代码结构")

TaskCreate(subject="提取公共验证逻辑",
          description="将散落在各处的参数校验提取到 validators 包",
          active_form="正在提取公共验证逻辑")

TaskCreate(subject="编写单元测试",
          description="为提取后的 validators 包编写完整的单测覆盖",
          active_form="正在编写单元测试")

TaskCreate(subject="更新 API 文档",
          description="根据重构结果更新 docs/api-auth.md",
          active_form="正在更新文档")
```

**注意事项**：
- `subject` 和 `description` 都不能为空，否则报错
- 创建后状态固定为 `pending`
- 返回的 `task_id` 是后续操作的关键凭据，必须保存

---

### 2.2 TaskGet — 查询单个任务

**用途**：通过 `task_id` 获取任务的完整信息。

**参数**：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `task_id` | string | **是** | 任务唯一标识 |

**返回值**：

```json
{
  "task_id": "xxx",
  "subject": "任务标题",
  "description": "详细描述",
  "status": "pending",        // 或 in_progress / completed / cancelled
  "active_form": "进行时态",
  "owner": "agent-name",      // 仅当已分配 owner 时出现
  "blocks": ["task-b"],       // 此任务阻塞了谁
  "blocked_by": ["task-a"],   // 此任务被谁阻塞
  "created_at": "2026-06-15 10:30:00"
}
```

**正确用法**：

```
场景：刚创建完一批任务，想确认某个任务的状态和依赖关系

TaskGet(task_id="刚才 TaskCreate 返回的 task_id")

→ 返回 status: "pending", blocked_by: []  → 可以开始执行
→ 返回 status: "pending", blocked_by: ["task-a"] → 需等 task-a 完成
→ 返回 error: "task xxx not found"         → task_id 有误
```

**何时使用**：
- 需要精确知道某个任务的当前状态时
- 需要检查某个任务的依赖关系（blocked_by / blocks）时
- 批量操作前确认目标存在时

---

### 2.3 TaskList — 列出所有任务

**用途**：查看当前会话中的全部任务概览。

**参数**：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `status_filter` | string | 否 | 按状态过滤：`pending` / `in_progress` / `completed` / `cancelled` |
| `owner_filter` | string | 否 | 按负责人（Agent 名称）过滤 |

**返回值**：

```json
{
  "tasks": [
    {
      "task_id": "xxx",
      "subject": "标题",
      "status": "in_progress",
      "owner": "agent-a",
      "active_form": "进行中...",
      "blocked_by": [],
      "created_at": "2026-06-15 10:30:00"
    }
  ],
  "count": 1
}
```

无任务时返回：`{"tasks": [], "message": "No tasks found in this session"}`

**正确用法**：

```
场景 1：查看全局进度
TaskList()
→ 看到所有任务及其状态分布

场景 2：只看还没做完的
TaskList(status_filter="pending")
→ 规划下一步要做什么

场景 3：看某个 Agent 负责什么
TaskList(owner_filter="code-reviewer")
→ 确认分配是否合理

场景 4：组合过滤（先调两次分别看）
TaskList(status_filter="in_progress")   → 正在做的
TaskList(status_filter="pending")       → 排队的
```

---

### 2.4 TaskUpdate — 更新任务（核心工具）

**用途**：推进任务生命周期、修改元数据、建立依赖关系。这是最强大的任务工具。

**参数**：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `task_id` | string | **是** | 要更新的任务 ID |
| `subject` | string | 否 | 新标题 |
| `description` | string | 否 | 新描述 |
| `status` | string | 否 | 新状态：`pending` / `in_progress` / `completed` / `cancelled` |
| `owner` | string | 否 | 分配给某 Agent |
| `active_form` | string | 否 | 新的进行时态 |
| `metadata` | object | 否 | 合并到现有 metadata（设 key 为 null 可删除） |
| `addBlocks` | array | 否 | 此任务新增阻塞的任务 ID 列表 |
| `addBlockedBy` | array | 否 | 此任务新增被阻塞的任务 ID 列表 |

**至少提供一个变更字段**，否则返回 `{"success": false, "message": "No changes provided..."}`

#### 2.4.1 状态流转操作

```
标准工作流：

① 开始做任务：
   TaskUpdate(task_id="xxx", status="in_progress")

② 做完了：
   TaskUpdate(task_id="xxx", status="completed")

③ 做不下去了 / 不需要了：
   TaskUpdate(task_id="xxx", status="cancelled")

❌ 非法操作：
   TaskUpdate(task_id="xxx", status="pending")  // 已 completed 不能回退
   → 返回 error: "invalid status transition: completed → pending"
```

#### 2.4.2 依赖关系操作（DAG）

依赖系统用于表达"任务 A 必须完成后才能开始任务 B"。

**两种声明方式，效果等价**：

```
方式一：用 addBlocks（从"前置任务"角度声明）
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
含义："A 完成后，B 才能开始" = "A 阻塞了 B"

Step 1: 在任务 A 上调用 addBlocks，填入 B 的 ID
TaskUpdate(task_id="A的ID", addBlocks=["B的ID"])
→ 结果：A.Blocks = ["B"], B.BlockedBy = ["A"]

方式二：用 addBlockedBy（从"后置任务"角度声明）
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
含义："B 需要 A 先完成" = "B 被 A 阻塞"

Step 1: 在任务 B 上调用 addBlockedBy，填入 A 的 ID
TaskUpdate(task_id="B的ID", addBlockedBy=["A的ID"])
→ 结果：B.BlockedBy = ["A"], A.Blocks = ["B"]
```

**具体例子——构建依赖链**：

```
假设有 4 个任务：
  task-1: 分析需求
  task-2: 设计方案
  task-3: 编码实现
  task-4: 编写测试

依赖关系：1 → 2 → 3 → 4（线性链）

实现方式（任选其一）：

// 用 addBlocks 从前往后建链
TaskUpdate(task_id="task-1", addBlocks=["task-2"])   // 1 阻塞 2
TaskUpdate(task_id="task-2", addBlocks=["task-3"])   // 2 阻塞 3
TaskUpdate(task_id="task-3", addBlocks=["task-4"])   // 3 阻塞 4

// 或者用 addBlockedBy 从后往前建链
TaskUpdate(task_id="task-2", addBlockedBy=["task-1"]) // 2 被 1 阻塞
TaskUpdate(task_id="task-3", addBlockedBy=["task-2"]) // 3 被 2 阻塞
TaskUpdate(task_id="task-4", addBlockedBy=["task-3"]) // 4 被 3 阻塞
```

**扇入模式（多对一）**：

```
task-A (代码) ──┐
                 ├──→ task-D (集成测试) ← A 和 B 都完成后才能测
task-B (文档) ──┘

TaskUpdate(task_id="task-A", addBlocks=["task-D"])
TaskUpdate(task_id="task-B", addBlocks=["task-D"])

→ task-D.BlockedBy = ["task-A", "task-B"]
→ 查询 task-D 时能看到它被两个任务阻塞
```

**扇出模式（一对多）**：

```
task-X (需求分析) ──→ task-Y (前端实现)
                   └→ task-Z (后端实现)

TaskUpdate(task_id="task-Y", addBlockedBy=["task-X"])
TaskUpdate(task_id="task-Z", addBlockedBy=["task-X"])

→ X 完成后，Y 和 Z 可以并行开始
```

**循环依赖检测**：

系统会自动检测并拒绝循环依赖。以下操作会失败：

```
TaskUpdate(task_id="A", addBlocks=["B"])  // ✅ A → B
TaskUpdate(task_id="B", addBlocks=["A"])  // ❌ 报错：circular dependency

同样，传递循环也会被检测：
A → B → C → A   ❌ 被拒绝
```

#### 2.4.3 分配负责人

```
TaskUpdate(task_id="xxx", owner="code-reviewer")
→ 将任务分配给名为 "code-reviewer" 的 Agent

配合 owner_filter 使用：
TaskList(owner_filter="code-reviewer")  → 查看该 Agent 的所有任务
```

#### 2.4.4 Metadata 操作

```
// 添加/更新 metadata
TaskUpdate(task_id="xxx", metadata={"priority": "high", "estimation": "2h"})

// 删除某个 key（设为 null）
TaskUpdate(task_id="xxx", metadata={"priority": null})
→ priority 被删除，estimation 保留
```

#### 2.4.5 自动提醒机制

每完成第 3 个任务（3、6、9...）时，TaskUpdate 会额外返回一个 `nudge` 字段：

```json
{
  "success": true,
  "message": "Task xxx updated",
  "task_id": "xxx",
  "status": "completed",
  "nudge": "You have completed 3 tasks. Consider verifying your work (e.g., review files, run tests) before proceeding to the next task."
}
```

LLM 收到 `nudge` 时应考虑执行验证操作。

---

## 3. 第二层：团队编排工具（Team Orchestration）

### 3.1 TeamCreate — 创建团队

**用途**：组建一个多 Agent 协作团队。可同时为成员预建任务并自动轮询分配。

**参数**：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `team_name` | string | **是** | 团队唯一名称（kebab-case，如 `backend-refactor-team`） |
| `description` | string | **是** | 团队工作内容描述 |
| `leader` | string | **是** | 团队领导 Agent 名称 |
| `members` | array | **是** | 成员 Agent 名称列表（至少 1 个） |
| `tasks` | array | 否 | 为成员预建的任务描述数组 |

**返回值（无 tasks 参数时）**：

```json
{
  "team_name": "data-team",
  "leader": "coordinator",
  "members": ["analyst-1", "analyst-2"],
  "message": "Team \"data-team\" created with 2 members"
}
```

**返回值（带 tasks 参数时）**：

```json
{
  "team_name": "data-team",
  "leader": "coordinator",
  "members": ["analyst-1", "analyst-2"],
  "task_ids": ["team-data-team-task-1", "team-data-team-task-2"],
  "message": "Team \"data-team\" created with 2 members and 2 tasks created"
}
```

**正确用法**：

```
场景 1：只建团队，稍后再分配任务
TeamCreate(
  team_name="api-redesign-team",
  description="重新设计 REST API 接口",
  leader="myself",
  members=["schema-designer", "impl-agent", "test-writer"]
)

场景 2：建团队 + 同时分配任务（tasks 会轮询分配给 members）
TeamCreate(
  team_name="feature-x-team",
  description="实现 Feature X 功能",
  leader="coordinator",
  members=["frontend-dev", "backend-dev", "qa-agent"],
  tasks=[
    "实现前端页面组件和交互逻辑",
    "实现后端 API 和数据库迁移",
    "编写端到端测试用例"
  ]
)
→ 3 个任务自动创建，owner 分别设为：
  task-1 → frontend-dev
  task-2 → backend-dev
  task-3 → qa-agent  (轮询回绕到第一个成员)
```

**关键细节**：
- `members` 至少包含一个 Agent，空列表会报错
- `tasks` 数组中的任务按顺序 **轮询分配** 给 members（不是随机）
- 预建任务的 ID 格式为 `team-{team_name}-task-{序号}`
- 预建任务初始状态均为 `pending`

---

### 3.2 TeamList — 列出所有团队

**用途**：查看当前会话中所有团队的概况。

**参数**：无

**返回值**：

```json
{
  "teams": [
    {
      "team_name": "api-redesign-team",
      "leader": "coordinator",
      "members": ["schema-designer", "impl-agent", "test-writer"],
      "status": "active",
      "task_ids": ["team-api-redesign-team-task-1", "..."],
      "description": "重新设计 REST API 接口",
      "created_at": "2026-06-15 10:30:00"
    }
  ],
  "count": 1
}
```

无团队时返回：`{"teams": [], "message": "No teams found in this session"}`

---

### 3.3 TeamGetTasks — 查询团队任务

**用途**：获取指定团队下的所有任务及详情。

**参数**：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `team_name` | string | **是** | 团队名称 |

**返回值**：

```json
{
  "team_name": "feature-x-team",
  "tasks": [
    {
      "task_id": "team-feature-x-task-1",
      "subject": "实现前端页面组件",
      "status": "in_progress",
      "owner": "frontend-dev",
      "active_form": "正在实现前端组件",
      "blocked_by": [],
      "created_at": "2026-06-15 10:35:00"
    }
  ],
  "count": 1
}
```

无任务时 `message` 字段为 `"No tasks assigned to this team"`。

---

### 3.4 TeamDelete — 删除团队

**用途**：删除团队及其关联数据。

**参数**：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `team_name` | string | **是** | 要删除的团队名称 |

**返回值**：

```json
{
  "success": true,
  "message": "Team \"old-team\" deleted successfully",
  "team_name": "old-team"
}
```

**注意**：删除团队不会自动删除其下属的 Task 记录（Task 是独立存储的）。如需彻底清理，需先手动取消/完成任务，再删团队。

---

## 4. 第三层：子代理调度工具（SubAgent）

### 4.1 SubAgent — 异步派发子代理

**用途**：生成一个独立的子代理，在后台执行一次性委派任务。

**关键特性**：
- **异步执行**：调用后立即返回，不等待结果
- **并行能力**：同一响应中的多个 SubAgent 调用**自动并行**
- **并发上限**：最多同时运行 20 个子代理
- **事件通知**：派发时发送 `SubtaskSpawned` 事件，完成时发送 `SubtaskCompleted` 事件

**参数**：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `agent_name` | string | **是** | 子代理名称/角色（如 `code-reviewer`, `researcher`） |
| `task` | string | **是** | 任务描述（必须自包含，子代理看不到主对话上下文） |

**返回值**（立即返回，不代表任务完成）：

```json
{
  "task_id": "subagent-1",
  "status": "running",
  "agent_name": "code-reviewer"
}
```

**正确用法**：

```
场景：需要同时做三件独立的事

同一响应中并行调用三个 SubAgent：

SubAgent(agent_name="code-reviewer",
         task="审查 src/auth/ 目录下所有 Go 文件的代码质量，
               重点检查：错误处理、并发安全、输入验证。
               输出格式：按文件列出问题级别和具体建议。")

SubAgent(agent_name="researcher",
         task="调研 Go 1.24 中新增的 iter 包和 slog 包的最佳实践，
               总结 5 条可以在项目中应用的改进建议。
               输出格式：结构化的建议列表，每条包含现状分析和改进方案。")

SubAgent(agent_name="test-analyst",
         task="分析 coverage.out 文件，找出覆盖率低于 60% 的包，
               列出未覆盖的关键路径和推荐的测试用例。
               输出格式：按包分组，标注优先级。")

→ 三个调用立即全部返回 {task_id, status: "running"}
→ 三个子代理在后台并行运行
→ 通过 CollectResults（或其他机制）获取结果
```

**task 描述写作规范**：

因为子代理**看不到主对话的上下文**，所以 task 描述必须是**自包含的**：

```
❌ 错误写法（依赖上下文）：
task="按照刚才讨论的方案来实现"

✅ 正确写法（自包含）：
task="在 src/user/service.go 中实现 GetUser 函数：
     1. 从 DB 查询用户记录（使用已有的 userRepo）
     2. 如果未找到返回 404 错误
     3. 过滤掉 password 字段后返回 UserDTO
     4. 参考同目录下的 CreateUser 函数的风格"
```

**命名约定**：
- `agent_name` 应反映角色：`code-reviewer`, `refactor-agent`, `test-generator`, `doc-writer`
- 避免泛化名称如 `worker-1`, `helper`
- 名称会在事件系统中用于追踪

**并发限制**：
- 硬上限：20 个同时运行的子代理
- 超过上限时会阻塞等待（goroutine 信号量控制）
- 合理做法：大批量任务分批派发

---

## 5. 三层协作：编排协议

### 5.1 三种标准编排模式

#### 模式 A：线性任务链（单 Agent 顺序执行）

适用场景：步骤间有强依赖，必须串行。

```
用户请求：重构用户认证模块

第 1 步：规划
  TaskCreate(subject="分析现有认证代码", description="...")
  TaskCreate(subject="提取验证逻辑", description="...")
  TaskCreate(subject="编写单元测试", description="...")
  TaskCreate(subject="更新文档", description="...")

  // 建立依赖链
  TaskUpdate(task_id=task1, addBlocks=[task2])
  TaskUpdate(task_id=task2, addBlocks=[task3])
  TaskUpdate(task_id=task3, addBlocks=[task4])

第 2 步：逐个执行
  TaskUpdate(task_id=task1, status="in_progress")
  ... 执行任务 1 的实际工作 ...
  TaskUpdate(task_id=task1, status="completed")

  TaskUpdate(task_id=task2, status="in_progress")
  ... 执行任务 2 ...
  TaskUpdate(task_id=task2, status="completed")

  ... 以此类推 ...

第 3 步：确认
  TaskList(status_filter="completed")
  → count == 4 ✅ 全部完成
```

**流程图**：

```
Create(×4) → 建依赖链 → Update(in_progress) → 执行 → Update(completed) → 下一个
                                                                              ↓
                                                                    List 确认全部完成
```

#### 模式 B：并行任务扇出（单 Agent 并行派发）

适用场景：多个独立子任务，可以同时进行。

```
用户请求：全面审查项目代码质量

第 1 步：规划
  TaskCreate(subject="审查认证模块", ...)
  TaskCreate(subject="审查数据库层", ...)
  TaskCreate(subject="审查 API 路由", ...)
  TaskCreate(subject="审查配置管理", ...)
  // 这些任务互相独立，不需要 addBlocks/addBlockedBy

第 2 步：并行派发（在同一响应中）
  TaskUpdate(task_id=t1, status="in_progress")
  TaskUpdate(task_id=t2, status="in_progress")
  TaskUpdate(task_id=t3, status="in_progress")
  TaskUpdate(task_id=t4, status="in_progress")

  SubAgent(agent_name="auth-reviewer", task="详细的自包含任务描述...")
  SubAgent(agent_name="db-reviewer",   task="详细的自包含任务描述...")
  SubAgent(agent_name="api-reviewer",  task="详细的自包含任务描述...")
  SubAgent(agent_name="config-reviewer",task="详细的自包含任务描述...")

  → 4 个 SubAgent 同时在后台运行

第 3 步：收集结果并关闭
  CollectResults(task_ids=[subagent-1, subagent-2, subagent-3, subagent-4])
  → 得到每个子代理的结果

  TaskUpdate(task_id=t1, status="completed")  // 逐一标记完成
  TaskUpdate(task_id=t2, status="completed")
  TaskUpdate(task_id=t3, status="completed")
  TaskUpdate(task_id=t4, status="completed")
```

**流程图**：

```
Create(×4) → 全部 Update(in_progress) → 并行 SubAgent(×4) → CollectResults → 全部 Update(completed)
                                     └→ 后台并行执行 ←┘
```

#### 模式 C：团队协作（多 Agent 分工）

适用场景：复杂任务需要不同角色的 Agent 配合。

```
用户请求：实现一个完整的用户管理功能（前后端+测试+文档）

第 1 步：建队并分配任务
  TeamCreate(
    team_name="user-mgmt-team",
    description="实现用户管理 CRUD 功能",
    leader="coordinator",
    members=["api-designer", "frontend-dev", "backend-dev", "test-writer", "doc-writer"],
    tasks=[
      "设计 API schema 和数据模型",
      "实现后端 CRUD 接口",
      "实现前端管理页面",
      "编写 API 集成测试",
      "编写 API 使用文档"
    ]
  )
  → 返回 5 个 task_ids，已轮询分配给 5 个成员

第 2 步：确认分配
  TeamGetTasks(team_name="user-mgmt-team")
  → 确认每个任务的 owner 正确

第 3 步：派发（有依赖的任务先处理依赖关系）
  // 假设 api-designer 的任务要先完成
  TaskUpdate(task_id=design_task, status="in_progress")
  SubAgent(agent_name="api-designer", task="自包含的设计任务描述...")
  // ... 收集结果 ...
  TaskUpdate(task_id=design_task, status="completed")

  // design 完成后，backend 和 frontend 可以并行
  TaskUpdate(task_id=backend_task, status="in_progress")
  TaskUpdate(task_id=frontend_task, status="in_progress")
  SubAgent(agent_name="backend-dev",  task="...")
  SubAgent(agent_name="frontend-dev", task="...")
  // ... 收集结果 ...
  TaskUpdate(task_id=backend_task,  status="completed")
  TaskUpdate(task_id=frontend_task, status="completed")

  // 最后 test 和 doc 可以并行
  TaskUpdate(task_id=test_task, status="in_progress")
  TaskUpdate(task_id=doc_task,  status="in_progress")
  SubAgent(agent_name="test-writer", task="...")
  SubAgent(agent_name="doc-writer",  task="...")
  // ... 收集结果 ...
  TaskUpdate(task_id=test_task, status="completed")
  TaskUpdate(task_id=doc_task,  status="completed")

第 4 步：收尾确认
  TeamGetTasks(team_name="user-mgmt-team")
  → 所有任务 status == "completed" ✅
  TeamDelete(team_name="user-mgmt-team")  // 清理
```

**流程图**：

```
TeamCreate(含 tasks)
    │
    ▼
TeamGetTasks（确认分配）
    │
    ├─→ [Phase 1] design_task → SubAgent → completed
    │                          ↓
    ├─→ [Phase 2] backend ──→ SubAgent ──→ completed ─┐
    │            frontend ─→ SubAgent ──→ completed ─┤
    │                                                   ↓
    ├─→ [Phase 3] test ─────→ SubAgent ──→ completed ─┤
    │            doc ──────→ SubAgent ──→ completed ─┤
    │                                                   ↓
    └─→ TeamGetTasks（确认全部完成）→ TeamDelete
```

### 5.2 工具选择决策树

```
收到用户请求
    │
    ▼
是单一简单任务吗？
    ├── 是 → 直接执行，不需要 Task/SubAgent
    │
    └── 否 → 有多个步骤吗？
              ├── 是 → 需要 TaskCreate 规划
              │        │
              │        ├── 步骤间有依赖？ → 用 addBlocks 建链（模式 A）
              │        └── 步骤互相独立？ → 并行 SubAgent（模式 B）
              │
              └── 否 → 需要多角色协作吗？
                        ├── 是 → TeamCreate + SubAgent（模式 C）
                        └── 否 → 单个 SubAgent 委派即可
```

### 5.3 关键操作约束

| 约束 | 说明 |
|------|------|
| TaskCreate ≠ 执行 | Create 只是记录，真正干活靠直接操作或 SubAgent |
| SubAgent 是异步的 | 调用后立即返回 running，本轮看不到结果 |
| 同步响应内多 SubAgent 自动并行 | 不要分多次响应来"并行"，一次响应里全写上 |
| 状态只能前进 | pending → in_progress → completed/cancelled，不可逆 |
| 依赖不能成环 | 系统自动检测 A→B→A 等循环并拒绝 |
| Session 隔离 | 不同 SessionID 的任务/团队完全隔离，互不可见 |
| 完成立即标记 | 做完就 TaskUpdate(completed)，不要批量攒着 |

---

## 6. 完整实战示例

### 示例 1：代码库全面体检（模式 B — 纯并行）

**用户输入**：对这个 Go 项目做一次全面的代码质量检查

**LLM 的完整操作序列**：

```
=== 第 1 轮：规划 ===

TaskCreate(
  subject="安全漏洞扫描",
  description="检查所有 HTTP handler 的输入验证、SQL 拼接、XSS 风险点",
  active_form="正在进行安全扫描"
)
→ 返回 {task_id: "t-sec"}

TaskCreate(
  subject="性能热点分析",
  description="识别 N+1 查询、缺少索引、大内存分配等性能问题",
  active_form="正在分析性能热点"
)
→ 返回 {task_id: "t-perf"}

TaskCreate(
  subject="错误处理审查",
  description="检查错误是否被适当传播、是否有裸 panic、error wrap 是否规范",
  active_form="正在审查错误处理"
)
→ 返回 {task_id: "t-err"}

TaskCreate(
  subject="测试覆盖率分析",
  description="分析测试报告，找出低覆盖率的包和缺失的关键路径测试",
  active_form="正在分析测试覆盖率"
)
→ 返回 {task_id: "t-test"}

TaskCreate(
  subject="依赖健康度检查",
  description="检查 go.mod 中是否存在过期依赖、已知漏洞版本、废弃导入",
  active_form="正在检查依赖健康度"
)
→ 返回 {task_id: "t-dep"}

=== 第 2 轮：全部标记进行中 + 并行派发 ===

TaskUpdate(task_id="t-sec",  status="in_progress")
TaskUpdate(task_id="t-perf", status="in_progress")
TaskUpdate(task_id="t-err",  status="in_progress")
TaskUpdate(task_id="t-test", status="in_progress")
TaskUpdate(task_id="t-dep",  status="in_progress")

SubAgent(
  agent_name="security-scanner",
  task="你是安全审计专家。请扫描当前项目中的 Go 源文件：\n\
1. 查找所有直接拼接用户输入到 SQL/查询的地方\n\
2. 检查是否有未 sanitize 的 HTML 输出\n\
3. 查看 middleware 中的认证逻辑是否完整\n\
4. 检查敏感数据是否被记录到日志\n\
输出格式：按严重程度(Critical/High/Medium/Low)分类，每项给出文件路径和行号范围。"
)

SubAgent(
  agent_name="perf-analyzer",
  task="你是性能分析专家。分析当前 Go 项目：\n\
1. 搜索可能的 N+1 查询模式（循环内的数据库调用）\n\
2. 检查是否有大量数据一次性加载到内存\n\
3. 查看 goroutine 使用是否有泄漏风险（未处理的 context）\n\
4. 检查是否有不必要的锁竞争\n\
输出格式：按影响程度排序，每项给出优化建议和预期收益。"
)

SubAgent(
  agent_name="error-reviewer",
  task="你是代码质量专家。审查项目的错误处理模式：\n\
1. 统计 error return 的处理方式分布\n\
2. 检查是否有 fmt.Errorf 缺少 %w wrap 的地方\n\
3. 搜索可能被吞掉的错误（_, _ := ...）\n\
4. 检查 defer + recover 的使用是否恰当\n\
输出格式：分类列出问题，标注文件位置和修复建议。"
)

SubAgent(
  agent_name="coverage-analyst",
  task="你是测试专家。分析项目测试覆盖情况：\n\
1. 如果有 coverage.out，解析并找出低于 60% 的包\n\
2. 检查 *_test.go 文件的测试质量（是否只是假断言）\n\
3. 识别缺少测试的关键业务逻辑函数\n\
4. 检查是否有集成测试但缺少单元测试（或反之）\n\
输出格式：按优先级排列的测试补充计划。"
)

SubAgent(
  agent_name="dep-doctor",
  task="你是依赖管理专家。检查项目的 go.mod 和导入：\n\
1. 列出所有直接依赖及其版本\n\
2. 标记出超过 1 年未更新的依赖\n\
3. 检查是否有标准库已经提供但仍在用第三方包的功能\n\
4. 查看 vendor 目录（如有）是否与 go.mod 一致\n\
输出格式：建议升级/移除/替换的依赖清单。"
)

→ 全部立即返回 {task_id: "subagent-N", status: "running"}
→ 5 个子代理在后台并行运行

=== 第 3 轮：收集结果 ===

CollectResults(task_ids=["subagent-1","subagent-2","subagent-3","subagent-4","subagent-5"])
→ 得到 份结果

=== 第 4 轮：汇总 + 关闭任务 ===

[汇总 5 份报告，输出综合分析]

TaskUpdate(task_id="t-sec",  status="completed")
TaskUpdate(task_id="t-perf", status="completed")
TaskUpdate(task_id="t-err",  status="completed")
TaskUpdate(task_id="t-test", status="completed")
TaskUpdate(task_id="t-dep",  status="completed")

→ 收到 nudge: "You have completed 5 tasks. Consider verifying..."
→ 执行最终验证
```

---

### 示例 2：功能开发全流程（模式 C — 团队协作）

**用户输入**：实现一个文章发布系统，包括 API、前端页面、审批流

**LLM 的完整操作序列**：

```
=== 第 1 轮：建队 + 规划 ===

TeamCreate(
  team_name="article-pub-system",
  description="实现文章发布系统：API 设计、后端实现、前端页面、审批流、测试",
  leader="coordinator",
  members=["architect", "backend-dev", "frontend-dev", "workflow-engineer", "qa-engineer"],
  tasks=[
    "设计文章数据模型和 REST API schema（含审批状态机）",
    "实现后端：CRUD 接口、存储层、审批状态流转",
    "实现前端：编辑器页面、列表页、审批操作界面",
    "实现审批工作流引擎（起草→审核→发布/驳回）",
    "编写端到端测试和边界条件测试"
  ]
)
→ 返回 5 个 task_ids，轮询分配给 5 个成员

=== 第 2 轮：建立依赖关系 ===

// architect 必须先完成设计和 API schema
TaskUpdate(task_id=task-architect, addBlockedBy=[])  // 无前置依赖，可以开始

// backend 和 frontend 都依赖 architect 的设计
TaskUpdate(task_id=task-backend,  addBlockedBy=[task-architect])
TaskUpdate(task_id=task-frontend, addBlockedBy=[task-architect])

// workflow-engineer 可以和 backend 并行（都依赖 architect）
TaskUpdate(task_id=task-workflow, addBlockedBy=[task-architect])

// qa 等所有人都做完
TaskUpdate(task_id=task-qa,
  addBlockedBy=[task-backend, task-frontend, task-workflow])

=== 第 3 轮：Phase 1 — 架构设计 ===

TaskUpdate(task_id=task-architect, status="in_progress")
SubAgent(
  agent_name="architect",
  task="设计文章发布系统的技术方案：\n\
数据模型：Article(id, title, content, author_id, status, \
created_at, published_at, reviewer_id)\n\
状态机：draft → pending_review → approved → published\n\
                    └→ rejected → draft\n\
API 端点：POST /articles, GET /articles/:id, PUT /articles/:id,\
 PATCH /articles/:id/status, GET /articles\n\
请输出完整的 OpenAPI 3.0 YAML schema 和数据库 migration SQL。"
)
CollectResults(task_ids=["subagent-arch"])
TaskUpdate(task_id=task-architect, status="completed")

=== 第 4 轮：Phase 2 — 并行实现 ===

// 三个任务可以并行（都只依赖已完成的 architect）

TaskUpdate(task_id=task-backend,  status="in_progress")
TaskUpdate(task_id=task-frontend, status="in_progress")
TaskUpdate(task_id=task-workflow, status="in_progress")

SubAgent(agent_name="backend-dev", task="基于以下 API schema 实现 Go 后端...\n\
[粘贴 architect 输出的 schema]...")
SubAgent(agent_name="frontend-dev", task="基于以下 API schema 实现前端页面...\n\
[粘贴 architect 输出的 schema]...")
SubAgent(agent_name="workflow-engineer", task="实现审批工作流...\n\
[粘贴状态机定义]...")

CollectResults(...)
TaskUpdate(task_id=task-backend,  status="completed")
TaskUpdate(task_id=task-frontend, status="completed")
TaskUpdate(task_id=task-workflow, status="completed")

=== 第 5 轮：Phase 3 — 测试 ===

TaskUpdate(task_id=task-qa, status="in_progress")
SubAgent(agent_name="qa-engineer", task="为文章发布系统编写测试...\n\
[粘贴架构和实现要点]...")
CollectResults(...)
TaskUpdate(task_id=task-qa, status="completed")
→ 收到 nudge（如果这是第 3/6/9... 个完成的任务）

=== 第 6 轮：收尾 ===

TeamGetTasks(team_name="article-pub-system")
→ 确认 5 个任务全部 completed

TeamDelete(team_name="article-pub-system")
```

---

### 示例 3：快速修复（轻量使用，不用 SubAgent）

**用户输入**：修复登录接口的超时问题

**LLM 的操作**：

```
TaskCreate(
  subject="修复登录接口超时",
  description="用户反馈登录请求经常超时，需要排查并修复",
  active_form="正在修复登录超时问题"
)
→ {task_id: "t-fix-1"}

TaskUpdate(task_id="t-fix-1", status="in_progress")

// 直接读取代码、分析问题、实施修复（不使用 SubAgent）
Read(file_path="src/handler/login.go")
// ... 发现是数据库连接池耗尽导致 ...
Edit(file_path="src/handler/login.go", ...)
Edit(file_path="src/db/pool.go", ...)

TaskUpdate(task_id="t-fix-1", status="completed")

// 简单任务不需要 SubAgent，直接做更快
```

---

## 7. 错误处理与边界情况

### 7.1 常见错误及处理方式

| 错误信息 | 原因 | 处理方式 |
|----------|------|----------|
| `subject is required` | TaskCreate 缺少 subject | 补充 subject 参数 |
| `description is required` | TaskCreate 缺少 description | 补充 description 参数 |
| `task_id is required` | TaskGet/Update 缺少 task_id | 使用之前创建/列表返回的 task_id |
| `task "xxx" not found` | task_id 不存在 | 先 TaskList 确认有效 ID |
| `invalid status transition: A → B` | 状态转换非法 | 检查状态机规则 |
| `adding block would create circular dependency` | 循环依赖 | 调整依赖方向 |
| `members is required and must contain at least one agent` | TeamCreate members 为空 | 至少传入 1 个成员 |
| `team "xxx" not found` | TeamGetTasks/Delete 团队不存在 | 先 TeamList 确认 |
| `requires ToolContext with SessionID` | 上下文未初始化 | 框架层面问题，非 LLM 可处理 |
| `SpawnFunc not configured` | SubAgent 未注入 spawn 函数 | 框架配置问题 |

### 7.2 幂等性说明

| 操作 | 重复调用的影响 |
|------|---------------|
| `TaskCreate`(相同内容) | 每次创建新任务（不同 UUID），产生重复任务 |
| `TaskUpdate`(相同值) | 返回 `success: false`（"No changes provided"） |
| `TaskUpdate`(addBlocks 已存在的 ID) | 自动去重，不会重复添加 |
| `TeamCreate`(相同 name) | **会覆盖**同名团队（KVStore Set 语义） |
| `SubAgent`(相同任务) | 每次创建新的后台任务 |

**防重建议**：
- TaskCreate 前先用 TaskList 检查是否已有类似任务
- TeamCreate 前先用 TeamList 检查团队是否已存在

### 7.3 Session 隔离

- 每个 SessionID 有独立的任务/团队命名空间
- 不同 Session 的 `task_id` 可能相同但不冲突
- `ListTasks` / `ListTeams` 只返回当前 Session 的数据
- 测试已验证跨 Session 隔离（见 `TestTaskSessionIsolation`）

### 7.4 并发安全

- `CreateTask` / `UpdateTask` / `DeleteTask` 底层都是 KVStore 的原子操作
- SubAgent 并发上限为 20（信号量控制）
- `TaskUpdate` 的依赖检测（canReach DFS）在读-改-写之间理论上存在 TOCTOU 竞争，但在 LLM 单线程调用场景下不会触发

---

## 8. 推荐的 SystemPrompt 片段

如果你要在 SystemPrompt 中让 LLM 使用这套工具，推荐插入以下片段：

```markdown
## 任务与编排工具使用规范

### 工具概览
你有以下三类工具可用：
- **任务管理**：TaskCreate（创建）、TaskGet（查询）、TaskList（列表）、TaskUpdate（更新状态/依赖）
- **团队编排**：TeamCreate（建队+分配）、TeamList、TeamGetTasks、TeamDelete
- **子代理调度**：SubAgent（异步并行派发任务）

### 核心规则
1. **TaskCreate 只是规划记录**，实际执行靠直接操作工具或 SubAgent
2. **SubAgent 是异步的**——调用后立即返回 running，本轮看不到结果。需要后续收集
3. **同一响应中的多个 SubAgent 调用会自动并行执行**
4. **完成任务后立即 TaskUpdate(status=completed)**，不要攒着批量标记
5. **状态只能前进**：pending → in_progress → completed/cancelled，不可逆
6. **依赖关系用 addBlocks/addBlockedBy 建立**，系统自动拒绝循环依赖

### 编排模式选择
- **简单任务**：直接执行，不需要创建任务
- **多步骤有依赖**：TaskCreate → addBlocks 建链 → 逐个执行 → 逐个完成
- **多步骤独立**：TaskCreate → 全部 in_progress → 并行 SubAgent → 收集 → 全部 completed
- **多角色协作**：TeamCreate（含 tasks）→ 按 phase 派发 SubAgent → TeamGetTasks 确认 → TeamDelete 清理

### SubAgent task 写作要求
- 必须**自包含**：子代理看不到主对话上下文，所有必要信息写在 task 里
- 明确**输出格式**：告诉子代理期望什么格式的结果
- 给出**具体文件/路径**：不要说"那个文件"，要说 "src/auth/middleware.go"
- 设置合理的**角色名称**：反映子代理的专业领域
```

---

> **版本**：v1.0
> **适用工具版本**：GoHarness tools (Task* / Team* / SubAgent)
> **最后更新**：2026-06-15
