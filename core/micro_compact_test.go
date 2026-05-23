package core

import (
	"testing"
)

func TestIsReadOnlyToolResult(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"Read tool", "[Read] file content", true},
		{"Grep tool", "[Grep] matched 3 results", true},
		{"WebFetch tool", "[WebFetch] page content", true},
		{"WebSearch tool", "[WebSearch] search results", true},
		{"Glob tool", "[Glob] found 2 files", true},
		{"Skill tool", "[Skill] loaded instructions", true},
		{"AskUser tool", "[AskUser] question", true},
		{"Bash tool", "[Bash] command output", false},
		{"Write tool", "[Write] file written", false},
		{"Edit tool", "[Edit] changes applied", false},
		{"Delegate tool", "[Delegate] subagent result", false},
		{"no bracket prefix", "plain text", false},
		{"empty string", "", false},
		{"open bracket only", "[no close", false},
		{"empty tool name", "[] result", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReadOnlyToolResult(tt.content); got != tt.want {
				t.Errorf("isReadOnlyToolResult(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestMicroCompact(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		got := MicroCompact(nil, 2)
		if len(got) != 0 {
			t.Errorf("expected empty, got %d messages", len(got))
		}
	})

	t.Run("no assistant messages", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: "hello"},
			{Role: "tool", Content: "[Read] something"},
		}
		got := MicroCompact(msgs, 2)
		if len(got) != 2 {
			t.Errorf("expected 2, got %d", len(got))
		}
	})

	t.Run("keeps recent read-only results", func(t *testing.T) {
		msgs := []Message{
			{Role: "assistant", Content: "Thought: round 1\nDecision: act", ToolCalls: []ToolCall{{ID: "call1", Name: "Read", Arguments: `{}`}}},
			{Role: "tool", Content: "[Read] old content", ToolCallID: "call1"},
			{Role: "tool", Content: "[Bash] important output"},
			{Role: "assistant", Content: "Thought: round 2\nDecision: act"},
			{Role: "tool", Content: "[Read] recent content"},
		}
		got := MicroCompact(msgs, 1)
		if len(got) != 4 {
			t.Fatalf("expected 4 messages, got %d: %+v", len(got), got)
		}
		if got[0].Role != "assistant" {
			t.Errorf("expected assistant, got %s", got[0].Role)
		}
		if got[1].Content != "[Bash] important output" {
			t.Errorf("expected [Bash], got %s", got[1].Content)
		}
	})

	t.Run("removes old read-only, keeps recent", func(t *testing.T) {
		msgs := []Message{
			{Role: "assistant", Content: "Thought: round 1\nDecision: act"},
			{Role: "tool", Content: "[Read] old content"},
			{Role: "tool", Content: "[Grep] old results"},
			{Role: "assistant", Content: "Thought: round 2\nDecision: act"},
			{Role: "tool", Content: "[Read] recent content"},
			{Role: "assistant", Content: "Thought: round 3\nDecision: act"},
			{Role: "tool", Content: "[WebFetch] new content"},
		}
		got := MicroCompact(msgs, 2)
		if len(got) != 5 {
			t.Fatalf("expected 5 messages, got %d: %+v", len(got), got)
		}
	})

	t.Run("keepRecent=0 defaults to 1", func(t *testing.T) {
		msgs := []Message{
			{Role: "assistant", Content: "Thought: round 1\nDecision: act"},
			{Role: "tool", Content: "[Read] round1"},
			{Role: "assistant", Content: "Thought: round 2\nDecision: act"},
			{Role: "tool", Content: "[Read] round2"},
			{Role: "assistant", Content: "Thought: round 3\nDecision: act"},
			{Role: "tool", Content: "[Read] round3"},
		}
		got := MicroCompact(msgs, 0)
		if len(got) != 4 {
			t.Fatalf("expected 4 messages, got %d", len(got))
		}
	})

	t.Run("preserves non-tool messages", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: "original question"},
			{Role: "assistant", Content: "Thought: round 1\nDecision: act"},
			{Role: "tool", Content: "[Read] file content"},
			{Role: "assistant", Content: "Thought: round 2\nDecision: answer"},
		}
		got := MicroCompact(msgs, 1)
		if len(got) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(got))
		}
		if got[0].Role != "user" {
			t.Errorf("expected user preserved, got %s", got[0].Role)
		}
	})

	t.Run("strips orphaned ToolCalls from assistant", func(t *testing.T) {
		msgs := []Message{
			{
				Role:      "assistant",
				Content:   "Thought: round 1\nDecision: act",
				ToolCalls: []ToolCall{{ID: "read1", Name: "Read", Arguments: `{}`}},
			},
			{Role: "tool", Content: "[Read] round1", ToolCallID: "read1"},
			{
				Role:      "assistant",
				Content:   "Thought: round 2\nDecision: act",
				ToolCalls: []ToolCall{{ID: "read2", Name: "Read", Arguments: `{}`}},
			},
			{Role: "tool", Content: "[Read] round2", ToolCallID: "read2"},
		}
		got := MicroCompact(msgs, 1)
		// round 1 assistant preserved, but ToolCalls should be empty
		if len(got) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(got))
		}
		asst1 := got[0]
		if asst1.Role != "assistant" {
			t.Errorf("expected assistant, got %s", asst1.Role)
		}
		if len(asst1.ToolCalls) != 0 {
			t.Errorf("expected empty ToolCalls for compacted round, got %d calls", len(asst1.ToolCalls))
		}
		// round 2: ToolCalls preserved (asst2 is at index 1)
		asst2 := got[1]
		if len(asst2.ToolCalls) != 1 || asst2.ToolCalls[0].ID != "read2" {
			t.Errorf("expected ToolCalls preserved for recent round, got %+v", asst2.ToolCalls)
		}
		// round 2 tool result preserved (at index 2)
		if got[2].Content != "[Read] round2" {
			t.Errorf("expected tool result preserved, got %s", got[2].Content)
		}
	})

	t.Run("preserves non-read-only ToolCalls", func(t *testing.T) {
		msgs := []Message{
			{
				Role:      "assistant",
				Content:   "Thought: round 1\nDecision: act",
				ToolCalls: []ToolCall{{ID: "bash1", Name: "Bash", Arguments: `{}`}, {ID: "read1", Name: "Read", Arguments: `{}`}},
			},
			{Role: "tool", Content: "[Read] text", ToolCallID: "read1"},
			{Role: "tool", Content: "[Bash] output", ToolCallID: "bash1"},
			{Role: "assistant", Content: "Thought: round 2\nDecision: answer"},
		}
		got := MicroCompact(msgs, 1)
		// round 1: [Read] tool + ToolCall removed, [Bash] tool + ToolCall preserved
		if len(got) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(got))
		}
		asst1 := got[0]
		if len(asst1.ToolCalls) != 1 {
			t.Fatalf("expected 1 remaining ToolCall, got %d", len(asst1.ToolCalls))
		}
		if asst1.ToolCalls[0].ID != "bash1" {
			t.Errorf("expected bash1 ToolCall preserved, got %s", asst1.ToolCalls[0].ID)
		}
		// [Bash] tool message preserved
		if got[1].Content != "[Bash] output" {
			t.Errorf("expected [Bash] preserved, got %s", got[1].Content)
		}
	})

	t.Run("no ToolCalls on assistant", func(t *testing.T) {
		msgs := []Message{
			{Role: "assistant", Content: "Thought: round 1\nDecision: act"},
			{Role: "tool", Content: "[Read] content", ToolCallID: "orphan1"},
			{Role: "assistant", Content: "Thought: round 2\nDecision: answer"},
		}
		got := MicroCompact(msgs, 1)
		if len(got) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(got))
		}
		if len(got[0].ToolCalls) != 0 {
			t.Errorf("expected empty ToolCalls, got %+v", got[0].ToolCalls)
		}
	})
}
