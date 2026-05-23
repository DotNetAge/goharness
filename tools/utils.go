package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateRequired checks that a required parameter exists.
func ValidateRequired(params map[string]any, key string) error {
	if _, ok := params[key]; !ok {
		return fmt.Errorf("missing required parameter: %s", key)
	}
	return nil
}

// ValidateRequiredString validates that a required string parameter exists and is of string type.
func ValidateRequiredString(params map[string]any, key string) (string, error) {
	if err := ValidateRequired(params, key); err != nil {
		return "", err
	}

	str, ok := params[key].(string)
	if !ok {
		return "", fmt.Errorf("invalid type for parameter '%s': expected string", key)
	}
	return str, nil
}

// ValidateFileSafety verifies file access safety using path anchoring.
// It normalizes the path via filepath.Clean, resolves symlinks, and ensures
// the real path stays within the allowed workspace boundary.
//
// Parameters:
//   - path: The file path to validate
//   - projectDir: The project directory (Layer 2) to anchor paths against.
//     If empty, falls back to os.Getwd() for backward compatibility.
func ValidateFileSafety(path string, projectDir string) error {
	// Step 1: Clean the path to eliminate relative components like ../
	cleaned := filepath.Clean(path)

	// Step 2: Resolve to absolute path
	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Step 3: Resolve symlinks to get the real path
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// If the file does not exist (e.g. a file about to be created), fall back to the absolute path.
		// Only evaluate symlinks for paths that already exist.
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to resolve symlinks: %w", err)
		}
		realPath = absPath
	}

	// Step 4: Resolve the project directory's real path (also resolving symlinks)
	// Use provided projectDir (Design-time safety) with fallback to CWD
	if projectDir == "" {
		projectDir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get project directory: %w", err)
		}
	}
	realCwd, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		realCwd = projectDir
	}

	// Step 5: Ensure the real path is anchored within the project directory
	if !strings.HasPrefix(realPath, realCwd+string(filepath.Separator)) && realPath != realCwd {
		return fmt.Errorf("access denied: path %q resolves to %q which is outside the workspace %q", path, realPath, realCwd)
	}

	// Step 6: Check for sensitive system files
	baseName := filepath.Base(realPath)
	if baseName == ".env" || baseName == "id_rsa" || baseName == "id_ed25519" ||
		baseName == "passwd" || baseName == "shadow" || baseName == "sudoers" {
		return fmt.Errorf("access to %s is restricted for security reasons", baseName)
	}

	return nil
}

// TruncateString truncates a string to maxLen runes, appending "..." if truncated.
// It counts by runes to safely handle multi-byte characters.
func TruncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

// ToFloat64 converts a numeric value of any common type to float64.
func ToFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case int32:
		return float64(val), true
	default:
		return 0, false
	}
}

// PathScope represents the resolved scope of a file path.
type PathScope string

const (
	PathScopeProject PathScope = "project" // Resolves to project directory
	PathScopeSession PathScope = "session" // Resolves to session sandbox directory
)

const sessionPathPrefix = "session:"

// ResolveTargetPath resolves a file path with optional session: prefix.
// This provides minimal syntax sugar for explicit directory selection.
//
// Behavior:
//   - "session:filename" → resolves to <sessionDir>/filename (scope: session)
//                    → if sessionDir is empty, falls back to <projectDir>/filename
//   - "relative/path"  → resolves to <projectDir>/relative/path (scope: project)
//                    → if projectDir is empty, falls back to current working directory
//   - "/absolute/path" → returns as-is (scope: empty)
//
// This is intentionally simple - no heuristic inference.
// Directory semantics should be guided via System Prompt (Agent Native approach).
func ResolveTargetPath(inputPath string, projectDir, sessionDir string) (absPath string, scope PathScope) {
	if inputPath == "" {
		return "", PathScopeProject
	}

	if filepath.IsAbs(inputPath) {
		return inputPath, ""
	}

	if strings.HasPrefix(inputPath, sessionPathPrefix) {
		filename := strings.TrimPrefix(inputPath, sessionPathPrefix)
		targetDir := sessionDir

		// Defensive fallback: if sessionDir is empty, use projectDir instead
		if targetDir == "" {
			targetDir = projectDir
			scope = PathScopeProject // Fallback to project scope for logging clarity
		} else {
			scope = PathScopeSession
		}

		return filepath.Join(targetDir, filename), scope
	}

	// Default: resolve relative to projectDir with defensive fallback
	targetDir := projectDir
	if targetDir == "" {
		targetDir, _ = os.Getwd() // Last resort: use CWD only when no context available
	}

	return filepath.Join(targetDir, inputPath), PathScopeProject
}


// SessionContextKey is the context key for storing session ID.
type SessionContextKey struct{}

// ExtractSessionID extracts the session ID from context.
func ExtractSessionID(ctx context.Context) string {
	if sessionID, ok := ctx.Value(SessionContextKey{}).(string); ok {
		return sessionID
	}
	return ""
}

// WithSessionID embeds a session ID into context.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, SessionContextKey{}, sessionID)
}
