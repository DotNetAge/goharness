# Read 工具重构方案

## 概述

`goharness/tools/read.go` 的 Read 工具当前支持文本文件、图片文件和文档格式（PDF/DOCX/XLSX/EPUB → Markdown）的读取，具备分页、Token 截断、路径安全校验等基础能力。本次重构的目标是系统性提升工具的鲁棒性、经济性和对 LLM 消费的友好度。

---

## 现状分析

### 已实现的能力 ✓

| 能力                        | 代码位置                            | 说明                                        |
| --------------------------- | ----------------------------------- | ------------------------------------------- |
| 两层限制分离                | `read.go:171-183`, `read_limits.go` | `MaxSizeBytes`（预检）+ `MaxTokens`（后检） |
| 分段读取（offset/limit）    | `read.go:226-257`                   | 支持行号范围和行数限制                      |
| has_more + next_offset 指引 | `read.go:287-317`                   | 分页提示，含 token 截断提示                 |
| 文档格式自动转换            | `read.go:186-217`, `read_doc.go`    | PDF/DOCX/XLSX/EPUB → Markdown               |
| 路径安全检查                | `utils.go:77-104`                   | symlink 解析 + TOCTOU 保护                  |
| 敏感文件拦截                | `utils.go:161-180`                  | .env, SSH 私钥等                            |
| 参数名模糊匹配              | `bash.go`（`GetParam`）             | 支持多种命名风格                            |
| Token 截断                  | `read.go:260-281`                   | 超过 token 预算时截断并附提示               |

### 缺失/薄弱的能力 ✗

| 能力              | 现状                                 | 问题                 |
| ----------------- | ------------------------------------ | -------------------- |
| 去重缓存          | 无。每次调用都重新读取和发送相同内容 | 浪费上下文 token     |
| 零 I/O 前置校验   | 校验和 I/O 混合在 Execute 中         | 无法提前拦截非法请求 |
| 设备文件拦截      | 无。/dev/zero 等会导致无限阻塞       | 安全风险             |
| ENOENT 渐进兜底   | 仅返回"文件不存在"                   | 用户体验差           |
| 行为指令型结果    | 仅返回 JSON 字段，缺乏自然语言指引   | LLM 需自行推断下一步 |
| 渐进式 Token 估算 | 粗暴的 chars/3 公式                  | 精确度低             |
| 配置优先级链      | 硬编码默认值                         | 不可覆盖             |
| 图片读取          | 不支持                               | 功能缺失             |

---

## 重构目标

| 维度         | 目标                                         | 衡量方式                                  |
| ------------ | -------------------------------------------- | ----------------------------------------- |
| **鲁棒性**   | Fail-fast：I/O 前完成参数校验 + 设备文件拦截 | 前置校验覆盖 10+ 场景                     |
| **降低错误** | ENOENT 三级兜底，错误提示附带操作建议        | 错误信息建议率 > 90%                      |
| **指引性**   | 返回值附带自然语言行为指令                   | 每个返回结果均有 `_suggestion` 或 `_note` |
| **杜绝重复** | 去重缓存 + mtime 比对 + file_unchanged 指令  | 同一文件同 range 重复读取次数减少至 0     |
| **经济性**   | 懒加载 + 渐进估算 + 分段读取引导             | 非必要不读取完整文件                      |
| **可扩展**   | 图片读取 + Hook 机制，功能可插拔             | 通过标志位控制                            |

---

## 重构方案

### A. 前置校验层（Pre-Validation Layer）

**新增 `read_validate.go`：**

在 Execute 的第一个 I/O 操作之前，做纯字符串级别的校验：

```
PreValidate(params) → error
    ├── filePath 存在且非空？
    ├── 设备文件黑名单？
    │   /dev/zero, /dev/random, /dev/urandom, /dev/full
    │   /dev/stdin, /dev/tty, /dev/console
    │   /dev/stdout, /dev/stderr, /dev/fd/{0,1,2}
    │   /proc/*/fd/{0,1,2}
    ├── 二进制扩展名（文档/图片白名单外）？
    └── 敏感文件名？（utils.go:161 已有，迁移至此）
```

