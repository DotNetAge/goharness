// Package goharness is the main package for the AI agent framework.
// It provides unified error definitions aliased from sub-packages for backward compatibility.
package goharness

import (
	"github.com/DotNetAge/goharness/memory"
	"github.com/DotNetAge/goharness/skill"
)

// Skill errors (aliased here for backward compatibility)

// ErrSkillNotFound is returned when a requested skill doesn't exist in the registry.
var ErrSkillNotFound = skill.ErrSkillNotFound

// ErrSkillExecution is returned when a skill execution fails.
var ErrSkillExecution = skill.ErrSkillExecution

// ErrSkillCompilation is returned when skill code compilation fails.
var ErrSkillCompilation = skill.ErrSkillCompilation

// Memory errors (aliased here for backward compatibility)

// ErrMemoryNotFound is returned when a requested memory record doesn't exist.
var ErrMemoryNotFound = memory.ErrMemoryNotFound

// ErrMemoryStorage is returned when a memory storage operation fails.
var ErrMemoryStorage = memory.ErrMemoryStorage

// ErrMemoryRetrieval is returned when a memory retrieval operation fails.
var ErrMemoryRetrieval = memory.ErrMemoryRetrieval
