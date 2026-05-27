package goreact

import (
	"github.com/DotNetAge/goreact/memory"
	"github.com/DotNetAge/goreact/skill"
)

// Skill errors (aliased here for backward compatibility)
var (
	ErrSkillNotFound    = skill.ErrSkillNotFound
	ErrSkillExecution   = skill.ErrSkillExecution
	ErrSkillCompilation = skill.ErrSkillCompilation
)

// Memory errors (aliased here for backward compatibility)
var (
	ErrMemoryNotFound   = memory.ErrMemoryNotFound
	ErrMemoryStorage    = memory.ErrMemoryStorage
	ErrMemoryRetrieval  = memory.ErrMemoryRetrieval
)