**设计约束：** 纯字符串操作，零 I/O。不 stat、不 open、不 resolve symlink。

**文件存在后的权限检查（在 fs.Stat 之后、ReadFileFromFS 之前）：**

```
fs.Stat → ok（文件存在）
    ├── access(path, R_OK) → EACCES？
    │   └── 返回 SuggestionPermissionDenied，跳过读取
    └── ok → 继续读取流程
```

---

### B. 去重缓存层（Dedup Cache）

**新增 `read_dedup.go`：**

```go
// ReadFileState 缓存一次读操作的状态和内容，支持增量扩展。
// 缓存的 key 是 (filePath, startOffset)。
// 例如：先读 offset=1, limit=100，缓存 key=(path, 1) 存 1-100 行。
//       再读 offset=1, limit=200，发现已缓存 100 行，只需补充读取 101-200 行。
//
// 内容存储限制：
//   - 每个文件最多缓存 200 行
//   - 总缓存容量上限 50KB
//   - 超过限制时只缓存元数据（Lines=nil），增量读取退化为全量读取
type ReadFileState struct {
    FilePath   string
    StartLine  int      // 起始行
    Lines      []string // 已缓存的每一行内容（nil 表示超过容量限制，不缓存内容）
    LinesRead  int      // 已缓存的行数（= len(Lines) 或仅元数据）
    MtimeMs    int64    // 读取时的文件 mtime（毫秒）
}

var readFileStates sync.Map
```

**增量读取逻辑：**

```
requestKey = (filePath, startLine)
readFileStates.Load(requestKey)
    ├── 命中且 mtime 一致？
    │   ├── request.lines ≤ cached.LinesRead
    │   │   └── 返回 file_unchanged（缓存覆盖了请求范围）
    │   └── request.lines > cached.LinesRead
    │       └── 只读取 cached.LinesRead + 1 到 request.end 的增量行
    │           → 合并后返回，更新缓存
    └── 未命中 / mtime 变了 → 全量读取
```

**设计约束：**
- 对所有读取生效（不限制 offset 是否存在）。Edit/Write 修改文件会改变 mtime，mtime 比对已能正确处理失效场景
- 通过 `Read.EnableDedup` 标志位控制是否启用
- `ReadFileState` 只在单个 Read 实例内有效，不跨实例污染
- 图片不写入 DedupCache（图片去重收益低，且 Hook 需要重复触发）

**重复失败路径缓存（NegativeCache）：**

配套新增一个 TTL 失效的路径已确认"不存在"缓存，避免模型在不存在路径上反复重试导致重复 I/O：

```go
var negativeCache sync.Map  // key=filePath, value=expireAt

// 路径解析后：
if entry, ok := negativeCache.Load(resolvedPath); ok {
    return nil, fmt.Errorf("路径 %s 不存在（已确认，跳过重复检查）", resolvedPath)
}

// ENOENT 确认后：
negativeCache.Store(resolvedPath, time.Now().Add(5*time.Minute))
```

---

### C. 渐进式 ENOENT 兜底（Progressive Fallback）

**新增 `read_enotfound.go`：**

```
ENOENT
    ├── 第一级：相似文件名匹配
    │   └── findSimilarFile(dir, originalName) → [建议文件名]
    │       ├── 找到 → 附带 "您是否想要读取 xxx？"
    │       └── 未找到 → 第二级
    ├── 第二级：目录内容提示
    │   └── listDirectoryFiles(dir, max=5) → [目录中存在的文件]
    │       ├── 存在 → 附带 "目录中包含以下文件：xxx"
    │       └── 不存在 → 第三级
    └── 第三级：CWD 路径前缀建议
        └── suggestPathUnderCwd(input) → "您是否想读取 projectDir/xxx？"
```

