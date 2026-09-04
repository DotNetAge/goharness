package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"unicode/utf8"
)

// 引导式错误消息（Guide）。
//
// 设计原则：授权与指导是本次重构的关键，尤其是指引。所有可预知的错误路径，
// 工具返回给模型的提示都必须采用「我做了什么 → 导致了什么错误 → 下一步我应该
// 怎么做」的第一人称引导格式，帮助（尤其是本地小型的）模型从失败中自主纠正
// 思路，而不是面对一串裸错误原地打转。只有超出预知范围的未知错误才原样返回
// （默认兜底）。
//
// 文案规范（重要）：
//   - action：我做了什么（第一人称，含具体路径/参数等上下文）
//   - cause：可预知的错误描述（说明问题是什么），禁止直接把 error.Error() 整段
//     塞进 cause——底层错误只通过 WithErrDetail 作为补充，且超长会被截断
//   - next：明确的解决方案（做法），而非「请检查 XXX 重试」式模糊话术。
//     推荐句式：「先自查：我传入的 XX 是否正确？若确认是 XX，应 YY；无法自行
//     解决时应询问用户」

// BuildGuide 组装第一人称引导式错误消息。
//
// 返回形如：
//
//	我尝试读取文件 "/x"，但该文件不存在。
//	原因：目标路径在文件系统中不存在。
//	下一步我应该：检查路径拼写，使用 Glob 或 Ls 定位正确路径后重新读取。
func BuildGuide(action, cause, next string) string {
	return fmt.Sprintf("我%s。\n原因：%s。\n下一步我应该：%s", action, cause, next)
}

// errDetailMaxRunes 底层错误信息保留的最大长度。
// 底层错误只是补充细节，过长会浪费 Token，故截断。
const errDetailMaxRunes = 120

// WithErrDetail 将底层错误信息附加到原因描述末尾（长度受 errDetailMaxRunes 限制）。
// cause 必须是可预知的错误描述；底层 error 只作为补充细节：
//   - 信息超长会被截断，避免浪费 Token
//   - err 为 nil 时原样返回 cause
func WithErrDetail(cause string, err error) string {
	if err == nil {
		return cause
	}
	detail := err.Error()
	if n := utf8.RuneCountInString(detail); n > errDetailMaxRunes {
		detail = TruncateString(detail, errDetailMaxRunes)
	}
	return fmt.Sprintf("%s（底层错误：%s）", cause, detail)
}

// ── 通用沙箱未注入引导（所有工具共用）─────────────────────────────

// GuideSandboxRequired 会话未注入沙箱，工具拒绝执行（安全决策统一收口到沙箱）。
func GuideSandboxRequired(toolName string) string {
	return BuildGuide(
		fmt.Sprintf("尝试调用 %s 工具，但会话未注入沙箱", toolName),
		"安全决策（工作区边界、敏感文件拦截、危险命令检测、命令白名单、网络预检）已统一收口到沙箱（sandbox.Sandbox），工具自身不做任何授权检查，未注入沙箱时一律拒绝执行",
		"创建会话时通过 session.WithSandbox(sb) 注入沙箱实例后重试；这是调用方配置错误，授权（PermissionAllow）无法解除",
	)
}

// ── Read 工具的可预知错误路径 ──────────────────────────────────────

// GuideReadFileNotFound 目标文件不存在（ENOENT）。
func GuideReadFileNotFound(path string) string {
	return BuildGuide(
		fmt.Sprintf("尝试读取文件 %q，但该文件不存在", path),
		"目标路径在文件系统中不存在（ENOENT），可能是路径拼写、大小写或相对路径基准有误",
		"使用 Glob 或 Ls 工具在工作区内定位正确的文件路径，修正后重新读取",
	)
}

// GuideReadPermissionDenied 无读取权限。
func GuideReadPermissionDenied(path string) string {
	return BuildGuide(
		fmt.Sprintf("尝试读取文件 %q，但没有读取权限", path),
		"当前进程对该文件没有读取权限",
		"检查文件权限；若确实需要读取该文件，改用其它有权限的路径或方式",
	)
}

// GuideReadIsDirectory 目标是目录而不是文件。
func GuideReadIsDirectory(path string) string {
	return BuildGuide(
		fmt.Sprintf("尝试读取路径 %q，但它是目录而不是文件", path),
		"Read 只能读取文件内容，不能读取目录",
		"使用 Ls 列出该目录的内容，或指定目录内具体文件的路径后重新读取",
	)
}

