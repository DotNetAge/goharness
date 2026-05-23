package tools

import (
	"context"
	"fmt"
	"testing"

	"github.com/DotNetAge/goreact/core"
)

func newTestKVStore(t *testing.T) (core.KVStore, func()) {
	tmpDir := t.TempDir()
	store, err := core.NewFileSystemKVStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create KVStore: %v", err)
	}
	return store, func() {}
}

func withKVStoreContext(ctx context.Context, kv core.KVStore, sessionID string) context.Context {
	toolCtx := &core.ToolContext{
		KVStore:   kv,
		SessionID: sessionID,
		EmitEvent: func(e core.ReactEvent) {},
	}
	return core.WithToolContext(ctx, toolCtx)
}

func TestTaskCreateTool_Execute(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	tool := NewTaskCreateTool()
	ctx := withKVStoreContext(context.Background(), kv, "test-session-1")

	params := map[string]any{
		"subject":     "Analyze data",
		"description": "Run data analysis on the customer dataset",
	}

	result, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("Execute() result is not map[string]any")
	}

	if _, ok := resultMap["task_id"]; !ok {
		t.Errorf("Execute() missing task_id")
	}
	if status, _ := resultMap["status"].(string); status != "pending" {
		t.Errorf("Execute() status = %q, want 'pending'", status)
	}
	if subj, _ := resultMap["subject"].(string); subj != "Analyze data" {
		t.Errorf("Execute() subject = %q, want 'Analyze data'", subj)
	}

	task, err := GetTask(ctx, "test-session-1", resultMap["task_id"].(string))
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if task.Status != TaskPending {
		t.Errorf("Task status = %v, want %v", task.Status, TaskPending)
	}
	if task.Subject != "Analyze data" {
		t.Errorf("Task subject = %q, want 'Analyze data'", task.Subject)
	}
}

func TestTaskListTool_Execute(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-2")

	task1 := &Task{
		ID:          "task-1",
		Subject:     "Task one",
		Description: "First task",
		Status:      TaskCompleted,
		Owner:       "agent-a",
	}
	task2 := &Task{
		ID:          "task-2",
		Subject:     "Task two",
		Description: "Second task",
		Status:      TaskPending,
		Owner:       "agent-b",
	}

	if err := CreateTask(ctx, "test-session-2", task1); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if err := CreateTask(ctx, "test-session-2", task2); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	tool := NewTaskListTool()
	result, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resultMap := result.(map[string]any)
	tasks := resultMap["tasks"].([]map[string]any)
	if len(tasks) != 2 {
		t.Errorf("List returned %d tasks, want 2", len(tasks))
	}
}