**相似度算法：** Levenshtein Distance，在同一目录下扫描文件名，返回编辑距离 ≤ 3 的建议列表（最多 3 个）。

---

### D. 行为指令型结果（Behavioral Directives）

**返回值结构标准化：**

```go
// ReadResult 是 Read.Execute 的正式返回值类型。
//
// 层级设计：
//   Data       — map 层，序列化为 JSON 输出（包含 _note, _suggestion, content 等），
//                向后兼容当前 map[string]any 的消费者。
//   Images     — 图片数据层，由 ReadHook 填充、Executor 消费（非 JSON 字段）。
//   SideEffect — 消息注入层，携带需要注入 LLM 上下文的消息（非 JSON 字段）。
type ReadResult struct {
    Data       map[string]any `json:"-"`        // JSON 输出数据（_note, _suggestion, content, ...）
    Images     []ImageContent `json:"-"`         // 图片数据（ReadHook 输出，由 Executor 注入 LLM）
    SideEffect []Message      `json:"-"`         // 需要注入 LLM 上下文的消息
}

// `json:"-"` 确保 Images 和 SideEffect 不会出现在 JSON 序列化中。
// Data 中的 _suggestion 使用 Suggestion* 常量赋值。
```

**Suggestion 常量定义（`read_suggestion.go`）：**

将 `_suggestion` 定义为 Go 常量，避免字符串字面量散落各处导致不一致：

```go
const (
    SuggestionReadComplete      = "read_complete"       // 正常读取，无剩余
    SuggestionHasMoreLines      = "has_more_lines"      // 有剩余行未读
    SuggestionTruncatedByToken  = "truncated_by_tokens" // Token 预算截断
    SuggestionFileTooLarge      = "file_too_large"      // 文件过大（预检拒绝）
    SuggestionContentUnchanged  = "content_unchanged"   // 文件未变化
    SuggestionDocConverted      = "doc_converted"       // 文档转换完成
    SuggestionImageRead         = "image_read"          // 图片已读取
    SuggestionImageFailed       = "image_failed"        // 图片读取失败
    SuggestionEmptyFile         = "empty_file"          // 文件为空
    SuggestionPermissionDenied  = "permission_denied"   // 无读取权限
)
```

**每个场景的指令映射：**

| 场景             | `_suggestion`                | `_note`                                                             |
| ---------------- | ---------------------------- | ------------------------------------------------------------------- |
| 正常读取，无剩余 | `SuggestionReadComplete`     | （空）                                                              |
| 有剩余行未读     | `SuggestionHasMoreLines`     | `在偏移量 N 处有更多可用内容...使用 offset 和 limit 继续读取`       |
| Token 截断       | `SuggestionTruncatedByToken` | `在 N 个 token 处截断...使用更小的 range`                           |
| 文件过大（预检） | `SuggestionFileTooLarge`     | `文件太大。使用 offset 和 limit 参数读取特定部分`                   |
| 文件未变化       | `SuggestionContentUnchanged` | `文件未变化，引用之前的结果`                                        |
| 文档转换完成     | `SuggestionDocConverted`     | `已转换为 Markdown`                                                 |
| 图片已读取       | `SuggestionImageRead`        | `图片 xxx 已读取。图片数据通过独立的 ImageUrl Message 发送至上下文` |
| 图片读取失败     | `SuggestionImageFailed`      | `图片读取失败：{原因}`                                              |
| 文件为空         | `SuggestionEmptyFile`        | `文件为空，无内容可读取`                                            |
| 无读取权限       | `SuggestionPermissionDenied` | `无读取权限，请检查文件权限或使用 shell 命令读取`                   |

---

### E. 经济性读入（Economic Reading）

**E1. 动态默认行数**

基于文件大小自适应 `defaultLines`：

| 文件大小       | defaultLines | 行为                  |
| -------------- | ------------ | --------------------- |
| < 1 KB         | 不限         | 全部读取              |
| 1 KB - 32 KB   | 500          | 当前默认值            |
| 32 KB - 256 KB | 200          | 强制分段，附 has_more |
| > 256 KB       | 拒绝         | 强制使用 offset/limit |

