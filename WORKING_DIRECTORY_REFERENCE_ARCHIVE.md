# Goreact 项目"工作目录"概念引用归档

> 生成时间: 2026-05-12
> 搜索范围: /Users/ray/workspaces/ai-ecosystem/goreact

---

## 一、中文直接提及 - "工作目录"

### 1. TODO.md
**文件位置**: `TODO.md:96`
**内容**:
```
一切的脚本，运行都是基于会话上下文运行的，因此运行的工作目录就是会话的存储目录
```

---

## 二、英文 "Working Directory" 概念引用

### 1. reactor/prompt.go - 系统提示词环境信息
**文件位置**: `reactor/prompt.go:211-227`
**功能**: 向 AI Agent 汇报运行时环境信息时使用工作目录概念

```go
func BuildEnvironmentInfo(sessionID string, sessionDir string) string {
    cwd, _ := os.Getwd()
    platform := runtime.GOOS
    osVersion := runtime.GOARCH
    shell, _ := os.LookupEnv("SHELL")

    return fmt.Sprintf(`## Environment
You have been invoked in the following environment:
- Primary working directory: %s
- Platform: %s
- OS version: %s
- Shell: %s
- Session ID: %s
- Pwd: %s
- App name: %s
- App version: %s
%s`,
        cwd,
        platform,
        osVersion,
        // ...
    )
}
```

**关键点**: 
- 使用 `os.Getwd()` 获取当前工作目录
- 将其作为 "Primary working directory" 暴露给 Agent
- 同时暴露 sessionDir 作为 Pwd

---

### 2. tools/run_script.go - 脚本执行工具
**文件位置**: `tools/run_script.go:626-681`
**功能**: 定义脚本执行工具的 `working_dir` 参数

**参数定义 (L626-630)**:
```go
{
    Name:        "working_dir",
    Type:        "string",
    Description: "Working directory for script execution. Defaults to the current directory. Usually the {base_dir} of the active skill.",
    Required:    false,
}
```

**参数使用 (L653-681)**:
```go
workingDir, _ := params["working_dir"].(string)
if workingDir == "" {
    workingDir = "."
}
workingDir = filepath.Clean(workingDir)

language, scriptPath := parseCommand(command, workingDir)
// ...
result, err := t.scriptExecutor.Execute(ctx, workingDir, scriptPath, args)
```

**关键点**:
- 显式支持 `working_dir` 参数控制脚本执行目录
- 默认值为当前目录 (".")
- 参数会被清理和规范化处理

---

### 3. tools/bash.go - Bash 命令执行工具
**文件位置**: `tools/bash.go:94,108`
**功能**: Bash 工具描述中提到工作目录概念

**相关内容**:
```
L94: Executes a given bash command and returns its output. The working directory persists between commands, but shell state does not.
L108: - Try to maintain your current working directory by using absolute paths.
```

---

### 4. tools/powershell.go - PowerShell 执行工具
**文件位置**: `tools/powershell.go:195`
**功能**: 提及当前工作目录

**相关内容**:
```go
sb.WriteString("- Working directory is the current working directory\n")
```

---

## 三、工作目录安全校验机制

### tools/utils.go - 路径安全验证函数
**文件位置**: `tools/utils.go:34-68`
**功能**: 确保所有文件访问都在当前工作目录范围内

```go
// ValidateFileSafety verifies file access safety using path anchoring.
// It normalizes the path via filepath.Clean, resolves symlinks, and ensures
// the real path stays within the allowed workspace boundary.
func ValidateFileSafety(path string) error {
    // Step 1: Clean the path to eliminate relative components like ../
    cleaned := filepath.Clean(path)

    // Step 2: Resolve to absolute path
    absPath, err := filepath.Abs(cleaned)
    if err != nil {
        return fmt.Errorf("failed to get absolute path: %w", err)
    }

    // Step 3: Resolve symlinks to get the real path
    realPath, err := filepath.EvalSymlinks(absPath)
    if err != nil {
        if !os.IsNotExist(err) {
            return fmt.Errorf("failed to resolve symlinks: %w", err)
        }
        realPath = absPath
    }

    // Step 4: Resolve the working directory's real path (also resolving symlinks)
    cwd, err := os.Getwd()
    if err != nil {
        return fmt.Errorf("failed to get working directory: %w", err)
    }
    realCwd, err := filepath.EvalSymlinks(cwd)
    if err != nil {
        realCwd = cwd
    }

    // Step 5: Ensure the real path is anchored within the current working directory
    if !strings.HasPrefix(realPath, realCwd+string(filepath.Separator)) && realPath != realCwd {
        return fmt.Errorf("access denied: path %q resolves to %q which is outside the workspace %q", path, realPath, realCwd)
    }
    return nil
}
```

**关键点**:
- 五步验证流程：清理路径 → 绝对化 → 解析符号链接 → 获取工作目录真实路径 → 边界检查
- 防止路径遍历攻击（../）
- 确保访问不会超出工作目录边界

---

## 四、Workspace（工作空间）沙箱配置体系

### 1. tools/sandbox.go - 沙箱配置类型定义
**文件位置**: `tools/sandbox.go:14-16`

```go
type SandboxProfile string

const (
    ProfileSandbox    SandboxProfile = "sandbox"      // 完整沙箱隔离
    ProfileWorkspace  SandboxProfile = "workspace"    // 工作空间模式（默认）
    ProfileUnconfined SandboxProfile = "unconfined"   // 无限制模式
)

type SandboxConfig struct {
    Enabled      bool
    Profile      SandboxProfile
    AllowedPaths []string
    AllowNetwork bool
    TempDir      string
    CustomPolicy string
    mu           sync.RWMutex
}
```

