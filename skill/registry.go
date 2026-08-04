package skill

// SkillRegistry 管理 Skill 定义。
// Skill 封装了可复用的领域知识和指令（例如“代码审查者”、
// “设计文档撰写者”）。它们遵循渐进式披露模式：
//
//	L1 (Name+Description) → L2 (SKILL.md body) → L3 (references/ resources).
//
// 这与 ToolRegistry 不同：Skill 引导 LLM 行为，工具负责执行。
type SkillRegistry interface {
	// RegisterSkill 向注册表中添加一个技能。
	RegisterSkill(skill *Skill) error

	// GetSkill 根据名称返回技能。
	GetSkill(name string) (*Skill, error)

	// ListSkills 返回所有已注册的技能（仅元数据，不含指令）。
	// 用于启动时的技能发现（每个技能约 100 tokens）。
	ListSkills() []*Skill

	// FindApplicableSkills 查找与给定上下文匹配的技能。
	// context 应为 reactor 包中的 *Intent。
	FindApplicableSkills(context any) ([]*Skill, error)
}
