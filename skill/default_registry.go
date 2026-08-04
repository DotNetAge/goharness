package skill

import (
	"fmt"
	"strings"
	"sync"
)

// DefaultSkillRegistry 基于关键词匹配实现 SkillRegistry 接口。
type DefaultSkillRegistry struct {
	mu     sync.RWMutex
	skills map[string]*Skill
}

// NewDefaultSkillRegistry 创建一个空的技能注册表。
func NewDefaultSkillRegistry() SkillRegistry {
	return &DefaultSkillRegistry{
		skills: make(map[string]*Skill),
	}
}

// NewSkillRegistryFromDirectory 通过从包含技能子目录（每个子目录含一个 SKILL.md 文件）
// 的目录中加载所有技能来创建 SkillRegistry。
// 不存在的目录视为空目录（不返回错误）。
func NewSkillRegistryFromDirectory(rootDir string) (SkillRegistry, error) {
	reg := NewDefaultSkillRegistry()
	loader := NewFileSystemSkillLoader(rootDir)
	skills, err := loader.Load()
	if err != nil {
		return nil, fmt.Errorf("从 %q 加载技能失败: %w", rootDir, err)
	}
	for _, s := range skills {
		if err := reg.RegisterSkill(s); err != nil {
			return nil, fmt.Errorf("注册技能 %q 失败: %w", s.Name, err)
		}
	}
	return reg, nil
}

// 编译期接口检查
var _ SkillRegistry = (*DefaultSkillRegistry)(nil)

// RegisterSkill 向注册表中添加或更新一个技能。
func (r *DefaultSkillRegistry) RegisterSkill(sk *Skill) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sk == nil || sk.Name == "" {
		return fmt.Errorf("技能名称不能为空")
	}
	r.skills[sk.Name] = sk
	return nil
}

// GetSkill 根据名称获取技能。
// 找到时返回该技能和 nil，未找到时返回 nil 和 ErrSkillNotFound。
func (r *DefaultSkillRegistry) GetSkill(name string) (*Skill, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sk, ok := r.skills[name]
	if !ok {
		return nil, ErrSkillNotFound
	}
	return sk, nil
}

// ListSkills 返回所有已注册的技能。返回副本以避免数据竞争。
func (r *DefaultSkillRegistry) ListSkills() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		result = append(result, s)
	}
	return result
}

// FindApplicableSkills 查找描述与给定意图上下文匹配的技能。
// 匹配方式是检查技能描述或名称中的任意关键词是否出现在意图的
// 类型、主题、摘要或实体文本中。
// context 接受 string 或 fmt.Stringer 类型；其他类型会被静默忽略。
func (r *DefaultSkillRegistry) FindApplicableSkills(context any) ([]*Skill, error) {
	intentText := ""
	switch v := context.(type) {
	case string:
		intentText = v
	case fmt.Stringer:
		intentText = v.String()
	default:
		return nil, nil
	}
	intentText = strings.ToLower(intentText)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var applicable []*Skill
	for _, skill := range r.skills {
		if matchSkill(skill, intentText) {
			applicable = append(applicable, skill)
		}
	}
	return applicable, nil
}

// matchSkill 使用加权评分算法检查技能是否与给定意图文本相关，
// 该算法可减少误匹配：
//   - 更长的关键词匹配得分更高（更具体）
//   - 要求最低总分为 2.0（例如两个 3 字符的词，或一个 6+ 字符的词）
//   - 技能名称的精确子串匹配提供强有力的加成
func matchSkill(skill *Skill, intentText string) bool {
	skillText := strings.ToLower(skill.Name + " " + skill.Description)
	skillName := strings.ToLower(skill.Name)

	intentKeywords := extractKeywords(intentText)
	if len(intentKeywords) == 0 {
		return false
	}

	var totalScore float64

	for _, word := range intentKeywords {
		wordLen := len(word)
		if wordLen < 3 || !strings.Contains(skillText, word) {
			continue
		}

		switch {
		case wordLen >= 7:
			totalScore += 2.5 // 非常具体的词
		case wordLen >= 5:
			totalScore += 1.5 // 中等具体的词
		default:
			totalScore += 1.0 // 常见词
		}
	}

	// 意图中包含技能名称的精确子串会给予较大加成
	if len(skillName) >= 4 && strings.Contains(intentText, skillName) {
		totalScore += 2.0
	}

	// 最低分数阈值：需要达到 2.0 分
	return totalScore >= 2.0
}

// extractKeywords 将文本拆分为小写单词，并过滤常见停用词。
func extractKeywords(text string) []string {
	words := strings.Fields(text)
	var keywords []string
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "must": true, "shall": true, "can": true,
		"to": true, "of": true, "in": true, "for": true, "on": true,
		"with": true, "at": true, "by": true, "from": true, "as": true,
		"into": true, "through": true, "during": true, "before": true,
		"after": true, "above": true, "below": true, "between": true,
		"out": true, "off": true, "over": true, "under": true, "again": true,
		"further": true, "then": true, "once": true, "here": true,
		"there": true, "when": true, "where": true, "why": true, "how": true,
		"all": true, "each": true, "every": true, "both": true, "few": true,
		"more": true, "most": true, "other": true, "some": true, "such": true,
		"no": true, "nor": true, "not": true, "only": true, "own": true,
		"same": true, "so": true, "than": true, "too": true, "very": true,
		"just": true, "because": true, "but": true, "and": true, "or": true,
		"if": true, "while": true, "that": true, "this": true, "it": true,
		"its": true, "use": true, "user": true, "you": true, "your": true,
	}
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if len(w) > 1 && !stopWords[w] {
			keywords = append(keywords, w)
		}
	}
	return keywords
}
