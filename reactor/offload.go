package reactor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goreact/core"
)

const (
	offloadThreshold      = 30 * 1024
	offloadDirName         = ".goreact" + string(os.PathSeparator) + "offload"
	offloadPrefix          = "[offload:"
	offloadSuffix          = "]"
	offloadTTL             = 24 * time.Hour
	offloadCleanupInterval = 1 * time.Hour
)

type OffloadManager struct {
	logger    core.Logger
	cleanupMu sync.Mutex
	started   bool
}

func NewOffloadManager(logger core.Logger) *OffloadManager {
	return &OffloadManager{logger: logger}
}

func (m *OffloadManager) StartBackgroundCleanup() {
	m.cleanupMu.Lock()
	defer m.cleanupMu.Unlock()
	if m.started {
		return
	}
	m.started = true
	go m.periodicOffloadCleanup()
}

func resultExceedsThreshold(result string) bool {
	return len(result) > offloadThreshold
}

func offloadResult(ctx context.Context, sessionID, toolName, result string) string {
	offloadDir := offloadPath(sessionID)
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

func offloadPath(sessionID string) string {
	return filepath.Join(offloadDirName, sessionID)
}

func (r *Reactor) offloadLargeResults(ctx *ReactContext) {
	if ctx.LastAction == nil || len(ctx.LastAction.Results) == 0 {
		return
	}

	sessionID := r.resolveSessionID(ctx)
	for i, tr := range ctx.LastAction.Results {
		if !resultExceedsThreshold(tr.Result) {
			continue
		}
		ref := offloadResult(ctx.Ctx(), sessionID, tr.ToolName, tr.Result)
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

func (m *OffloadManager) CleanupSessionOffloads(sessionID string) error {
	sessionDir := offloadPath(sessionID)

	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read session offload directory: %w", err)
	}

	for _, entry := range entries {
		filePath := filepath.Join(sessionDir, entry.Name())
		if err := os.Remove(filePath); err != nil {
			m.logger.Warn("failed to remove session offload file",
				"file", filePath,
				"error", err,
			)
		}
	}

	if err := os.Remove(sessionDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove session offload directory: %w", err)
	}

	m.logger.Info("session offload cleanup completed",
		"session_id", sessionID,
		"files_removed", len(entries),
	)

	return nil
}

func (m *OffloadManager) periodicOffloadCleanup() {
	ticker := time.NewTicker(offloadCleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		m.cleanupExpiredOffloads()
	}
}

func (m *OffloadManager) cleanupExpiredOffloads() {
	m.cleanupMu.Lock()
	defer m.cleanupMu.Unlock()

	rootDir := offloadDirName
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		m.logger.Warn("failed to read offload directory", "dir", rootDir, "error", err)
		return
	}

	now := time.Now()
	var totalCleaned int64

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		sessionDir := filepath.Join(rootDir, entry.Name())
		files, err := os.ReadDir(sessionDir)
		if err != nil {
			continue
		}

		for _, file := range files {
			info, err := file.Info()
			if err != nil {
				continue
			}

			if now.Sub(info.ModTime()) > offloadTTL {
				filePath := filepath.Join(sessionDir, file.Name())
				if err := os.Remove(filePath); err != nil {
					m.logger.Warn("failed to clean up offloaded file",
						"file", filePath,
						"error", err,
					)
					continue
				}
				totalCleaned++
			}
		}

		if remaining, err := os.ReadDir(sessionDir); err == nil && len(remaining) == 0 {
			os.Remove(sessionDir)
		}
	}

	if totalCleaned > 0 {
		m.logger.Info("offload cleanup completed",
			"files_removed", totalCleaned,
		)
	}
}
