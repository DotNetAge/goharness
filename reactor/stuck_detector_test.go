package reactor

import (
	"testing"
)

func TestToolLoopDetector_NoDetection(t *testing.T) {
	d := &ToolLoopDetector{Threshold: 3}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Bash"}}},
		{Iteration: 2, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Read"}}},
		{Iteration: 3, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Bash"}}},
	}
	if diag := d.Analyze(history); diag != nil {
		t.Errorf("expected no detection, got stuck=%v pattern=%s", diag.Stuck, diag.Pattern)
	}
}

func TestToolLoopDetector_DetectThreeConsecutive(t *testing.T) {
	d := &ToolLoopDetector{Threshold: 3}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Bash"}}},
		{Iteration: 2, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Bash"}}},
		{Iteration: 3, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Bash"}}},
	}
	diag := d.Analyze(history)
	if diag == nil || !diag.Stuck {
		t.Fatal("expected tool loop detection")
	}
	if diag.Pattern != PatternToolLoop {
		t.Errorf("expected PatternToolLoop, got %s", diag.Pattern)
	}
}

func TestToolLoopDetector_BreakOnDifferentTool(t *testing.T) {
	d := &ToolLoopDetector{Threshold: 3}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Bash"}}},
		{Iteration: 2, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Bash"}}},
		{Iteration: 3, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Read"}}}, // breaks chain
	}
	if diag := d.Analyze(history); diag != nil {
		t.Errorf("expected no detection after tool change, got stuck=%v", diag.Stuck)
	}
}

func TestToolLoopDetector_NoToolCalls(t *testing.T) {
	d := &ToolLoopDetector{Threshold: 3}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAnswer},
		{Iteration: 2, Decision: DecisionAnswer},
	}
	if diag := d.Analyze(history); diag != nil {
		t.Errorf("expected no detection without tool calls, got stuck=%v", diag.Stuck)
	}
}

func TestErrorLoopDetector_DetectTwoConsecutive(t *testing.T) {
	d := &ErrorLoopDetector{Threshold: 2}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{
			{Name: "Bash", Error: "permission denied"},
		}},
		{Iteration: 2, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{
			{Name: "Bash", Error: "permission denied"},
		}},
	}
	diag := d.Analyze(history)
	if diag == nil || !diag.Stuck {
		t.Fatal("expected error loop detection")
	}
	if diag.Pattern != PatternErrorLoop {
		t.Errorf("expected PatternErrorLoop, got %s", diag.Pattern)
	}
}

func TestErrorLoopDetector_BreakOnSuccess(t *testing.T) {
	d := &ErrorLoopDetector{Threshold: 2}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{
			{Name: "Bash", Error: "not found"},
		}},
		{Iteration: 2, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{
			{Name: "Bash"}, // success — no error
		}},
	}
	if diag := d.Analyze(history); diag != nil {
		t.Errorf("expected no detection after success, got stuck=%v", diag.Stuck)
	}
}

func TestErrorLoopDetector_DifferentError(t *testing.T) {
	d := &ErrorLoopDetector{Threshold: 2}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{
			{Name: "Read", Error: "not found"},
		}},
		{Iteration: 2, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{
			{Name: "Read", Error: "permission denied"}, // different error
		}},
	}
	if diag := d.Analyze(history); diag != nil {
		t.Errorf("expected no detection with different errors, got stuck=%v", diag.Stuck)
	}
}

func TestOscillationDetector_DetectAlternation(t *testing.T) {
	d := &OscillationDetector{Threshold: 4}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct},
		{Iteration: 2, Decision: DecisionAnswer},
		{Iteration: 3, Decision: DecisionAct},
		{Iteration: 4, Decision: DecisionAnswer},
	}
	diag := d.Analyze(history)
	if diag == nil || !diag.Stuck {
		t.Fatal("expected oscillation detection")
	}
	if diag.Pattern != PatternOscillation {
		t.Errorf("expected PatternOscillation, got %s", diag.Pattern)
	}
}

func TestOscillationDetector_ThreeWayOscillation(t *testing.T) {
	d := &OscillationDetector{Threshold: 4}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct},
		{Iteration: 2, Decision: DecisionAnswer},
		{Iteration: 3, Decision: DecisionAct},
		{Iteration: 4, Decision: DecisionAnswer},
	}
	diag := d.Analyze(history)
	if diag == nil || !diag.Stuck {
		t.Fatal("expected oscillation detection for 2-way alternation")
	}
}

func TestOscillationDetector_NotEnoughIterations(t *testing.T) {
	d := &OscillationDetector{Threshold: 4}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct},
		{Iteration: 2, Decision: DecisionAnswer},
	}
	if diag := d.Analyze(history); diag != nil {
		t.Errorf("expected no detection with insufficient history, got stuck=%v", diag.Stuck)
	}
}

func TestOscillationDetector_NoAlternation(t *testing.T) {
	d := &OscillationDetector{Threshold: 4}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct},
		{Iteration: 2, Decision: DecisionAct},
		{Iteration: 3, Decision: DecisionAct},
		{Iteration: 4, Decision: DecisionAct},
	}
	if diag := d.Analyze(history); diag != nil {
		t.Errorf("expected no detection with steady decision, got stuck=%v", diag.Stuck)
	}
}