func TestTaskGetTool_Execute(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-3")

	task := &Task{
		ID:          "task-abc",
		Subject:     "Test task",
		Description: "A test task",
		Status:      TaskCompleted,
		Owner:       "agent-x",
	}
	if err := CreateTask(ctx, "test-session-3", task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	tool := NewTaskGetTool()
	params := map[string]any{"task_id": "task-abc"}

	result, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resultMap := result.(map[string]any)
	if resultMap["task_id"] != "task-abc" {
		t.Errorf("task_id = %v, want 'task-abc'", resultMap["task_id"])
	}
	if resultMap["status"] != "completed" {
		t.Errorf("status = %v, want 'completed'", resultMap["status"])
	}
	if resultMap["subject"] != "Test task" {
		t.Errorf("subject = %v, want 'Test task'", resultMap["subject"])
	}
}

func TestTaskUpdateTool_Execute(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-4")

	task := &Task{
		ID:          "task-update",
		Subject:     "Original subject",
		Description: "Original description",
		Status:      TaskPending,
	}
	if err := CreateTask(ctx, "test-session-4", task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	tool := NewTaskUpdateTool()

	// Test updating description and status
	params := map[string]any{
		"task_id":     "task-update",
		"description": "Updated description",
		"status":      "in_progress",
	}

	result, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resultMap := result.(map[string]any)
	if resultMap["success"] != true {
		t.Errorf("success = %v, want true", resultMap["success"])
	}

	task, err = GetTask(ctx, "test-session-4", "task-update")
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if task.Description != "Updated description" {
		t.Errorf("Description = %q, want 'Updated description'", task.Description)
	}
	if task.Status != TaskInProgress {
		t.Errorf("Status = %v, want %v", task.Status, TaskInProgress)
	}
}

func TestTaskStopTool_Execute(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-5")

	task := &Task{
		ID:          "task-stop",
		Subject:     "Stop me",
		Description: "Task to be stopped",
		Status:      TaskPending,
	}
	if err := CreateTask(ctx, "test-session-5", task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	tool := NewTaskStopTool()
	params := map[string]any{"task_id": "task-stop"}

	result, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resultMap := result.(map[string]any)
	if resultMap["success"] != true {
		t.Errorf("success = %v, want true", resultMap["success"])
	}

	task, err = GetTask(ctx, "test-session-5", "task-stop")
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if task != nil {
		t.Errorf("Task should be deleted after stop, but still exists")
	}
}

func TestTeamCreateTool_Execute(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	spawnFunc := func(ctx context.Context, agentName, task string) (string, error) {
		return "team result", nil
	}

	tool := NewTeamCreateTool(spawnFunc)
	ctx := withKVStoreContext(context.Background(), kv, "test-session-6")

	params := map[string]any{
		"team_name":   "data-team",
		"description": "Analyze customer data",
		"leader":      "coordinator",
		"members":     []any{"analyst-1", "analyst-2"},
	}

	result, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resultMap := result.(map[string]any)
	if resultMap["team_name"] != "data-team" {
		t.Errorf("team_name = %v, want 'data-team'", resultMap["team_name"])
	}
	if resultMap["leader"] != "coordinator" {
		t.Errorf("leader = %v, want 'coordinator'", resultMap["leader"])
	}
}

func TestTeamListTool_Execute(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-7")

	team1 := &Team{
		Name:    "team-alpha",
		Leader:  "leader-1",
		Members: []string{"member-1", "member-2"},
		Status:  "active",
	}
	team2 := &Team{
		Name:    "team-beta",
		Leader:  "leader-2",
		Members: []string{"member-3"},
		Status:  "active",
	}

	if err := CreateTeam(ctx, "test-session-7", team1); err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	if err := CreateTeam(ctx, "test-session-7", team2); err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}

	tool := NewTeamListTool()
	result, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resultMap := result.(map[string]any)
	teams := resultMap["teams"].([]map[string]any)
	if len(teams) != 2 {
		t.Errorf("List returned %d teams, want 2", len(teams))
	}
}

func TestTeamDeleteTool_Execute(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-8")

	team := &Team{
		Name:    "team-to-delete",
		Leader:  "leader",
		Members: []string{"member"},
		Status:  "active",
	}
	if err := CreateTeam(ctx, "test-session-8", team); err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}

	tool := NewTeamDeleteTool()
	params := map[string]any{"team_name": "team-to-delete"}

	result, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resultMap := result.(map[string]any)
	if resultMap["success"] != true {
		t.Errorf("success = %v, want true", resultMap["success"])
	}

	team, err = GetTeam(ctx, "test-session-8", "team-to-delete")
	if err != nil {
		t.Fatalf("GetTeam() error = %v", err)
	}
	if team != nil {
		t.Errorf("Team should be deleted, but still exists")
	}
}

func TestTaskSessionIsolation(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx1 := withKVStoreContext(context.Background(), kv, "session-a")
	ctx2 := withKVStoreContext(context.Background(), kv, "session-b")

	task := &Task{
		ID:          "shared-task",
		Subject:     "Isolation test",
		Description: "Testing session isolation",
		Status:      TaskPending,
	}

	if err := CreateTask(ctx1, "session-a", task); err != nil {
		t.Fatalf("CreateTask in session-a error = %v", err)
	}

	task, err := GetTask(ctx2, "session-b", "shared-task")
	if err != nil {
		t.Fatalf("GetTask in session-b error = %v", err)
	}
	if task != nil {
		t.Errorf("Task from session-a should not be visible in session-b")
	}
}

func TestConcurrentTaskOperations(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-concurrent")

	for i := 0; i < 10; i++ {
		task := &Task{
			ID:          fmt.Sprintf("task-concurrent-%d", i),
			Subject:     "Concurrent task",
			Description: "Testing concurrent operations",
			Status:      TaskPending,
		}
		if err := CreateTask(ctx, "test-session-concurrent", task); err != nil {
			t.Fatalf("CreateTask error = %v", err)
		}
	}

	taskIDs, err := ListTasks(ctx, "test-session-concurrent")
	if err != nil {
		t.Fatalf("ListTasks error = %v", err)
	}
	if len(taskIDs) != 10 {
		t.Errorf("Expected 10 tasks, got %d", len(taskIDs))
	}
}

func TestTaskGetNotFound(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-get")

	tool := NewTaskGetTool()
	params := map[string]any{"task_id": "nonexistent"}

	_, err := tool.Execute(ctx, params)
	if err == nil {
		t.Fatal("expected error when task not found")
	}
}

func TestTaskStopNotFound(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-stop")

	tool := NewTaskStopTool()
	params := map[string]any{"task_id": "nonexistent"}

	_, err := tool.Execute(ctx, params)
	if err == nil {
		t.Fatal("expected error when stopping nonexistent task")
	}
}

func TestTaskUpdateNotFound(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-update")

	tool := NewTaskUpdateTool()
	params := map[string]any{
		"task_id":     "nonexistent",
		"description": "new desc",
	}

	_, err := tool.Execute(ctx, params)
	if err == nil {
		t.Fatal("expected error when updating nonexistent task")
	}
}

func TestTaskCreateDuplicateIDs(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	tool := NewTaskCreateTool()
	ctx := withKVStoreContext(context.Background(), kv, "test-session-dup")

	params := map[string]any{
		"subject":     "Task A",
		"description": "First task",
	}

	result1, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	taskID1 := result1.(map[string]any)["task_id"].(string)

	params["subject"] = "Task B"
	result2, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatalf("second create failed: %v", err)
	}
	taskID2 := result2.(map[string]any)["task_id"].(string)

	if taskID1 == taskID2 {
		t.Error("expected unique task IDs")
	}

	taskIDs, err := ListTasks(ctx, "test-session-dup")
	if err != nil {
		t.Fatalf("ListTasks error = %v", err)
	}
	if len(taskIDs) != 2 {
		t.Errorf("Expected 2 tasks, got %d", len(taskIDs))
	}
}

