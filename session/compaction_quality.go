package session

import (
	"regexp"
	"sort"
	"strings"
)

// CompactionQuality 描述一次压缩摘要对原文「关键锚点」的保留率。
//
// 为什么需要它：压缩由 LLM 生成摘要，除「非空」之外没有任何质量约束。若摘要
// 漏掉关键文件路径、条目编号或标识符，会话就会静默失忆——agent 不会报错，
// 只会基于残缺记忆继续工作，产出看着合理却已偏离，而且事后极难发现。
// 这个指标把「有没有失忆」从不可观测变成可观测的数字。
type CompactionQuality struct {
	// TotalAnchors 是原文中识别出的关键锚点总数（去重后）。
	TotalAnchors int `json:"total_anchors"`
	// KeptAnchors 是在摘要中仍能找到的锚点数。
	KeptAnchors int `json:"kept_anchors"`
	// Coverage 是保留率（0~1）。无锚点时记为 1。
	Coverage float64 `json:"coverage"`
	// MissingSamples 是未被保留的锚点样例（最多 5 个，便于人工定位）。
	MissingSamples []string `json:"missing_samples,omitempty"`
}

// CompactionMinCoverage 是告警下限：摘要保留率低于该值时记警告。
//
// 不强制阻止压缩——摘要本就有损，阈值定太死会误伤正常压缩，让会话再也压不动。
// 低于此值说明大概率丢了关键信息，值得人工复核一次。
const CompactionMinCoverage = 0.5

// maxAnchorSamples 控制告警输出里保留几个缺失样例。
const maxAnchorSamples = 5

var (
	// 绝对路径：至少两级，如 /Users/ray/workspaces/ai-ecosystem/x.go
	anchorPathRe = regexp.MustCompile(`(?:/[\w.@+\-]{1,64}){2,}`)
	// 条目编号：如 E1-04、C-03、F1 这类在事实库里承载结论的结构化编号
	anchorCodeRe = regexp.MustCompile(`\b[A-Z]{1,3}\d{0,2}-\d{1,3}\b`)
	// 反引号包裹的标识符：如 `dedupCacheKey`、`session.yml`
	anchorTickRe = regexp.MustCompile("`([^`\\n]{3,60})`")
)

// extractAnchors 从文本中提取关键锚点集合。
//
// 选用这三类锚点，是因为它们在正文里不可被「概括」掉：
// 路径丢失会导致无法复现，编号丢失会导致结论无法溯源，标识符丢失会导致
// 操作对象变成模糊描述。普通叙述性文字不参与统计——摘要本就该概括它们。
func extractAnchors(text string) map[string]struct{} {
	const maxPerKind = 200
	out := make(map[string]struct{}, 64)

	for _, m := range anchorPathRe.FindAllString(text, maxPerKind) {
		out[m] = struct{}{}
	}
	for _, m := range anchorCodeRe.FindAllString(text, maxPerKind) {
		out[m] = struct{}{}
	}
	for _, m := range anchorTickRe.FindAllStringSubmatch(text, maxPerKind) {
		if len(m) > 1 {
			out[m[1]] = struct{}{}
		}
	}
	return out
}

// EvaluateCompactionQuality 对比原文与摘要在关键锚点上的保留情况。
//
// 只做「字符串是否仍然出现」的判定，不判断语义是否被曲解——后者需要 LLM
// 复核，成本远超收益。宁可给出一个便宜的粗指标，也不做看起来精确的假校验。
func EvaluateCompactionQuality(messages []Message, summary string) CompactionQuality {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(m.Content)
		b.WriteByte('\n')
		for _, tc := range m.ToolCalls {
			b.WriteString(tc.Arguments)
			b.WriteByte('\n')
		}
	}

	anchors := extractAnchors(b.String())
	q := CompactionQuality{TotalAnchors: len(anchors)}
	if len(anchors) == 0 {
		// 纯叙述性窗口没有可校验锚点，视为无损失，避免误报。
		q.Coverage = 1
		return q
	}

	missing := make([]string, 0, maxAnchorSamples)
	for a := range anchors {
		if strings.Contains(summary, a) {
			q.KeptAnchors++
			continue
		}
		if len(missing) < maxAnchorSamples {
			missing = append(missing, a)
		}
	}

	sort.Strings(missing)
	q.MissingSamples = missing
	q.Coverage = float64(q.KeptAnchors) / float64(q.TotalAnchors)
	return q
}