**E2. 两级 Token 估算**

```
第一级（快速）：estimatedTokens = outputChars / 3
    ├── ≤ maxTokens → 直接返回（无需截断）
    └── > maxTokens → 第二级

第二级（按比例截断）：
    ratio = maxTokens / (outputChars / 3)    // 约 0.0 ~ 1.0
    keepLines = int(totalLines * ratio)       // 按行数比例保留
    截断到前 keepLines 行，附自然语言指引
    // 无需 tokenizer，精度足够用于截断决策
```

---

### F. 图片读取（Image Reading）

**职责分离：**

| 角色                       | 职责                                                         | 产出                                  |
| -------------------------- | ------------------------------------------------------------ | ------------------------------------- |
| **Read.Execute**           | 读取图片文件字节，进行大小检查                               | 返回**纯文字**结果（不包含图片数据）  |
| **ReadHook**               | 压缩图片（最大边长 512px）→ base64 编码 → 生成 ImageUrl 消息 | 返回 `ImageContent`（含 base64 数据） |
| **Executor/Agent Runtime** | 将 Hook 返回的 ImageContent 注入到 LLM 上下文                | 独立的 ImageUrl Message               |

**Read 端不存储图片相关路径配置**——图片如何压缩、编码是 Hook 的事。

**核心约束：**
- 图片在 Hook 层被压缩，**最大边长不超过 512px**（保持宽高比）
- 压缩后的图片被编码为 base64，以 `data:image/{type};base64,{data}` 格式发送给 LLM
- Read 的返回值中**不包含**任何图片数据，只有文字说明

**Read 端的变更：**

```go
type Read struct {
    info      *ToolInfo
    limits    FileReadingLimits
    whitelist []string
    hooks     []ReadHook          // 新增：后处理 Hook 链

    EnableImageReading bool       // 新增：是否启用图片读取
}
```

**支持格式与边界处理：**

| 格式              | 压缩策略               | 风险 / 特殊处理                                      |
| ----------------- | ---------------------- | ---------------------------------------------------- |
| `.png`            | resize → JPEG quality  | 标准位图，无特殊处理                                 |
| `.jpg` / `.jpeg`  | resize → JPEG quality  | 标准位图，无特殊处理                                 |
| `.webp`           | resize → JPEG quality  | Go 1.21+ 标准库原生支持                              |
| `.bmp`            | resize → JPEG quality  | 解码后按位图处理                                     |
| `.gif` 静态       | resize → JPEG quality  | 按单帧位图处理                                       |
| `.gif` 动画       | 仅取第一帧             | resize 会破坏动画帧同步，只取首帧                    |
| `.svg`            | 不 resize，直接 base64 | 矢量图无法用 bitmap resize；原尺寸 base64 编码返回   |
| `.heic` / `.heif` | **不支持**             | Go 无原生支持；需 cgo/三方库，返回"不支持的图片格式" |

**读取流程：**

```
检测到图片扩展名
    ├── EnableImageReading = false
    │   └── 返回："此文件是图片格式，当前未启用图片读取。如需读取请启用 EnableImageReading"
    │
    └── EnableImageReading = true
        ├── 大小检查（MaxImageSizeBytes，默认 10MB）
        ├── 读取文件到 buffer
        ├── 触发 ReadHook.OnFileRead()  ← Hook 负责压缩 + base64
        │   └── Hook 返回 []ImageContent（含压缩后的 base64 数据）
        ├── 不写入 DedupCache（图片去重收益低，且 Hook 需重复触发）
        └── Read 返回纯文字结果：
            {
                "_note":       "图片 /path/to/img.png 已读取（原始 %d 字节，压缩后 %d 字节）",
                "_suggestion": "image_read",
                "success":     true,
                "path":        "/path/to/img.png",
                "size_bytes":  12345,
                "content":     "图片数据已通过独立的 ImageUrl Message 发送至 LLM 上下文。"
            }
            // ↑ 只有文字，没有 base64 数据
```

