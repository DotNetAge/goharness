package tools

// toolGroupMaps 定义工具组展开规则。
// 当工具名映射到非空列表时，选择该工具会自动激活组内所有工具。
//
// 组定义针对共享数据模型和生命周期的工具——
// 单独使用其中某个成员没有独立价值。
//
// 此映射仅限内部使用，对 LLM 不可见
// （工具目录保持扁平结构），不影响外部 API。
var toolGroupMaps = map[string][]string{
	// Task 组：基于共享 Task 数据模型的 CRUD 生命周期
	"TaskCreate": {"TaskCreate", "TaskGet", "TaskList", "TaskUpdate"},
	"TaskGet":    {"TaskCreate", "TaskGet", "TaskList", "TaskUpdate"},
	"TaskList":   {"TaskCreate", "TaskGet", "TaskList", "TaskUpdate"},
	"TaskUpdate": {"TaskCreate", "TaskGet", "TaskList", "TaskUpdate"},

	// Team 组：基于共享 Team 数据模型的生命周期
	"TeamCreate":    {"TeamCreate", "TeamDelete", "TeamList", "TeamGetTasks"},
	"TeamDelete":    {"TeamCreate", "TeamDelete", "TeamList", "TeamGetTasks"},
	"TeamList":      {"TeamCreate", "TeamDelete", "TeamList", "TeamGetTasks"},
	"TeamGetTasks":  {"TeamCreate", "TeamDelete", "TeamList", "TeamGetTasks"},

	// Skill 组：Skill 加载指令 + RootDir，RunScript 在该目录下执行脚本
	"Skill":     {"Skill", "RunScript"},
	"RunScript": {"Skill", "RunScript"},
}

// ExpandGroup 返回工具名对应的完整组。
// 若工具不属于任何组，返回仅包含该工具自身的切片。
func ExpandGroup(name string) []string {
	if group, ok := toolGroupMaps[name]; ok {
		return group
	}
	return []string{name}
}
