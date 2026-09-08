package skill

// SkillRegistry 定义运行时技能检索契约。
//
// SPI 收窄（PR-PROMPTS P4）：goharness 只保留检索契约——应用侧（mindx）
// 的技能存储负责技能的发现/加载/注册，Skill 工具按名称检索（GetSkill）。
// 这与 ToolRegistry 不同：Skill 引导 LLM 行为，工具负责执行。
type SkillRegistry interface {
	// GetSkill 根据名称返回技能。
	// 找到时返回该技能和 nil，未找到时返回 nil 和 ErrSkillNotFound。
	GetSkill(name string) (*Skill, error)
}