// GuideReadFileTooLarge 文件超过单次读取上限。
func GuideReadFileTooLarge(path string, sizeKB float64, maxKB int64) string {
	return BuildGuide(
		fmt.Sprintf("尝试读取文件 %q（%.2f KB），超过单次读取上限 %d KB", path, sizeKB, maxKB),
		"文件过大，全量读取会浪费上下文空间",
		"使用 offset 与 limit 参数分页精读（offset 为起始行号，limit 为最大行数），或先用 Glob/Grep 定位相关内容后只读取相关片段",
	)
}

// GuideReadEmptyFile 目标文件为空。
func GuideReadEmptyFile(path string) string {
	return BuildGuide(
		fmt.Sprintf("尝试读取文件 %q，但该文件为空", path),
		"文件大小为 0，无内容可读取",
		"确认是否读取了正确的文件；若怀疑路径有误，用 Glob 或 Ls 核对后再读取",
	)
}

// GuideReadInvalidPath 设备文件/二进制等不支持读取的路径。
func GuideReadInvalidPath(path, cause string) string {
	return BuildGuide(
		fmt.Sprintf("尝试读取文件 %q，但该路径不被支持", path),
		cause,
		"确认目标是可读的普通文件（文本、文档或受支持的图片），修正路径后重新读取",
	)
}

// GuideFileError 对文件系统类错误进行分类引导。
// 依据 errors.Is 识别可预知的错误类别（权限不足 / 不存在 / 已存在 / 非法操作），
// 并为每个类别给出针对性的解决方案；无法分类的未知错误才走通用兜底。
//
// 参数：
//   - verb：我做了什么（动作描述，如 "读取"、"写入"、"创建目录"、"编辑"）
//   - path：涉及的路径
//   - err：原始错误（通过 errors.Is 分类识别）
func GuideFileError(verb, path string, err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return BuildGuide(
			fmt.Sprintf("尝试%s %q 时失败", verb, path),
			fmt.Sprintf("没有访问 %q 的权限（permission denied）——这是明确的权限问题", path),
			"先自查：该路径是否属于当前工作区或已获授权的范围？若确实需要访问，我可以使用 chmod 调整该路径的访问权限（谨慎操作），或询问用户：是由用户手动变更权限，还是由我来执行 chmod？",
		)
	case errors.Is(err, os.ErrNotExist):
		return BuildGuide(
			fmt.Sprintf("尝试%s %q 时失败", verb, path),
			fmt.Sprintf("目标路径 %q 不存在", path),
			"先自查：我传入的路径是否拼写正确、相对路径的基准目录是否正确？使用 Glob 或 Ls 定位正确的路径后重试",
		)
	case errors.Is(err, os.ErrExist):
		return BuildGuide(
			fmt.Sprintf("尝试%s %q 时失败", verb, path),
			fmt.Sprintf("目标 %q 已存在", path),
			"确认是否要覆盖或更新已存在的内容：若是，可直接操作；若否，应更换路径或先读取现有内容再决定",
		)
	case errors.Is(err, fs.ErrInvalid):
		return BuildGuide(
			fmt.Sprintf("尝试%s %q 时失败", verb, path),
			fmt.Sprintf("对 %q 执行了非法的文件操作（参数或路径形式无效）", path),
			"先自查：我传入的路径是否为空、格式是否正确、是否为已存在的目录或文件？修正为合法路径后重试",
		)
	default:
		return BuildGuide(
			fmt.Sprintf("尝试%s %q 时失败", verb, path),
			WithErrDetail("文件操作发生了未预知的系统级错误", err),
			"先自查：我传入的路径与参数是否正确？若确认无误仍失败，应停止无意义的重试，基于已有信息作答或询问用户",
		)
	}
}

// GuideReadIO 读取过程中的 I/O 错误（按可预知类别分类引导）。
// 权限不足 / 文件不存在 / 已存在 / 非法操作等可预知问题由 GuideFileError
// 分类识别并给出针对性方案；无法分类的未知错误才走通用兜底。
func GuideReadIO(path string, wrap error) string {
	return GuideFileError("读取", path, wrap)
}

