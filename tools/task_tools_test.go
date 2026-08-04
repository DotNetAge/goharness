package tools

import (
	"context"
	"fmt"
	"testing"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/store"
)

func newTestKVStore(t *testing.T) (store.KVStore, func()) {
	tmpDir := t.TempDir()
	store, err := store.NewFileSystemKVStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create KVStore: %v", err)
	}
	return store, func() {}
}

func withKVStoreContext(ctx context.Context, kv store.KVStore, _ string) (context.Context, string) {
	store := newMockSessionStore()
	sess, err := session.New("test-agent", "", "/tmp/test", store, logging.NewNopLogger())
	if err != nil {
		panic(err)
	}
	toolCtx := &ToolContext{
		KVStore:   kv,
		Session:   sess,
		EmitEvent: func(e events.ReactEvent) {},
	}
	return WithToolContext(ctx, toolCtx), sess.ID()
}

func TestTaskCreateTool_Execute(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	tool := NewTaskCreateTool()
	ctx, _ := withKVStoreContext(context.Background(), kv, "test-session-1")

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

	// 使用实际的 session ID 而不是硬编码值
	tc := GetToolContext(ctx)
	task, err := GetTask(ctx, tc.Session.ID(), resultMap["task_id"].(string))
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if task == nil {
		t.Fatal("GetTask() returned nil task")
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

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-2")

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

	if err := CreateTask(ctx, sessionID, task1); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if err := CreateTask(ctx, sessionID, task2); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	tool := NewTaskListTool()
	result, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resultMap := result.(map[string]any)
	tasksRaw := resultMap["tasks"]
	taskSlice, ok := tasksRaw.([]map[string]any)
	if !ok {
		taskSlice2 := tasksRaw.([]any)
		taskSlice = make([]map[string]any, len(taskSlice2))
		for i, v := range taskSlice2 {
			taskSlice[i] = v.(map[string]any)
		}
	}
	if len(taskSlice) != 2 {
		t.Errorf("List returned %d tasks, want 2", len(taskSlice))
	}
}

func TestTaskGetTool_Execute(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-3")

	task := &Task{
		ID:          "task-abc",
		Subject:     "Test task",
		Description: "A test task",
		Status:      TaskCompleted,
		Owner:       "agent-x",
	}
	if err := CreateTask(ctx, sessionID, task); err != nil {
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

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-4")

	task := &Task{
		ID:          "task-update",
		Subject:     "Original subject",
		Description: "Original description",
		Status:      TaskPending,
	}
	if err := CreateTask(ctx, sessionID, task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	tool := NewTaskUpdateTool()

	// 测试更新描述和状态
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

	task, err = GetTask(ctx, sessionID, "task-update")
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

func TestTeamCreateTool_Execute(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	spawnFunc := func(ctx context.Context, agentName, task string) (string, string, error) {
		return "team result", "team-session-id", nil
	}

	tool := NewTeamCreateTool(spawnFunc)
	ctx, _ := withKVStoreContext(context.Background(), kv, "test-session-6")

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

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-7")

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

	if err := CreateTeam(ctx, sessionID, team1); err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	if err := CreateTeam(ctx, sessionID, team2); err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}

	tool := NewTeamListTool()
	result, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resultMap := result.(map[string]any)
	teamsRaw := resultMap["teams"]
	teamSlice, ok := teamsRaw.([]map[string]any)
	if !ok {
		teamSlice2 := teamsRaw.([]any)
		teamSlice = make([]map[string]any, len(teamSlice2))
		for i, v := range teamSlice2 {
			teamSlice[i] = v.(map[string]any)
		}
	}
	if len(teamSlice) != 2 {
		t.Errorf("List returned %d teams, want 2", len(teamSlice))
	}
}

func TestTeamDeleteTool_Execute(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-8")

	team := &Team{
		Name:    "team-to-delete",
		Leader:  "leader",
		Members: []string{"member"},
		Status:  "active",
	}
	if err := CreateTeam(ctx, sessionID, team); err != nil {
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

	team, err = GetTeam(ctx, sessionID, "team-to-delete")
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

	ctx1, sessionID1 := withKVStoreContext(context.Background(), kv, "session-a")
	ctx2, sessionID2 := withKVStoreContext(context.Background(), kv, "session-b")

	task := &Task{
		ID:          "shared-task",
		Subject:     "Isolation test",
		Description: "Testing session isolation",
		Status:      TaskPending,
	}

	if err := CreateTask(ctx1, sessionID1, task); err != nil {
		t.Fatalf("CreateTask in session-a error = %v", err)
	}

	task, err := GetTask(ctx2, sessionID2, "shared-task")
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

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-concurrent")

	for i := 0; i < 10; i++ {
		task := &Task{
			ID:          fmt.Sprintf("task-concurrent-%d", i),
			Subject:     "Concurrent task",
			Description: "Testing concurrent operations",
			Status:      TaskPending,
		}
		if err := CreateTask(ctx, sessionID, task); err != nil {
			t.Fatalf("CreateTask error = %v", err)
		}
	}

	taskIDs, err := ListTasks(ctx, sessionID)
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

	ctx, _ := withKVStoreContext(context.Background(), kv, "test-session-get")

	tool := NewTaskGetTool()
	params := map[string]any{"task_id": "nonexistent"}

	_, err := tool.Execute(ctx, params)
	if err == nil {
		t.Fatal("expected error when task not found")
	}
}

func TestTaskUpdateNotFound(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx, _ := withKVStoreContext(context.Background(), kv, "test-session-update")

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
	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-dup")

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

	ids, err := ListTasks(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListTasks error = %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("Expected 2 tasks, got %d", len(ids))
	}
}

func TestTaskCreateEmptySubject(t *testing.T) {
	tool := NewTaskCreateTool()
	ctx, _ := withKVStoreContext(context.Background(), nil, "test-session-empty")

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

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-status")

	task := &Task{
		ID:          "task-status",
		Subject:     "Status test",
		Description: "Test status transitions",
		Status:      TaskPending,
	}
	if err := CreateTask(ctx, sessionID, task); err != nil {
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

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-blocks")

	taskA := &Task{ID: "task-a", Subject: "Task A", Status: TaskPending}
	taskB := &Task{ID: "task-b", Subject: "Task B", Status: TaskPending}
	CreateTask(ctx, sessionID, taskA)
	CreateTask(ctx, sessionID, taskB)

	tool := NewTaskUpdateTool()

	// A blocks B → B.blockedBy += A, A.blocks += B
	_, err := tool.Execute(ctx, map[string]any{
		"task_id":   "task-a",
		"addBlocks": []any{"task-b"},
	})
	if err != nil {
		t.Fatalf("addBlocks error = %v", err)
	}

	updatedA, _ := GetTask(ctx, sessionID, "task-a")
	if len(updatedA.Blocks) != 1 || updatedA.Blocks[0] != "task-b" {
		t.Errorf("Task A blocks = %v, want ['task-b']", updatedA.Blocks)
	}

	updatedB, _ := GetTask(ctx, sessionID, "task-b")
	if len(updatedB.BlockedBy) != 1 || updatedB.BlockedBy[0] != "task-a" {
		t.Errorf("Task B blockedBy = %v, want ['task-a']", updatedB.BlockedBy)
	}
}

func TestTaskUpdate_Cancelled(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-cancel")

	task := &Task{ID: "task-cancel", Subject: "Cancel me", Status: TaskPending}
	CreateTask(ctx, sessionID, task)

	tool := NewTaskUpdateTool()
	_, err := tool.Execute(ctx, map[string]any{
		"task_id": "task-cancel",
		"status":  "cancelled",
	})
	if err != nil {
		t.Fatalf("cancel error = %v", err)
	}

	got, _ := GetTask(ctx, sessionID, "task-cancel")
	if got == nil {
		t.Fatal("Task should still exist after cancellation")
	}
	if got.Status != TaskCancelled {
		t.Errorf("Task status = %v, want %v", got.Status, TaskCancelled)
	}
}

func TestTaskUpdate_CycleDetection(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-cycle")

	taskA := &Task{ID: "cycle-a", Subject: "A", Status: TaskPending}
	taskB := &Task{ID: "cycle-b", Subject: "B", Status: TaskPending}
	CreateTask(ctx, sessionID, taskA)
	CreateTask(ctx, sessionID, taskB)

	tool := NewTaskUpdateTool()

	// A blocks B (valid)
	_, err := tool.Execute(ctx, map[string]any{
		"task_id":   "cycle-a",
		"addBlocks": []any{"cycle-b"},
	})
	if err != nil {
		t.Fatalf("A→B should be valid: %v", err)
	}

	// B blocks A (would create cycle B→A→B, rejected)
	_, err = tool.Execute(ctx, map[string]any{
		"task_id":   "cycle-b",
		"addBlocks": []any{"cycle-a"},
	})
	if err == nil {
		t.Fatal("B→A should be rejected as circular dependency")
	}
	t.Logf("Cycle detection correctly rejected: %v", err)
}

func TestTaskUpdate_CycleDetection_BlockedBy(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-cycle-bb")

	taskA := &Task{ID: "bb-a", Subject: "A", Status: TaskPending}
	taskB := &Task{ID: "bb-b", Subject: "B", Status: TaskPending}
	CreateTask(ctx, sessionID, taskA)
	CreateTask(ctx, sessionID, taskB)

	tool := NewTaskUpdateTool()

	// A blockedBy B (B blocks A, valid)
	_, err := tool.Execute(ctx, map[string]any{
		"task_id":      "bb-a",
		"addBlockedBy": []any{"bb-b"},
	})
	if err != nil {
		t.Fatalf("A blockedBy B should be valid: %v", err)
	}

	// B blockedBy A (would create cycle A→B→A via blockedBy, rejected)
	_, err = tool.Execute(ctx, map[string]any{
		"task_id":      "bb-b",
		"addBlockedBy": []any{"bb-a"},
	})
	if err == nil {
		t.Fatal("B blockedBy A should be rejected as circular dependency")
	}
	t.Logf("BlockedBy cycle detection correctly rejected: %v", err)
}

func TestTaskUpdate_CycleDetection_Transitive(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-cycle-tran")

	taskA := &Task{ID: "tran-a", Subject: "A", Status: TaskPending}
	taskB := &Task{ID: "tran-b", Subject: "B", Status: TaskPending}
	taskC := &Task{ID: "tran-c", Subject: "C", Status: TaskPending}
	CreateTask(ctx, sessionID, taskA)
	CreateTask(ctx, sessionID, taskB)
	CreateTask(ctx, sessionID, taskC)

	tool := NewTaskUpdateTool()

	// A blocks B, B blocks C (chain: A→B→C)
	mustUpdate(t, tool, ctx, "tran-a", map[string]any{"addBlocks": []any{"tran-b"}})
	mustUpdate(t, tool, ctx, "tran-b", map[string]any{"addBlocks": []any{"tran-c"}})

	// C blocks A (would create C→A→B→C, rejected)
	_, err := tool.Execute(ctx, map[string]any{
		"task_id":   "tran-c",
		"addBlocks": []any{"tran-a"},
	})
	if err == nil {
		t.Fatal("C→A should be rejected as transitive circular dependency")
	}
	t.Logf("Transitive cycle detection correctly rejected: %v", err)
}

func mustUpdate(t *testing.T, tool *TaskUpdateTool, ctx context.Context, taskID string, params map[string]any) {
	t.Helper()
	params["task_id"] = taskID
	_, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatalf("update %s failed: %v", taskID, err)
	}
}

func TestTaskUpdate_Owner(t *testing.T) {
	kv, cleanup := newTestKVStore(t)
	defer cleanup()

	ctx, sessionID := withKVStoreContext(context.Background(), kv, "test-session-owner")

	task := &Task{ID: "task-owner", Subject: "Owner test", Status: TaskPending}
	CreateTask(ctx, sessionID, task)

	tool := NewTaskUpdateTool()
	_, err := tool.Execute(ctx, map[string]any{
		"task_id": "task-owner",
		"owner":   "agent-x",
	})
	if err != nil {
		t.Fatalf("set owner error = %v", err)
	}

	updated, _ := GetTask(ctx, sessionID, "task-owner")
	if updated.Owner != "agent-x" {
		t.Errorf("Owner = %q, want 'agent-x'", updated.Owner)
	}
}

func TestTaskCreateEmptyDescription(t *testing.T) {
	tool := NewTaskCreateTool()
	ctx, _ := withKVStoreContext(context.Background(), nil, "test-session-empty")

	params := map[string]any{
		"subject":     "Test subject",
		"description": "",
	}

	_, err := tool.Execute(ctx, params)
	if err == nil {
		t.Fatal("expected error when description is empty")
	}
}
