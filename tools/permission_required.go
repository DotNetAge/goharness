package tools

import (
	"context"
	"strings"
)

// PermissionRequired is an opt-in interface that tools can implement to
// declare that their execution needs a runtime permission check.
//
// # Why opt-in?
//
// The vast majority of tools (Read, Grep, Glob, Ls, WebSearch, WebFetch,
// AskUser, CollectResults, TaskCreate, TaskGet, TaskList, TaskUpdate,
// TeamCreate, TeamDelete, TeamGetTasks, TeamList, Skill, Sleep) have
// SecurityLevel = LevelSafe / IsReadOnly = true and can be invoked
// without bothering the user. Only a small subset of tools — Bash,
// RunScript, Write, Edit — touch the filesystem or shell in a way that
// could damage the system, so they opt in by implementing this interface.
//
// # How it works
//
// The runtime calls Grant(ctx, params) *before* tool execution:
//
//   - granted == true  → the tool is safe; runtime executes it directly.
//   - granted == false → the tool needs user approval. Runtime emits a
//                        PermissionPending event, saves the invocation in
//                        session.PendingPermission, and stops the thinking
//                        loop. The user responds via a "magic word" (see
//                        PermissionAllow / PermissionDeny below) in a new
//                        Ask() call. Runtime then either runs the tool
//                        (Allow) or appends a "Permission Denied" tool
//                        result (Deny) before continuing the loop.
//
// # Magic words vs AskUser
//
// AskUser's response (e.g. "option A") is itself part of the LLM context:
// the assistant's next turn sees both the question and the user's answer.
//
// Permission is invisible to the LLM: the magic word "PermissionAllow" /
// "PermissionDeny" arrives via the regular chat channel but is filtered
// out by the runtime before reaching the session. The LLM only ever sees
// the tool result (success or "Permission Denied"), never the "waiting
// for human approval" intermediate state.
//
// # Implementation
//
// Each tool decides what is "restricted". Typical signals:
//   - Bash: dangerousPatterns match, or base command is not whitelisted.
//   - Write / Edit: resolved path is outside the project/session boundary.
//   - RunScript: script path is outside the skill root, or the interpreter
//                is not in the platform's supported list.
//
// Hard errors (sensitive files like .env, .ssh, etc.) should NOT be
// expressed via Grant() — keep them as errors in Execute(). Grant() only
// expresses "ask, but user can override".
type PermissionRequired interface {
	// Grant inspects the tool input and returns whether the tool can
	// proceed without asking the user.
	//
	// Parameters:
	//   - ctx:    Standard context.Context (no deadline required — runtime
	//            handles cancellation if the user aborts).
	//   - params: The same parameter map that will be passed to Execute.
	//            Tools should treat it as read-only; if a tool needs to
	//            normalize the input (e.g. resolve "session:" prefixes),
	//            it should re-do that resolution in Execute() rather than
	//            mutate params here.
	//
	// Return:
	//   - granted: true if no permission is needed; false to trigger the
	//              permission flow.
	//   - reason:  Human-readable explanation shown in the UI when asking
	//              the user. Ignored when granted is true. Should describe
	//              WHAT triggered the check (e.g. "command contains 'rm
	//              -rf /'", "path is outside the project boundary"), not
	//              just "this needs approval".
	Grant(ctx context.Context, params map[string]any) (granted bool, reason string)
}

// ImplementsPermissionRequired reports whether a tool optionally implements
// the PermissionRequired interface. Tools that do not implement it are
// considered "always allow" by the runtime.
func ImplementsPermissionRequired(tool FuncTool) bool {
	_, ok := tool.(PermissionRequired)
	return ok
}

// Magic-word constants used by the UI to respond to a PermissionPending
// event. The runtime intercepts these as control signals: the message is
// never appended to the session, and the runtime immediately resolves the
// pending permission (executing the tool on Allow, or appending a
// "Permission Denied" result on Deny) before continuing the loop.
//
// Format is a single bare word with no prefix so the UI can render it as
// plain text in the chat box. Magic-word detection is whitespace-trimmed
// and case-insensitive.
const (
	// PermissionAllow is sent by the UI to approve the pending permission
	// and run the tool with its original arguments.
	PermissionAllow = "PermissionAllow"

	// PermissionAllowSession is sent by the UI when the user checks
	// "Remember my choice for this session". It works like PermissionAllow
	// — the tool is executed — but also adds the tool + its approved
	// parameters to the session-level whitelist ({sessionDir}/session-wl.json).
	// Subsequent invocations of the same tool with matching parameters
	// will auto-grant without user confirmation.
	PermissionAllowSession = "PermissionAllowSession"

	// PermissionDeny is sent by the UI to reject the pending permission.
	// The LLM sees a "Permission Denied" tool result and can adapt its
	// plan (e.g. ask the user, choose a different path, etc.).
	PermissionDeny = "PermissionDeny"
)

// ClassifyMagicWord returns the magic-word action implied by a user message
// after whitespace trimming, or "" if the message is not a magic word.
//
// The detection is intentionally narrow — the whole point is to keep the
// permission flow invisible to the LLM. Anything other than an exact
// (trimmed, case-insensitive) match for PermissionAllow / PermissionDeny
// is treated as a regular user message.
func ClassifyMagicWord(msg string) string {
	trimmed := strings.TrimSpace(msg)
	switch {
	case strings.EqualFold(trimmed, PermissionAllow):
		return PermissionAllow
	case strings.EqualFold(trimmed, PermissionAllowSession):
		return PermissionAllowSession
	case strings.EqualFold(trimmed, PermissionDeny):
		return PermissionDeny
	default:
		return ""
	}
}