---

### G. ReadHook 机制

**用途：** Read 读取完成后，Hook 对图片进行**压缩（最大边长 512px）→ base64 编码**，生成 ImageUrl 类型的 Content 消息，由 Executor 注入到 LLM 上下文。

**ImageUrl 内容类型定义（`content_types.go`）：**

```go
// ImageContent 表示一个 ImageUrl 类型的消息内容。
// 由 ReadHook 生成，由 Executor 注入到 LLM 上下文。
type ImageContent struct {
    MediaType      string // 如 "image/png", "image/jpeg"
    Base64Data     string // 压缩后图片的 base64 编码字符串（不含 data: URI 前缀）
    AltText        string // 可选的替代文本
    Width          int    // 压缩后的宽（px）
    Height         int    // 压缩后的高（px）
    RawSize        int64  // 原始文件大小（字节）
    CompressedSize int    // 压缩后 base64 前的字节数
}
```

**压缩策略：**

```
resizeImage(data []byte, maxSide int) → (resized []byte, width, height int)
    1. 解码图片（支持 png/jpeg/webp/gif/bmp）
    2. 计算缩放比例：scale = min(maxSide / width, maxSide / height)
    3. 如果 width ≤ maxSide 且 height ≤ maxSide，跳过缩放
    4. 缩小到 (width*scale, height*scale)
    5. 以 JPEG quality=85 重新编码
    6. 返回压缩后的字节 + 新尺寸
```

**Hook 接口定义（`read_hook.go`）：**

```go
// ReadHook 在 Read 工具完成文件读取后被调用。
// 实现方可以返回零个或多个 ImageContent，由 Executor 注入到 LLM 上下文。
type ReadHook interface {
    // OnFileRead 在每次文件读取完成后调用。
    // 参数：
    //   - ctx:        上下文
    //   - filePath:   已读取文件的完整路径
    //   - content:    文件原始字节
    //   - result:     Read 即将返回的结果（可读，不建议修改）
    // 返回：
    //   - []ImageContent: 要注入 LLM 上下文的 ImageUrl 消息列表（可为 nil）
    //   - error:          Hook 执行错误（不影响 Read 的主流程）
    OnFileRead(ctx context.Context, filePath string, content []byte, result map[string]any) ([]ImageContent, error)
}
```

**默认图片 Hook 实现（ImageResizeHook）：**

```go
// ImageResizeHook 对图片进行压缩（最大边长 512px）→ base64 编码。
// Executor 收到返回的 ImageContent 后，以 data:image URI 格式发送至 LLM。
//
// SVG 特殊处理：SVG 是矢量图，不解码不 resize，直接 base64 编码原始内容返回。
type ImageResizeHook struct {
    MaxSide int // 最大边长（px），默认 512
    Quality int // JPEG 编码质量（1-100），默认 85
}

// 压缩策略：
//   原始大小 < 1MB   → Quality=90（高清晰度）
//   原始大小 1-5MB   → Quality=85（默认）
//   原始大小 > 5MB   → Quality=70（激进压缩）
func defaultQuality(rawSize int64) int { ... }

func (h *ImageResizeHook) OnFileRead(ctx context.Context, filePath string, content []byte, result map[string]any) ([]ImageContent, error) {
    // 1. 检查文件扩展名，判定是否为图片（非图片返回 nil, nil）
    // 2. 如果是 SVG，不解码不 resize，直接 base64 编码返回
    // 3. 解码图片（支持 png/jpeg/webp/gif/bmp）
    // 4. 压缩：最长边不超过 MaxSide（默认 512px），保持宽高比
    // 5. 以 JPEG 编码（quality 由 defaultQuality 或用户配置决定）
    // 6. base64 编码压缩后的字节
    // 7. 组装 ImageContent
    return []ImageContent{...}, nil
}
```