// GuideReadOutputBudget 单次读取范围超过输出字符预算。
func GuideReadOutputBudget(startLine, endLine, outputChars, maxChars, totalLines int) string {
	return BuildGuide(
		fmt.Sprintf("尝试一次性读取第 %d-%d 行（约 %d 字符），超过单次读取预算 %d 字符", startLine, endLine, outputChars, maxChars),
		fmt.Sprintf("文件共 %d 行，大段读取会超出输出预算", totalLines),
		"使用 offset 与 limit 参数缩小读取范围后重试（例如 offset=1 limit=200），按需分多次读取",
	)
}

// errDetailSuffix 生成底层错误的截断补充文本（≤errDetailMaxRunes 字符），
// 供 Grant reason 末尾追加。err 为 nil 时返回空串。
func errDetailSuffix(err error) string {
	if err == nil {
		return ""
	}
	return "\n" + TruncateString(err.Error(), errDetailMaxRunes)
}

// guideOutsideWorkspace 构造越界操作的授权请求文案（Grant reason）的公共骨架。
// 所有涉及工作区边界的工具（Read/Ls/Edit/Write/RunScript）共用，保证授权语义一致：
// 根目录或工作区外的路径并非不可访问，而是必须先获得用户同意才能操作；
// 授权一次后（PermissionAllowSession），同一目录/文件路径不再重复授权。
func guideOutsideWorkspace(action string, err error) string {
	return fmt.Sprintf(
		"%s。这是越权操作，必须先获得你的同意才能执行（可用 PermissionAllow / PermissionAllowSession 授权，或用 PermissionDeny 拒绝）。%s",
		action, errDetailSuffix(err),
	)
}

// GuideReadOutsideWorkspace 构造越界读取的授权请求文案（Grant reason）。
func GuideReadOutsideWorkspace(filePath, resolved string, err error) string {
	return guideOutsideWorkspace(
		fmt.Sprintf("我需要读取位于工作区之外的文件 %q（解析为 %q）", filePath, resolved),
		err,
	)
}

// ── Ls 工具的可预知错误路径 ────────────────────────────────────────

// GuideLsDirNotFound 目标目录不存在（ENOENT）。
func GuideLsDirNotFound(path string) string {
	return BuildGuide(
		fmt.Sprintf("尝试列出目录 %q，但该目录不存在", path),
		"目标目录在文件系统中不存在（ENOENT），可能是路径拼写、大小写或相对路径基准有误",
		"检查路径拼写与大小写，从已知存在的父目录开始使用 Ls 逐层浏览，或改用 Glob 搜索目标",
	)
}

// GuideLsNotDirectory 目标是文件而不是目录。
func GuideLsNotDirectory(path string) string {
	return BuildGuide(
		fmt.Sprintf("尝试列出路径 %q，但它是文件而不是目录", path),
		"Ls 只能列出目录的内容",
		"若目标是文件，改用 Read 工具读取内容；若想按模式搜索文件，改用 Glob 工具",
	)
}

// GuideLsStatFailed 获取目录状态失败。
func GuideLsStatFailed(path string, wrap error) string {
	return BuildGuide(
		fmt.Sprintf("尝试获取目录 %q 的状态", path),
		WithErrDetail("无法读取目录状态，常见原因是目录不存在、权限不足或文件系统异常", wrap),
		"先自查：我传入的目录路径是否拼写正确、目录是否真实存在？若目录存在但无权限访问，应询问用户或改用其它可访问的目录",
	)
}

// GuideLsReadFailed 读取目录内容失败。
func GuideLsReadFailed(path string, wrap error) string {
	return BuildGuide(
		fmt.Sprintf("尝试读取目录 %q 的内容", path),
		WithErrDetail("无法读取目录内容，常见原因是目录已被删除/移动，或当前没有读取权限", wrap),
		"先自查：目录是否仍然存在、路径是否拼写正确？若目录存在但无权限读取，应询问用户或改用其它可访问的目录",
	)
}

// GuideLsOutsideWorkspace 构造越界列目录的授权请求文案（Grant reason）。
func GuideLsOutsideWorkspace(dirPath, resolved string, err error) string {
	return guideOutsideWorkspace(
		fmt.Sprintf("我需要列出位于工作区之外的目录 %q（解析为 %q）", dirPath, resolved),
		err,
	)
}