func TestNoProgressDetector_DetectFiveToolCallsNoAnswer(t *testing.T) {
	d := &NoProgressDetector{Threshold: 5}
	history := make([]IterationSnapshot, 5)
	for i := 0; i < 5; i++ {
		history[i] = IterationSnapshot{
			Iteration: i + 1,
			Decision:  DecisionAct,
			ToolCalls: []ToolCallSnapshot{{Name: "Bash"}},
		}
	}
	diag := d.Analyze(history)
	if diag == nil || !diag.Stuck {
		t.Fatal("expected no-progress detection")
	}
	if diag.Pattern != PatternNoProgress {
		t.Errorf("expected PatternNoProgress, got %s", diag.Pattern)
	}
}

func TestNoProgressDetector_AnswerBreaksChain(t *testing.T) {
	d := &NoProgressDetector{Threshold: 5}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct},
		{Iteration: 2, Decision: DecisionAct},
		{Iteration: 3, Decision: DecisionAct},
		{Iteration: 4, Decision: DecisionAnswer}, // breaks chain
		{Iteration: 5, Decision: DecisionAct},
	}
	if diag := d.Analyze(history); diag != nil {
		t.Errorf("expected no detection with answer breaking chain, got stuck=%v", diag.Stuck)
	}
}

func TestNoProgressDetector_NotEnoughIterations(t *testing.T) {
	d := &NoProgressDetector{Threshold: 5}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct},
		{Iteration: 2, Decision: DecisionAct},
		{Iteration: 3, Decision: DecisionAct},
	}
	if diag := d.Analyze(history); diag != nil {
		t.Errorf("expected no detection before threshold, got stuck=%v", diag.Stuck)
	}
}

func TestCompositeDetector_FirstMatchWins(t *testing.T) {
	d := &CompositeDetector{
		Detectors: []StuckDetector{
			&ToolLoopDetector{Threshold: 2},
			&ErrorLoopDetector{Threshold: 2},
		},
	}
	// Both tool loop and error loop patterns present; tool loop goes first
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Bash", Error: "err1"}}},
		{Iteration: 2, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Bash", Error: "err1"}}},
	}
	diag := d.Analyze(history)
	if diag == nil || !diag.Stuck {
		t.Fatal("expected detection from composite")
	}
	if diag.Pattern != PatternToolLoop {
		t.Errorf("expected first detector (ToolLoop) to match, got %s", diag.Pattern)
	}
}

func TestCompositeDetector_NoMatch(t *testing.T) {
	d := &CompositeDetector{
		Detectors: []StuckDetector{
			&ToolLoopDetector{Threshold: 3},
			&NoProgressDetector{Threshold: 5},
		},
	}
	history := []IterationSnapshot{
		{Iteration: 1, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Bash"}}},
		{Iteration: 2, Decision: DecisionAct, ToolCalls: []ToolCallSnapshot{{Name: "Read"}}},
	}
	if diag := d.Analyze(history); diag != nil {
		t.Errorf("expected no detection, got stuck=%v", diag.Stuck)
	}
}

func TestBuildNudgeMessage_FirstNudge(t *testing.T) {
	diag := &StuckDiagnosis{
		Stuck:       true,
		Pattern:     PatternToolLoop,
		Description: "test description",
		NudgePrefix: "Note",
		NudgeDetail: "you called Bash 3 times",
	}
	msg := buildNudgeMessage(diag, 1)
	expectedPrefix := "Note:"
	if len(msg) < len(expectedPrefix) || msg[:len(expectedPrefix)] != expectedPrefix {
		t.Errorf("expected nudge to start with 'Note:', got %q", msg)
	}
	if len(msg) < 20 {
		t.Errorf("nudge message too short: %q", msg)
	}
}

func TestBuildNudgeMessage_Warning(t *testing.T) {
	diag := &StuckDiagnosis{
		Stuck:       true,
		Pattern:     PatternErrorLoop,
		Description: "test error",
		NudgePrefix: "Note",
		NudgeDetail: "same error occurred",
	}
	msg := buildNudgeMessage(diag, 2)
	expectedPrefix := "Warning:"
	if len(msg) < len(expectedPrefix) || msg[:len(expectedPrefix)] != expectedPrefix {
		t.Errorf("expected warning to start with 'Warning:', got %q", msg)
	}
	if !contains(msg, "terminate") {
		t.Errorf("expected warning to mention termination, got %q", msg)
	}
}

func TestBuildNudgeMessage_ThirdNudge(t *testing.T) {
	diag := &StuckDiagnosis{
		Stuck:       true,
		Pattern:     PatternNoProgress,
		Description: "test",
		NudgeDetail: "no answer for 5 iterations",
	}
	msg := buildNudgeMessage(diag, 3)
	expectedPrefix := "Warning:"
	if len(msg) < len(expectedPrefix) || msg[:len(expectedPrefix)] != expectedPrefix {
		t.Errorf("expected third nudge to also start with 'Warning:', got %q", msg)
	}
	if !contains(msg, "terminate") {
		t.Errorf("expected warning to mention termination, got %q", msg)
	}
}

func TestNewDefaultStuckDetector_HasAllPatterns(t *testing.T) {
	d := NewDefaultStuckDetector()
	if d == nil {
		t.Fatal("NewDefaultStuckDetector returned nil")
	}
	if len(d.Detectors) != 4 {
		t.Errorf("expected 4 detectors, got %d", len(d.Detectors))
	}
}

// contains reports whether substr is within s.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
