package reactor

import (
	"fmt"
	"strings"
	"sync"

	"github.com/DotNetAge/goreact/core"
)

// DefaultSkillRegistry implements core.SkillRegistry using keyword-based matching.
type DefaultSkillRegistry struct {
	mu     sync.RWMutex
	skills map[string]*core.Skill
}

// NewDefaultSkillRegistry creates a new empty skill registry.
func NewDefaultSkillRegistry() core.SkillRegistry {
	return &DefaultSkillRegistry{
		skills: make(map[string]*core.Skill),
	}
}

// Compile-time interface check
var _ core.SkillRegistry = (*DefaultSkillRegistry)(nil)

// RegisterSkill adds or updates a skill in the registry.
//
// Behavior:
//   - If the skill name does not exist, it is registered.
//   - If a skill with the same name already exists, it is **silently overwritten**.
//     This differs from ToolRegistry.Register() which returns an error on duplicates.
//     The overwrite behavior allows hot-reloading of skill definitions.
//
// Parameters:
//   - skill: The skill to register (must not be nil, name must not be empty).
//
// Returns an error if skill is nil or has an empty name.
func (r *DefaultSkillRegistry) RegisterSkill(skill *core.Skill) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if skill == nil || skill.Name == "" {
		return fmt.Errorf("skill name cannot be empty")
	}
	r.skills[skill.Name] = skill
	return nil
}

// GetSkill retrieves a skill by name.
// Returns the skill and nil if found, or nil and core.ErrSkillNotFound if not found.
func (r *DefaultSkillRegistry) GetSkill(name string) (*core.Skill, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	skill, ok := r.skills[name]
	if !ok {
		return nil, core.ErrSkillNotFound
	}
	return skill, nil
}

// ListSkills returns all registered skills. Returns a copy to avoid data races.
func (r *DefaultSkillRegistry) ListSkills() []*core.Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*core.Skill, 0, len(r.skills))
	for _, s := range r.skills {
		result = append(result, s)
	}
	return result
}

// FindApplicableSkills finds skills whose description matches the given intent context.
// The matching is done by checking if any keyword from the skill's description or name
// appears in the intent's type, topic, summary, or entity blob.
// Accepts a string or fmt.Stringer as the context; other types are silently ignored.
func (r *DefaultSkillRegistry) FindApplicableSkills(context any) ([]*core.Skill, error) {
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
	var applicable []*core.Skill
	for _, skill := range r.skills {
		if matchSkill(skill, intentText) {
			applicable = append(applicable, skill)
		}
	}
	return applicable, nil
}

// matchSkill checks if a skill is relevant to the given intent text using
// a weighted scoring algorithm that reduces false positives:
//   - Longer keyword matches score higher (more specific)
//   - Requires minimum total score of 2.0 (e.g., two 3-char words, or one 6+ char word)
//   - Exact skill-name substring match provides a strong bonus
func matchSkill(skill *core.Skill, intentText string) bool {
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
			totalScore += 2.5 // very specific term
		case wordLen >= 5:
			totalScore += 1.5 // moderately specific
		default:
			totalScore += 1.0 // common term
		}
	}

	// Exact skill name substring in intent gives a big bonus
	if len(skillName) >= 4 && strings.Contains(intentText, skillName) {
		totalScore += 2.0
	}

	// Minimum score threshold: 2.0 points required
	return totalScore >= 2.0
}

// extractKeywords splits text into lowercase words, filtering common stop words.
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