func TestTaskCreateEmptySubject(t *testing.T) {
	tool := NewTaskCreateTool()
	ctx := withKVStoreContext(context.Background(), nil, "test-session-empty")

	params := map[string]any{
		"subject":     "",
		"description": "Some description",
	}

	_, err := tool.Execute(ctx, params)
	if err == nil {
		t.Fatal("expected error when subject is empty")
	}
}

func TestTaskUpdate_StatusTransition(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-status")

	task := &Task{
		ID:          "task-status",
		Subject:     "Status test",
		Description: "Test status transitions",
		Status:      TaskPending,
	}
	if err := CreateTask(ctx, "test-session-status", task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	tool := NewTaskUpdateTool()

	// pending → in_progress (valid)
	_, err := tool.Execute(ctx, map[string]any{
		"task_id": "task-status",
		"status":  "in_progress",
	})
	if err != nil {
		t.Fatalf("pending → in_progress should be valid: %v", err)
	}

	// in_progress → completed (valid)
	_, err = tool.Execute(ctx, map[string]any{
		"task_id": "task-status",
		"status":  "completed",
	})
	if err != nil {
		t.Fatalf("in_progress → completed should be valid: %v", err)
	}

	// completed → in_progress (invalid)
	_, err = tool.Execute(ctx, map[string]any{
		"task_id": "task-status",
		"status":  "in_progress",
	})
	if err == nil {
		t.Fatal("completed → in_progress should be invalid")
	}
}

func TestTaskUpdate_Blocks(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-blocks")

	taskA := &Task{ID: "task-a", Subject: "Task A", Status: TaskPending}
	taskB := &Task{ID: "task-b", Subject: "Task B", Status: TaskPending}
	CreateTask(ctx, "test-session-blocks", taskA)
	CreateTask(ctx, "test-session-blocks", taskB)

	tool := NewTaskUpdateTool()

	// A blocks B → B.blockedBy += A, A.blocks += B
	_, err := tool.Execute(ctx, map[string]any{
		"task_id":   "task-a",
		"addBlocks": []any{"task-b"},
	})
	if err != nil {
		t.Fatalf("addBlocks error = %v", err)
	}

	updatedA, _ := GetTask(ctx, "test-session-blocks", "task-a")
	if len(updatedA.Blocks) != 1 || updatedA.Blocks[0] != "task-b" {
		t.Errorf("Task A blocks = %v, want ['task-b']", updatedA.Blocks)
	}

	updatedB, _ := GetTask(ctx, "test-session-blocks", "task-b")
	if len(updatedB.BlockedBy) != 1 || updatedB.BlockedBy[0] != "task-a" {
		t.Errorf("Task B blockedBy = %v, want ['task-a']", updatedB.BlockedBy)
	}
}

func TestTaskUpdate_Deleted(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-delete")

	task := &Task{ID: "task-del", Subject: "Delete me", Status: TaskPending}
	CreateTask(ctx, "test-session-delete", task)

	tool := NewTaskUpdateTool()
	_, err := tool.Execute(ctx, map[string]any{
		"task_id": "task-del",
		"status":  "deleted",
	})
	if err != nil {
		t.Fatalf("delete error = %v", err)
	}

	got, _ := GetTask(ctx, "test-session-delete", "task-del")
	if got != nil {
		t.Error("Task should be deleted")
	}
}

func TestTaskUpdate_Owner(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx := withKVStoreContext(context.Background(), kv, "test-session-owner")

	task := &Task{ID: "task-owner", Subject: "Owner test", Status: TaskPending}
	CreateTask(ctx, "test-session-owner", task)

	tool := NewTaskUpdateTool()
	_, err := tool.Execute(ctx, map[string]any{
		"task_id": "task-owner",
		"owner":   "agent-x",
	})
	if err != nil {
		t.Fatalf("set owner error = %v", err)
	}

	updated, _ := GetTask(ctx, "test-session-owner", "task-owner")
	if updated.Owner != "agent-x" {
		t.Errorf("Owner = %q, want 'agent-x'", updated.Owner)
	}
}

func TestTaskCreateEmptyDescription(t *testing.T) {
	tool := NewTaskCreateTool()
	ctx := withKVStoreContext(context.Background(), nil, "test-session-empty")

	params := map[string]any{
		"subject":     "Test subject",
		"description": "",
	}

	_, err := tool.Execute(ctx, params)
	if err == nil {
		t.Fatal("expected error when description is empty")
	}
}
