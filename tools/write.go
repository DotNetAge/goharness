package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DotNetAge/goreact/core"
)

// Write implements a tool for writing files to the filesystem.
type Write struct {
	info *core.ToolInfo
}

const writeDescription = `Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path unless append mode is true.
- If this is an existing file, you should normally use the read tool first to get the current contents.
- ALWAYS prefer editing existing files using file_edit tool in the codebase. NEVER write new files unless explicitly required.
- The path parameter must be an absolute path, not a relative path.`

// NewWriteTool creates a file write tool.
func NewWriteTool() core.FuncTool {
	return &Write{
		info: &core.ToolInfo{
			Name:          "Write",
			Description:   writeDescription,
			Prompt: `Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path. If this is an existing file, you MUST use the Read tool first to read the file's contents. This tool will fail if you did not read the file first.
- Prefer the file_edit tool for modifying existing files — it only sends the diff. Only use this tool to create new files or for complete rewrites.
- NEVER create documentation files (*.md) or README files unless explicitly requested by the User.
- Only use emojis if the user explicitly requests it. Avoid writing emojis to files unless asked.`,
			Tags:         []string{"file", "filesystem", "write", "create"},
			SecurityLevel: core.LevelSensitive,
		},
	}
}

func (w *Write) Info() *core.ToolInfo {
	return w.info
}

func (w *Write) Execute(ctx context.Context, params map[string]any) (any, error) {
	path, err := ValidateRequiredString(params, "path")
	if err != nil {
		return nil, err
	}

	content, err := ValidateRequiredString(params, "content")
	if err != nil {
		return nil, err
	}

	logger := getLogger(ctx)

	// Resolve path with optional session: prefix support
	tc := core.GetToolContext(ctx)
	projectDir := tc.ProjectDir
	sessionDir := tc.SessionDir
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}
	if sessionDir == "" {
		sessionDir = projectDir
	}

	resolvedPath, scope := ResolveTargetPath(path, projectDir, sessionDir)

	logger.Info("writing file",
		"input_path", path,
		"resolved_path", resolvedPath,
		"scope", scope,
		"content_len", len(content),
	)

	// Security check on resolved path
	if err := ValidateFileSafety(resolvedPath); err != nil {
		return nil, err
	}

	// Ensure the parent directory exists
	dir := filepath.Dir(resolvedPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Check if append mode is enabled
	appendMode := false
	if append, ok := params["append"].(bool); ok {
		appendMode = append
	} else if appendStr, ok := params["append"].(string); ok {
		appendMode = appendStr == "true" || appendStr == "1"
	} else if appendNum, ok := params["append"].(float64); ok {
		appendMode = appendNum != 0
	}

	var file *os.File
	if appendMode {
		// Append mode
		file, err = os.OpenFile(resolvedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open file for appending: %w", err)
		}
	} else {
		// Overwrite mode
		file, err = os.Create(resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create file: %w", err)
		}
	}
	defer file.Close()

	// Write content
	bytesWritten, err := file.WriteString(content)
	if err != nil {
		return nil, fmt.Errorf("failed to write content: %w", err)
	}

	// Get file info
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	return map[string]any{
		"success": true,
		"path":    resolvedPath,
		"scope":   scope,
		"mode": func() string {
			if appendMode {
				return "append"
			} else {
				return "overwrite"
			}
		}(),
		"bytes_written": bytesWritten,
		"total_size":    info.Size(),
		"message": func() string {
			if appendMode {
				return "Content appended successfully"
			} else {
				return "File written successfully"
			}
		}(),
	}, nil
}
