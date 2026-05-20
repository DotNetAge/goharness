package reactor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	offloadThreshold      = 5 * 1024
	offloadDirSuffix      = ".offload"
	offloadPrefix          = "[offload:"
	offloadSuffix          = "]"
	offloadTTL             = 24 * time.Hour
)

func resultExceedsThreshold(result string) bool {
	return len(result) > offloadThreshold
}

// offloadPath returns the offload directory within the given session directory.
func offloadPath(sessionDir string) string {
	return filepath.Join(sessionDir, offloadDirSuffix)
}

func offloadResult(ctx context.Context, sessionDir, toolName, result string) string {
	offloadDir := offloadPath(sessionDir)
	if err := os.MkdirAll(offloadDir, 0755); err != nil {
		return result
	}

	filename := fmt.Sprintf("%s_%d.out", toolName, time.Now().UnixNano())
	filePath := filepath.Join(offloadDir, filename)

	if err := os.WriteFile(filePath, []byte(result), 0644); err != nil {
		return result
	}

	preview := result
	if len(preview) > 200 {
		preview = preview[:200]
	}

	return fmt.Sprintf("[offload:%s:%d:%s]", filePath, len(result), preview)
}

func isOffloadReference(content string) bool {
	return strings.HasPrefix(content, offloadPrefix) && strings.HasSuffix(content, offloadSuffix)
}

func restoreOffloadResult(ref string) (string, bool) {
	inner := ref[len(offloadPrefix) : len(ref)-len(offloadSuffix)]
	parts := strings.SplitN(inner, ":", 3)
	if len(parts) < 2 {
		return ref, false
	}

	data, err := os.ReadFile(parts[0])
	if err != nil {
		return ref, false
	}
	return string(data), true
}

// resolveSessionDir resolves the session directory for offload operations.
// Priority: fileStore.GetSessionPath > r.sessionDir > fallback to cwd.
func (r *Reactor) resolveSessionDir(ctx *ReactContext) string {
	sessionID := r.resolveSessionID(ctx)
	if r.fileStore != nil {
		if dir := r.fileStore.GetSessionPath(sessionID); dir != "" {
			return dir
		}
	}
	if r.sessionDir != "" {
		return r.sessionDir
	}
	return "."
}

func (r *Reactor) offloadLargeResults(ctx *ReactContext) {
	if ctx.LastAction == nil || len(ctx.LastAction.Results) == 0 {
		return
	}

	sessionDir := r.resolveSessionDir(ctx)
	for i, tr := range ctx.LastAction.Results {
		if !resultExceedsThreshold(tr.Result) {
			continue
		}
		ref := offloadResult(ctx.Ctx(), sessionDir, tr.ToolName, tr.Result)
		if ref != tr.Result {
			ctx.LastAction.Results[i].Result = ref
		}
	}
}

func (r *Reactor) restoreOffloadedResults(ctx *ReactContext) {
	for i := range ctx.ConversationHistory {
		msg := &ctx.ConversationHistory[i]
		if isOffloadReference(msg.Content) {
			if restored, ok := restoreOffloadResult(msg.Content); ok {
				msg.Content = restored
			}
		}
	}
}

// CleanupSessionOffloads removes all offloaded files within the given session
// directory, then removes the empty .offload directory.
// This is called when a session is being cleaned up.
func CleanupSessionOffloads(sessionDir string) error {
	offloadDir := offloadPath(sessionDir)

	entries, err := os.ReadDir(offloadDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read session offload directory: %w", err)
	}

	for _, entry := range entries {
		filePath := filepath.Join(offloadDir, entry.Name())
		if err := os.Remove(filePath); err != nil {
			return fmt.Errorf("failed to remove offload file %s: %w", filePath, err)
		}
	}

	if err := os.Remove(offloadDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove offload directory: %w", err)
	}

	return nil
}