**Execute 中的调用流程（重要：Hook 先于 Dedup 执行）：**

```go
// 1. 读取完成
data, err := readFile(...)

// 2. 先执行 Hook（图片数据不写入 DedupCache）
var imageContents []ImageContent
for _, hook := range r.hooks {
    images, err := hook.OnFileRead(ctx, resolvedPath, data, result)
    if err != nil {
        // Hook 失败不回写 DedupCache，下次可重新触发
        // 同时修改 Read 的文字结果，保持语义一致
        result["_note"] = "图片读取失败：" + err.Error()
        result["_suggestion"] = SuggestionImageFailed
        result["content"] = "图片无法加载到 LLM 上下文中：" + err.Error()
        continue
    }
    imageContents = append(imageContents, images...)
}

// 3. 只有文本文件写入 DedupCache（图片不去重）
if !isImageFile(resolvedPath) {
    readFileStates.Store(dedupKey, ReadFileState{...})
}

// 4. 通过 ReadResult 结构体返回
return &ReadResult{
    Data:      result,
    Images:    imageContents,
    SideEffect: nil,
}, nil
```

**Executor 侧的处理：**

```go
// Executor 收到 ReadResult 后：
result, err := readTool.Execute(ctx, params)
rr := result.(*ReadResult)

// 注入图片消息到 LLM 上下文
for _, img := range rr.Images {
    llmContext.AddMessage(Message{
        Role: "user",
        Content: ImageUrlContent{
            Type: "image_url",
            ImageURL: ImageURL{
                URL: "data:" + img.MediaType + ";base64," + img.Base64Data,
            },
        },
    })
}

// 处理 SideEffect 消息
for _, msg := range rr.SideEffect {
    llmContext.AddMessage(msg)
}
```

**完整示例——启用图片读取：**

```go
readTool := NewReadTool().
    EnableImageReading(true).
    AddHook(&ImageResizeHook{
        MaxSide: 512,   // 最大边长 512px
        Quality: 85,    // JPEG 质量
    })
```

---

### H. 配置优先级链

**扩展 `FileReadingLimits`：**

```go
type FileReadingLimits struct {
    MaxSizeBytes      int64 // 默认 256KB
    MaxTokens         int   // 默认 25,000
    DefaultLines      int   // 默认 500（动态策略替代硬编码）
    MaxImageSizeBytes int64 // 图片大小限制，默认 10MB
    MaxDocSizeBytes   int64 // 文档大小限制，默认 50MB
}
```

**配置优先级：**
```
环境变量 GOHARNESS_READ_MAX_TOKENS
    → Runtime Config（session 或全局配置）
        → DefaultFileReadingLimits() 硬编码默认值
```

通过 `sync.Once` 锁定，防止会话中途变更。

---

## 目标数据流

