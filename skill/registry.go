package skill

// SkillRegistry manages Skill definitions.
// Skills encapsulate reusable domain knowledge and instructions (e.g., "code reviewer",
// "design doc writer"). They follow a progressive disclosure pattern:
//
//	L1 (Name+Description) → L2 (SKILL.md body) → L3 (references/ resources).
//
// This is distinct from ToolRegistry: Skills guide LLM behavior, tools enable execution.
type SkillRegistry interface {
	// RegisterSkill adds a skill to the registry.
	RegisterSkill(skill *Skill) error

	// GetSkill returns a skill by name.
	GetSkill(name string) (*Skill, error)

	// ListSkills returns all registered skills (metadata only, without instructions).
	// This is used for skill discovery at startup (~100 tokens per skill).
	ListSkills() []*Skill

	// FindApplicableSkills finds skills matching the given context.
	// The context should be an *Intent from the reactor package.
	FindApplicableSkills(context any) ([]*Skill, error)
}