### 2. 默认沙箱配置
**文件位置**: `tools/sandbox.go:77-86`

```go
func DefaultSandboxConfig() *SandboxConfig {
    cwd, _ := os.Getwd()
    return &SandboxConfig{
        Enabled:      true,
        Profile:      ProfileWorkspace,       // 默认使用 Workspace 模式
        AllowedPaths: []string{cwd},          // 当前工作目录是唯一允许的路径
        AllowNetwork: true,
        TempDir:      filepath.Join(os.TempDir(), "goreact-sandbox"),
    }
}
```

### 3. 受限沙箱配置
**文件位置**: `tools/sandbox.go:95-100`

```go
func RestrictedSandboxConfig(allowedPaths ...string) *SandboxConfig {
    cwd, _ := os.Getwd()
    if len(allowedPaths) == 0 {
        allowedPaths = []string{cwd}          // 默认限制到当前工作目录
    }
    return &SandboxConfig{
        // ...
    }
}
```

### 4. 平台特定实现
| 平台 | 文件 | 相关行号 |
|------|------|----------|
| macOS | `sandbox_darwin.go` | L64-65, L69, L128 (`generateWorkspaceProfile`) |
| Linux | `sandbox_linux.go` | L67, L134, L188, L225 |
| Windows | `sandbox_windows.go` | L26-27, L45 (`applyWindowsWorkspaceIsolation`) |

---

## 五、其他涉及 cwd/工作目录的位置

### 1. tools/skill.go - Skills 目录解析
**文件位置**: 
- `tools/skill.go:157-159`: 默认 skills 目录为 `./skills` 相对于 cwd
- `tools/skill.go:250-251`: 备选 skills 搜索路径

```go
// L157-159: Default: ./skills relative to cwd
if cwd, err := os.Getwd(); err == nil {
    defaultDir := filepath.Join(cwd, "skills")
}

// L250-251: 备选路径
if cwd, err := os.Getwd(); err == nil {
    dirs = []string{filepath.Join(cwd, "skills")}
}
```

### 2. tools/ls.go - 列出目录工具
**文件位置**: `tools/ls.go:29,45,58`

```go
// L29: Call with no parameters to list the current directory.
// L45: {Name: "path", Type: "string", Description: "Directory path to list. Defaults to current directory ('.').", Required: false},
// L58: Get directory path (defaults to current directory)
```

### 3. core/runtime.go - 运行时状态
**文件位置**: 
- `core/runtime.go:12`: 注释提到 "runtime directory"
- `core/runtime.go:75`: 注释提到 "RuntimeDirectory"

```go
// AgentState represents the current state of an agent in the runtime directory.
State      AgentState    // Current runtime state (mutated by RuntimeDirectory)
```

### 4. intercept_test.go - 测试辅助函数
**文件位置**: `intercept_test.go:164-183`

```go
func findWorkspace(t *testing.T) string {
    cwd, _ := os.Getwd()
    dir := cwd
    // 尝试多个可能的路径...
    candidates := []string{
        filepath.Join(cwd, "mindx", "runtime"),
        filepath.Join(cwd, "..", "mindx", "runtime"),
        filepath.Join(os.Getenv("HOME"), "workspaces", "ai-ecosystem", "mindx", "runtime"),
    }
    // ...
}
```

该测试文件大量使用 `workspace` 变量作为基础路径进行配置加载。

---

## 六、统计汇总

| 类别 | 数量 |
|------|------|
| 中文"工作目录"直接提及 | 1 处 |
| 英文 "Working Directory" 引用 | 15 处 |
| `os.Getwd()` 调用 | 12 处 |
| "workspace"/"Workspace" 引用 | 63 处 |
| 涉及文件总数 | 约 15 个文件 |

---

## 七、核心发现总结

### 工作目录在该项目中的作用层次：

1. **运行时上下文层**
   - 通过 `os.Getwd()` 获取系统当前工作目录
   - 注入到 AI Agent 的系统提示词中（prompt.go）
   - 作为环境感知能力的一部分

2. **命令执行层**
   - `run_script` 工具支持显式指定 `working_dir`
   - `bash`/`powershell` 工具在描述中强调维护工作目录
   - 脚本执行的默认工作目录为当前目录或 skill 的 base_dir

3. **安全隔离层**
   - `ValidateFileSafety()` 函数以工作目录为信任边界
   - 沙箱系统的 `ProfileWorkspace` 模式默认限制到 cwd
   - 所有文件读写操作都经过工作目录校验

4. **资源定位层**
   - Skills 默认目录相对于 cwd 解析
   - 测试代码通过 `findWorkspace()` 定位项目根目录
   - 运行时状态管理涉及 runtime directory 概念

5. **设计意图层** (TODO.md)
   - 明确提出：**"运行的工作目录就是会话的存储目录"**
   - 这暗示了 Session 与 Working Directory 的绑定关系设计方向

---

## 八、潜在关联点

结合 mindx/TODO.md 中提到的：
```
思路1: 将 SessionID 与 工作目录绑定，一个会话就必须与一个工作目录绑定；
```

goreact 项目已经具备了以下基础设施支持这一思路：

✅ 已有 SessionID 和 Pwd 在系统提示词中暴露  
✅ 已有基于工作目录的安全校验机制  
✅ 已有沙箱的 Workspace 隔离模式  
✅ TODO 中已确认"工作目录 = 会话存储目录"的设计决策  
⚠️ 目前尚未看到显式的 Session ↔ Working Directory 绑定逻辑实现  

---

*文档结束*
