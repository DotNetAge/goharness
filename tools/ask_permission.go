package tools

import (
	"fmt"
	"sync"
	"time"

	"github.com/DotNetAge/goreact/core"
)

// AskPermission is a ToolPermissionChecker that suspends tool execution
// and waits for the user's authorization decision.
//
// It implements both core.ToolPermissionChecker and core.PermissionResponder.
// When CheckPermissions returns PermissionAsk, the tool execution blocks
// until Respond() or RespondError() is called from external code (e.g., WebSocket handler).
//
// Usage:
//
//	askPerm := NewAskPermission()
//	askPerm.SetEventEmitter(func(e core.ReactEvent) { eventBus.Emit(e) })
//	registry.SetPermissionChecker(askPerm)
//
//	// In the permission check:
//	result := askPerm.CheckPermissions(ctx) // returns PermissionAsk
//	// ... blocks until user responds ...
//
//	// User responds (from WebSocket handler, CLI, etc.):
//	askPerm.Respond(core.PermissionResult{Behavior: core.PermissionAllow})
type AskPermission struct {
	// EventCallback emits ReactEvents (PermissionRequest, PermissionDenied).
	eventEmitter func(core.ReactEvent)

	mu      sync.Mutex
	pending *permRequest
}

type permRequest struct {
	toolName   string
	toolParams map[string]any // stored for AskUser merge
	done       chan struct{}
	result   core.PermissionResult
	err      error
}

// NewAskPermission creates a new AskPermission checker.
func NewAskPermission() *AskPermission {
	return &AskPermission{}
}

// SetEventEmitter sets a callback for emitting ReactEvents.
// Called by the reactor during initialization.
func (p *AskPermission) SetEventEmitter(fn func(core.ReactEvent)) {
	p.eventEmitter = fn
}

// CheckPermissions implements core.ToolPermissionChecker.
//
// Decision logic based on tool properties:
//   - LevelSafe + IsReadOnly: always allow
//   - LevelSafe (not read-only): always allow
//   - LevelSensitive: ask (requires user confirmation)
//   - LevelHighRisk: ask (requires user confirmation)
//
// The caller (ExecuteTool) handles the blocking when PermissionAsk is returned.
// After the user responds, CheckPermissions is called again to get the final decision.
func (p *AskPermission) CheckPermissions(ctx *core.ToolUseContext) core.PermissionResult {
	// If there's a stored result from a previous user response, return it
	p.mu.Lock()
	if p.pending != nil && p.pending.toolName == ctx.ToolName && p.pending.result.Behavior != "" {
		result := p.pending.result
		p.mu.Unlock()
		return result
	}
	p.mu.Unlock()

	// Special case: AskUser tool always goes through the permission system
	if ctx.ToolName == "AskUser" {
		questions := extractPermissionQuestions(ctx.Params)
		return core.PermissionResult{
			Behavior:  core.PermissionAsk,
			Message:   "Answer questions?",
			Questions: questions,
		}
	}

	// Auto-allow for safe/read-only tools
	info := ctx.ToolInfo
	if info != nil {
		if info.SecurityLevel == core.LevelSafe || info.IsReadOnly {
			return core.PermissionResult{Behavior: core.PermissionAllow}
		}
	}

	// For sensitive and high-risk tools, ask for user confirmation
	reason := fmt.Sprintf("Tool %q requires your authorization before execution", ctx.ToolName)
	if info != nil && info.SecurityLevel == core.LevelHighRisk {
		reason = fmt.Sprintf("Tool %q is high-risk and requires your explicit authorization", ctx.ToolName)
	}

	return core.PermissionResult{
		Behavior: core.PermissionAsk,
		Message:  reason,
	}
}

// extractPermissionQuestions converts AskUser's params into PermissionQuestion structs.
func extractPermissionQuestions(params map[string]any) []core.PermissionQuestion {
	qText, _ := params["question"].(string)
	if qText == "" {
		return nil
	}
	q := core.PermissionQuestion{
		Question: qText,
		Header:   "Question",
	}
	// Parse multiSelect flag
	if ms, ok := params["multiSelect"]; ok {
		if msBool, ok := ms.(bool); ok {
			q.MultiSelect = msBool
		}
	}
	// Parse options if provided
	if optsRaw, ok := params["options"]; ok {
		if opts, ok := optsRaw.([]any); ok {
			for _, o := range opts {
				if s, ok := o.(string); ok {
					q.Options = append(q.Options, core.QuestionOption{
						Label:       s,
						Description: "",
					})
				}
			}
		}
	}
	return []core.PermissionQuestion{q}
}

// BlockAndWait blocks until the user responds to the permission request.
// This must be called after CheckPermissions returns PermissionAsk.
// Returns the user's permission decision.
func (p *AskPermission) BlockAndWait(ctx *core.ToolUseContext) core.PermissionResult {
	p.mu.Lock()
	if p.pending != nil && p.pending.result.Behavior != "" {
		result := p.pending.result
		p.pending = nil
		p.mu.Unlock()
		return result
	}

	req := &permRequest{
		toolName:   ctx.ToolName,
		toolParams: ctx.Params,
		done:       make(chan struct{}),
	}
	p.pending = req
	p.mu.Unlock()

	select {
	case <-req.done:
		p.mu.Lock()
		result := req.result
		p.pending = nil
		p.mu.Unlock()
		return result

	case <-ctx.Ctx.Done():
		p.mu.Lock()
		p.pending = nil
		p.mu.Unlock()
		return core.PermissionResult{
			Behavior: core.PermissionDeny,
			Message:  fmt.Sprintf("permission request cancelled: %v", ctx.Ctx.Err()),
		}
	}
}

// Respond delivers the user's permission decision.
// This unblocks a waiting BlockAndWait call.
func (p *AskPermission) Respond(result core.PermissionResult) error {
	p.mu.Lock()
	req := p.pending
	if req == nil {
		p.mu.Unlock()
		return fmt.Errorf("no pending permission request")
	}
	// For AskUser, merge answers into original params so the tool
	// receives both the original question and the user's answers.
	if req.toolName == "AskUser" && result.UpdatedInput != nil && result.Behavior == core.PermissionAllow {
		merged := make(map[string]any, len(req.toolParams)+len(result.UpdatedInput))
		for k, v := range req.toolParams {
			merged[k] = v
		}
		for k, v := range result.UpdatedInput {
			merged[k] = v
		}
		result.UpdatedInput = merged
	}
	req.result = result
	// Guard against double-close
	select {
	case <-req.done:
	default:
		close(req.done)
	}
	p.mu.Unlock()
	return nil
}

// RespondError delivers an error to a waiting permission request.
func (p *AskPermission) RespondError(err error) error {
	p.mu.Lock()
	req := p.pending
	if req == nil {
		p.mu.Unlock()
		return fmt.Errorf("no pending permission request")
	}
	req.err = err
	req.result = core.PermissionResult{
		Behavior: core.PermissionDeny,
		Message:  err.Error(),
	}
	// Guard against double-close
	select {
	case <-req.done:
	default:
		close(req.done)
	}
	p.mu.Unlock()
	return nil
}

// IsWaiting returns true if the permission system is currently blocked waiting for a response.
func (p *AskPermission) IsWaiting() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pending != nil
}

// WaitWithTimeout waits for the permission system to become ready.
func (p *AskPermission) WaitWithTimeout(timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		p.mu.Lock()
		waiting := p.pending != nil
		p.mu.Unlock()
		if !waiting {
			return nil
		}
		select {
		case <-deadline:
			return fmt.Errorf("timed out waiting for permission response")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
