// Package goharness 是 AI 代理框架的主包。
// 提供从子包别名而来的统一错误定义，用于向后兼容。
package goharness

import (
	"github.com/DotNetAge/goharness/memory"
	"github.com/DotNetAge/goharness/skill"
)

// 技能错误（此处别名用于向后兼容）

// ErrSkillNotFound 当请求的技能在注册表中不存在时返回。
var ErrSkillNotFound = skill.ErrSkillNotFound

// ErrSkillExecution 当技能执行失败时返回。
var ErrSkillExecution = skill.ErrSkillExecution

// ErrSkillCompilation 当技能代码编译失败时返回。
var ErrSkillCompilation = skill.ErrSkillCompilation

// 记忆错误（此处别名用于向后兼容）

// ErrMemoryNotFound 当请求的记忆记录不存在时返回。
var ErrMemoryNotFound = memory.ErrMemoryNotFound

// ErrMemoryStorage 当记忆存储操作失败时返回。
var ErrMemoryStorage = memory.ErrMemoryStorage

// ErrMemoryRetrieval 当记忆检索操作失败时返回。
var ErrMemoryRetrieval = memory.ErrMemoryRetrieval
