package agents

import (
	"testing"

	"github.com/DotNetAge/goharness/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestActiveToolSetInitialCore 验证无子智能体时 core 工具正确。
func TestActiveToolSetInitialCore(t *testing.T) {
	reg := tools.NewDefaultToolRegistry()
	ats := NewActiveToolSet(reg, false)

	assert.True(t, ats.Has("ToolSelector"))
	assert.True(t, ats.Has("Skill"))
	assert.True(t, ats.Has("AskUser"))
	assert.False(t, ats.Has("SubAgent"))
	assert.False(t, ats.Has("CollectResults"))
}

// TestActiveToolSetInitialWithSubAgent 验证有子智能体时包含 conditional 工具。
func TestActiveToolSetInitialWithSubAgent(t *testing.T) {
	reg := tools.NewDefaultToolRegistry()
	ats := NewActiveToolSet(reg, true)

	assert.True(t, ats.Has("SubAgent"))
	assert.True(t, ats.Has("CollectResults"))
}

// TestActiveToolSetActivate 验证单个工具激活。
func TestActiveToolSetActivate(t *testing.T) {
	reg := tools.NewDefaultToolRegistry()
	require.NoError(t, reg.Register(newFakeTool("Read", nil)))

	ats := NewActiveToolSet(reg, false)
	activated := ats.Activate([]string{"Read"})
	assert.Equal(t, []string{"Read"}, activated)
	assert.True(t, ats.Has("Read"))
}

// TestActiveToolSetActivateUnknown 验证未知工具被静默跳过。
func TestActiveToolSetActivateUnknown(t *testing.T) {
	reg := tools.NewDefaultToolRegistry()
	ats := NewActiveToolSet(reg, false)
	activated := ats.Activate([]string{"NonExistent"})
	assert.Empty(t, activated)
}

// TestActiveToolSetActivateDedup 验证重复名称去重。
func TestActiveToolSetActivateDedup(t *testing.T) {
	reg := tools.NewDefaultToolRegistry()
	require.NoError(t, reg.Register(newFakeTool("Read", nil)))

	ats := NewActiveToolSet(reg, false)
	activated := ats.Activate([]string{"Read", "Read"})
	assert.Equal(t, []string{"Read"}, activated)
}

// TestActiveToolSetActivateGroup 验证工具组展开。
func TestActiveToolSetActivateGroup(t *testing.T) {
	reg := tools.NewDefaultToolRegistry()
	require.NoError(t, reg.Register(newFakeTool("TaskCreate", nil)))
	require.NoError(t, reg.Register(newFakeTool("TaskGet", nil)))
	require.NoError(t, reg.Register(newFakeTool("TaskList", nil)))
	require.NoError(t, reg.Register(newFakeTool("TaskUpdate", nil)))

	ats := NewActiveToolSet(reg, false)
	activated := ats.Activate([]string{"TaskCreate"})
	assert.Len(t, activated, 4)
	assert.True(t, ats.Has("TaskCreate"))
	assert.True(t, ats.Has("TaskGet"))
}

// TestActiveToolSetReset 验证 Reset 清除已激活工具。
func TestActiveToolSetReset(t *testing.T) {
	reg := tools.NewDefaultToolRegistry()
	require.NoError(t, reg.Register(newFakeTool("Read", nil)))

	ats := NewActiveToolSet(reg, false)
	ats.Activate([]string{"Read"})
	require.True(t, ats.Has("Read"))

	ats.Reset()
	assert.False(t, ats.Has("Read"))
	assert.True(t, ats.Has("ToolSelector"))
}

// TestActiveToolSetBuildDefinitions 验证 BuildDefinitions 包含所有活动工具。
func TestActiveToolSetBuildDefinitions(t *testing.T) {
	reg := tools.NewDefaultToolRegistry()
	require.NoError(t, reg.Register(newFakeTool("ToolSelector", nil)))
	require.NoError(t, reg.Register(newFakeTool("Skill", nil)))
	require.NoError(t, reg.Register(newFakeTool("AskUser", nil)))
	require.NoError(t, reg.Register(newFakeTool("Read", nil)))

	ats := NewActiveToolSet(reg, false)
	ats.Activate([]string{"Read"})
	defs := ats.BuildDefinitions()
	require.Len(t, defs, 4)
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	assert.Contains(t, names, "ToolSelector")
	assert.Contains(t, names, "Skill")
	assert.Contains(t, names, "AskUser")
	assert.Contains(t, names, "Read")
}