```
用户输入 (params)
    │
    ▼
PreValidate(params)  ← 零 I/O 前置校验
    ├── filePath 非空？
    ├── 设备文件黑名单？
    ├── 二进制扩展名（白名单外）？
    └── 敏感文件名？
    │
    ▼   (通过)
NegativeCache 检查  ← 新增
    ├── 路径在 TTL 内被确认不存在？
    │   └── 快速返回错误，跳过 I/O
    └── 未命中 → 继续
    │
    ▼
路径解析 (ResolveTargetPath + ValidateFileSafety)
    │
    ▼
DedupCache 检查  ← 新增（仅文本文件）
    ├── 命中 + mtime 未变 + 缓存覆盖请求范围
    │   └── 返回 file_unchanged（指令型 Prompt）
    ├── 命中 + mtime 未变 + 缓存不足
    │   └── 增量读取，合并返回
    └── 未命中/mtime 已变 → 继续
    │
    ▼
fs.Stat → 大小检查
    ├── ENOENT？
    │   ├── 写入 NegativeCache
    │   └── 渐进兜底（相似文件名 → 目录内容 → CWD 建议）
    │
    ├── 权限拒绝（EACCES）？
    │   └── 返回 SuggestionPermissionDenied
    │
    ├── 空文件（size=0）？
    │   └── 返回 SuggestionEmptyFile
    │
    ├── 文档格式 (PDF/DOCX/XLSX/EPUB)
    │   └── convertDocument() → docResult + 行为指令
    │
    ├── 图片格式 ← 受 EnableImageReading 控制
    │   ├── 大小检查
    │   └── 读取文件到 buffer（不写入 DedupCache）
    │
    └── 文本文件
        ├── 动态 defaultLines
        ├── readFileInRange(offset, limit)
        ├── 两级 Token 估算
        └── 写入 DedupCache
    │
    ▼
ReadHook.OnFileRead()  ← Hook 先于 DedupCache 写入执行
    ├── 成功 → ReadResult.Images
    └── 失败 → 回写 ReadResult._suggestion = image_failed
    │
    ▼
ReadResult 返回
    ├── Data:    (_note, _suggestion, content, ...)
    ├── Images:  []ImageContent（由 Executor 注入 LLM）
    └── SideEffect: []Message（可选注入）
    │
    ▼
Executor / Runtime
    ├── Images → 生成独立的 ImageUrl Message
    └── SideEffect → 注入 LLM 上下文
```

---

## 实施优先级

| 优先级 | 模块                                                   | 影响                | 工作量  |
| ------ | ------------------------------------------------------ | ------------------- | ------- |
| P0     | **行为指令型结果**（read.go）                          | 提升 LLM 理解效率   | ~30 行  |
| P0     | **去重缓存**（read_dedup.go）                          | 直接影响 token 消耗 | ~50 行  |
| P1     | **ENOENT 渐进兜底**（read_enotfound.go）               | 用户体验改善明显    | ~80 行  |
| P1     | **设备文件黑名单**（read_validate.go）                 | 安全性              | ~30 行  |
| P2     | **两级 Token 估算**（read.go）                         | 经济性优化          | ~20 行  |
| P2     | **配置优先级链**（read_limits.go）                     | 可配置性            | ~15 行  |
| P3     | **动态默认行数**（read.go）                            | 经济性优化          | ~15 行  |
| P3     | **图片读取 + ReadHook**（read_image.go, read_hook.go） | 功能扩展            | ~150 行 |

---

## 影响范围评估

| 改动点                                                     | 影响文件              | 兼容性             |
| ---------------------------------------------------------- | --------------------- | ------------------ |
| 新增 `PreValidate` 纯字符串校验                            | 不修改现有接口        | 完全向下兼容       |
| 新增 `readFileStates` `sync.Map`                           | 包级变量，不入参      | 完全向下兼容       |
| 返回结果增加 `_note`/`_suggestion`                         | 调用方按需使用        | 兼容（新增字段）   |
| 新增 `EnableImageReading` 标志位                           | Read struct           | 兼容（默认 false） |
| 新增 `hooks []ReadHook`                                    | Read struct           | 兼容（默认 nil）   |
| ENOENT 渐进兜底改为非错误返回                              | read.go               | **需要调用方适配** |
| 配置优先级链                                               | NewReadToolWithLimits | 完全向下兼容       |
| `Execute` 返回类型 `(any, error)` → `(*ReadResult, error)` | 调用方                | **需要调用方适配** |

---

## 遗留问题

以下问题在本次设计范围外，建议在后续迭代中处理：

### 1. 文件编码检测

当前读取逻辑使用 `strings.Split(string(data), "\n")` 按行分割，假设文件为 UTF-8 编码。UTF-16、Latin-1 等编码会因此产生乱码或分割错误。

**建议：** 在读取后检测 BOM（Byte Order Mark），非 UTF-8 文件先转码再处理。可复用 Go 的 `golang.org/x/text/encoding` 或简化处理——遇到 BOM 则转码，无 BOM 默认 UTF-8。