// ── 写 / 编辑 / 脚本的越界授权请求文案（Grant reason）────────────
// 与 Read / Ls 保持一致：第一人称说明越界操作 → 必须经用户同意 → 授权方式。

// GuideEditOutsideWorkspace 构造越界编辑的授权请求文案（Grant reason）。
func GuideEditOutsideWorkspace(filePath, resolved string, err error) string {
	return guideOutsideWorkspace(
		fmt.Sprintf("我需要编辑位于工作区之外的文件 %q（解析为 %q）", filePath, resolved),
		err,
	)
}

// GuideWriteOutsideWorkspace 构造越界写入的授权请求文案（Grant reason）。
func GuideWriteOutsideWorkspace(filePath, resolved string, err error) string {
	return guideOutsideWorkspace(
		fmt.Sprintf("我需要写入位于工作区之外的文件 %q（解析为 %q）", filePath, resolved),
		err,
	)
}

// ── 通用工具引导（全部工具共用）───────────────────────────────────
// 这些函数为任意工具的可预知错误路径提供统一的「我做了什么 → 原因 →
// 下一步我应该怎么做」引导，覆盖参数校验、运行上下文、目标不存在等
// 最常见的失败场景；超出预知范围的未知错误由执行器兜底
// （GuideToolFailure）。

// GuideToolFailure 兜底错误：工具执行失败且原因无法细分时使用。
//
// 该函数是默认兜底——只有超出预知范围的错误才会走到这里。兜底场景
// 无法预知解决方案，因此只陈述「发生了什么」与原因，刻意不包含
// 「下一步我应该」的指引：任何「请重试」式话术都会诱导（尤其是
// 小模型）陷入反复重试的死循环，原样陈述原因反而更安全。
func GuideToolFailure(toolName string, wrap error) string {
	return fmt.Sprintf("我执行工具 %s 时失败。原因：%s。", toolName, WithErrDetail("出现了未预知的错误", wrap))
}

// GuideMissingParam 调用工具时缺少必填参数。
func GuideMissingParam(toolName, key string) string {
	return BuildGuide(
		fmt.Sprintf("调用工具 %s 时缺少必需的参数 %q", toolName, key),
		"缺少必填参数，工具无法确定操作目标",
		fmt.Sprintf("仔细阅读 %s 工具的参数定义（参数名称、JSON 类型、是否必填），补充参数 %q 后重新调用", toolName, key),
	)
}

// GuideWrongParamType 调用工具时参数类型错误。
func GuideWrongParamType(toolName, key, expected string, got any) string {
	return BuildGuide(
		fmt.Sprintf("调用工具 %s 时参数 %q 的类型错误（期望 %s，实际为 %T）", toolName, key, expected, got),
		"参数类型与工具定义不符，工具无法解析该参数",
		fmt.Sprintf("按 %s 工具参数定义中的 JSON 类型，将参数 %q 修正为 %s 类型后重新调用", toolName, key, expected),
	)
}

// GuideMissingContext 工具运行所需上下文（Session / EventBus / Store 等）缺失。
func GuideMissingContext(toolName, component string) string {
	return BuildGuide(
		fmt.Sprintf("调用工具 %s 时缺少运行所需的 %s", toolName, component),
		"工具依赖的运行时上下文未注入，无法安全执行",
		"确认该工具在正确的运行时环境中被调用；若反复出现，说明系统配置存在问题，应告知用户",
	)
}

// GuideNotFound 目标（任务 / 团队 / 记录等）不存在。
func GuideNotFound(kind, id, next string) string {
	return BuildGuide(
		fmt.Sprintf("尝试获取%s %q，但未找到", kind, id),
		fmt.Sprintf("目标%s %q 在当前会话中不存在，可能是 ID 拼写错误、已被删除或从未创建", kind, id),
		next,
	)
}

// GuideInvalidValue 参数值不合法（格式/取值范围/枚举不匹配）。
func GuideInvalidValue(toolName, key string, got any, next string) string {
	return BuildGuide(
		fmt.Sprintf("调用工具 %s 时参数 %q 的值不合法（实际为 %v）", toolName, key, got),
		"参数值不满足工具的要求（格式、取值范围或枚举），工具无法执行",
		next,
	)
}
