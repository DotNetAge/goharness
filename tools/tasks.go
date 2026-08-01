package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/store"
)

type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskInProgress TaskStatus = "in_progress"
	TaskCompleted  TaskStatus = "completed"
	TaskCancelled  TaskStatus = "cancelled"
)

type Task struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  string         `json:"active_form,omitempty"`
	Status      TaskStatus     `json:"status"`
	Owner       string         `json:"owner,omitempty"`
	Blocks      []string       `json:"blocks,omitempty"`
	BlockedBy   []string       `json:"blocked_by,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

type Team struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Leader      string    `json:"leader"`
	Members     []string  `json:"members"`
	TaskIDs     []string  `json:"task_ids"`
	CreatedAt   time.Time `json:"created_at"`
	Status      string    `json:"status"`
}

var validTransitions = map[TaskStatus][]TaskStatus{
	TaskPending:    {TaskInProgress, TaskCompleted, TaskCancelled},
	TaskInProgress: {TaskCompleted, TaskCancelled},
	TaskCompleted:  {},
	TaskCancelled:  {},
}

func ValidTaskTransition(current, next TaskStatus) bool {
	allowed, ok := validTransitions[current]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == next {
			return true
		}
	}
	return false
}

const (
	taskKeyPrefix = "tasks_"
	teamKeyPrefix = "teams_"
)

func taskKey(sessionID, taskID string) string {
	return fmt.Sprintf("%s%s_%s", taskKeyPrefix, sessionID, taskID)
}

func teamKey(sessionID, teamName string) string {
	return fmt.Sprintf("%s%s_%s", teamKeyPrefix, sessionID, teamName)
}

func CreateTask(ctx context.Context, sessionID string, task *Task) error {
	kv := getKVStore(ctx)
	if kv == nil {
		return fmt.Errorf("%s", BuildGuide("创建任务时尝试访问 KVStore 存储", "KVStore 未注入到运行时上下文（ToolContext.KVStore 为 nil），无法持久化任务", "确认工具在正确的运行时环境中被调用；若反复出现，说明系统配置存在问题，应告知用户"))
	}

	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now()
	}
	if task.Status == "" {
		task.Status = TaskPending
	}

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("%s: %w", BuildGuide("创建任务时序列化任务数据失败", WithErrDetail("任务数据（尤其是 metadata 等字段）包含无法序列化的内容", err), "检查传入任务的 metadata 等字段，移除无法序列化的值（如函数、循环引用）后重试"), err)
	}

	key := taskKey(sessionID, task.ID)
	return kv.Set(ctx, sessionID, key, data, 0)
}

func GetTask(ctx context.Context, sessionID, taskID string) (*Task, error) {
	kv := getKVStore(ctx)
	if kv == nil {
		return nil, fmt.Errorf("%s", BuildGuide("获取任务时尝试访问 KVStore 存储", "KVStore 未注入到运行时上下文（ToolContext.KVStore 为 nil），无法读取任务", "确认工具在正确的运行时环境中被调用；若反复出现，说明系统配置存在问题，应告知用户"))
	}

	data, err := kv.Get(ctx, sessionID, taskKey(sessionID, taskID))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}

	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, fmt.Errorf("%s: %w", BuildGuide("获取任务时反序列化任务数据失败", WithErrDetail("存储中的任务数据已损坏或格式不兼容", err), "任务数据可能已损坏或不兼容，可尝试删除后重新创建该任务"), err)
	}
	return &task, nil
}

func UpdateTask(ctx context.Context, sessionID string, task *Task) error {
	kv := getKVStore(ctx)
	if kv == nil {
		return fmt.Errorf("%s", BuildGuide("更新任务时尝试访问 KVStore 存储", "KVStore 未注入到运行时上下文（ToolContext.KVStore 为 nil），无法持久化任务", "确认工具在正确的运行时环境中被调用；若反复出现，说明系统配置存在问题，应告知用户"))
	}

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("%s: %w", BuildGuide("更新任务时序列化任务数据失败", WithErrDetail("任务数据（尤其是 metadata 等字段）包含无法序列化的内容", err), "检查传入任务的 metadata 等字段，移除无法序列化的值（如函数、循环引用）后重试"), err)
	}

	return kv.Set(ctx, sessionID, taskKey(sessionID, task.ID), data, 0)
}

func DeleteTask(ctx context.Context, sessionID, taskID string) error {
	kv := getKVStore(ctx)
	if kv == nil {
		return fmt.Errorf("%s", BuildGuide("删除任务时尝试访问 KVStore 存储", "KVStore 未注入到运行时上下文（ToolContext.KVStore 为 nil），无法删除任务", "确认工具在正确的运行时环境中被调用；若反复出现，说明系统配置存在问题，应告知用户"))
	}
	return kv.Delete(ctx, sessionID, taskKey(sessionID, taskID))
}

func ListTasks(ctx context.Context, sessionID string) ([]string, error) {
	kv := getKVStore(ctx)
	if kv == nil {
		return nil, fmt.Errorf("%s", BuildGuide("列出任务时尝试访问 KVStore 存储", "KVStore 未注入到运行时上下文（ToolContext.KVStore 为 nil），无法读取任务列表", "确认工具在正确的运行时环境中被调用；若反复出现，说明系统配置存在问题，应告知用户"))
	}

	keys, err := kv.ListKeys(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	var ids []string
	prefix := taskKeyPrefix + sessionID + "_"
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			ids = append(ids, strings.TrimPrefix(k, prefix))
		}
	}
	return ids, nil
}

func CreateTeam(ctx context.Context, sessionID string, team *Team) error {
	kv := getKVStore(ctx)
	if kv == nil {
		return fmt.Errorf("%s", BuildGuide("创建团队时尝试访问 KVStore 存储", "KVStore 未注入到运行时上下文（ToolContext.KVStore 为 nil），无法持久化团队", "确认工具在正确的运行时环境中被调用；若反复出现，说明系统配置存在问题，应告知用户"))
	}

	team.CreatedAt = time.Now()
	team.Status = "active"

	data, err := json.Marshal(team)
	if err != nil {
		return fmt.Errorf("%s: %w", BuildGuide("创建团队时序列化团队数据失败", WithErrDetail("团队数据包含无法序列化的内容", err), "检查传入团队的字段，移除无法序列化的值后重试"), err)
	}

	return kv.Set(ctx, sessionID, teamKey(sessionID, team.Name), data, 0)
}

func GetTeam(ctx context.Context, sessionID, teamName string) (*Team, error) {
	kv := getKVStore(ctx)
	if kv == nil {
		return nil, fmt.Errorf("%s", BuildGuide("获取团队时尝试访问 KVStore 存储", "KVStore 未注入到运行时上下文（ToolContext.KVStore 为 nil），无法读取团队", "确认工具在正确的运行时环境中被调用；若反复出现，说明系统配置存在问题，应告知用户"))
	}

	data, err := kv.Get(ctx, sessionID, teamKey(sessionID, teamName))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}

	var team Team
	if err := json.Unmarshal(data, &team); err != nil {
		return nil, fmt.Errorf("%s: %w", BuildGuide("获取团队时反序列化团队数据失败", WithErrDetail("存储中的团队数据已损坏或格式不兼容", err), "团队数据可能已损坏或不兼容，可尝试删除后重新创建该团队"), err)
	}
	return &team, nil
}

func ListTeams(ctx context.Context, sessionID string) ([]string, error) {
	kv := getKVStore(ctx)
	if kv == nil {
		return nil, fmt.Errorf("%s", BuildGuide("列出团队时尝试访问 KVStore 存储", "KVStore 未注入到运行时上下文（ToolContext.KVStore 为 nil），无法读取团队列表", "确认工具在正确的运行时环境中被调用；若反复出现，说明系统配置存在问题，应告知用户"))
	}

	keys, err := kv.ListKeys(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	var names []string
	prefix := teamKeyPrefix + sessionID + "_"
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			names = append(names, strings.TrimPrefix(k, prefix))
		}
	}
	return names, nil
}

func DeleteTeam(ctx context.Context, sessionID, teamName string) error {
	kv := getKVStore(ctx)
	if kv == nil {
		return fmt.Errorf("%s", BuildGuide("删除团队时尝试访问 KVStore 存储", "KVStore 未注入到运行时上下文（ToolContext.KVStore 为 nil），无法删除团队", "确认工具在正确的运行时环境中被调用；若反复出现，说明系统配置存在问题，应告知用户"))
	}
	return kv.Delete(ctx, sessionID, teamKey(sessionID, teamName))
}

func getKVStore(ctx context.Context) store.KVStore {
	tc := GetToolContext(ctx)
	if tc == nil {
		return nil
	}
	return tc.KVStore
}
