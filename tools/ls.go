package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DotNetAge/goreact/events"
)

// LS lists directory contents with metadata (size, type, permissions, modification time).
type LS struct {
	info *ToolInfo
}

// NewLsTool creates an Ls tool.
func NewLsTool() FuncTool {
	return &LS{
		info: &ToolInfo{
			Name:               "Ls",
			MaxResultSizeChars: 30000,
			Description:        "List directory contents with file metadata — size, type, permissions, modification time. Supports recursive tree view and hidden files.",
			Prompt: `List the contents of a directory to browse the filesystem structure. Use this when you need to see what files exist in a directory, check file sizes, or explore the project layout before reading or editing files.

## Operations

### Basic listing — See files in a directory
Call with no parameters to list the current directory. Each entry includes: name, type (file/directory), size in bytes, modification time, and Unix permissions.

### Recursive tree view
Set recursive=true to show the full directory tree two levels deep. Sub-directories expand with their own children listed under them.

### Show hidden files
Set show_hidden=true to include dot-files (.gitignore, .env, .config, etc.). Hidden files are excluded by default.

## When to use this vs other tools
- Use Ls to explore what's in a directory before reading files.
- Use Glob to search for files matching a pattern across the whole project.
- Use Read to read a specific file's content.
- When exploring an unfamiliar codebase, start with Ls on the root directory to understand the project structure.`,
			Tags:          []string{"file", "filesystem", "list", "directory"},
			SecurityLevel: events.LevelSafe,
			Parameters: []Parameter{
				{Name: "path", Type: "string", Description: "Directory path to list. Defaults to current directory ('.').", Required: false},
				{Name: "recursive", Type: "boolean", Description: "If true, recursively list sub-directories (2 levels deep). Default: false.", Required: false},
				{Name: "show_hidden", Type: "boolean", Description: "If true, include dot-files and hidden directories. Default: false.", Required: false},
			},
		},
	}
}

func (l *LS) Info() *ToolInfo {
	return l.info
}

// maxLsItems is the maximum number of directory entries to return.
// Directories with more entries are truncated at this limit.
const maxLsItems = 500

func (l *LS) Execute(ctx context.Context, params map[string]any) (any, error) {
	// Get ToolContext for directory awareness (Design-time safety)
	tc := GetToolContext(ctx)

	// Get directory path (defaults to current directory)
	dirPath := "."
	if path, ok := params["path"].(string); ok && path != "" {
		dirPath = path
	}

	// Security check
	if err := ValidateFileSafety(dirPath, tc.ProjectDir); err != nil {
		return nil, err
	}

	// Check if path exists
	info, err := os.Stat(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("directory does not exist: %s", dirPath)
		}
		return nil, fmt.Errorf("failed to stat directory: %w", err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", dirPath)
	}

	// Get parameters
	recursive := false
	if rec, ok := params["recursive"].(bool); ok {
		recursive = rec
	}

	showHidden := false
	if hidden, ok := params["show_hidden"].(bool); ok {
		showHidden = hidden
	}

	// Read directory contents
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	// Build result
	var items []map[string]any

	for _, entry := range entries {
		if len(items) >= maxLsItems {
			break
		}

		// Skip hidden files unless show_hidden is set
		if !showHidden && strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		finfo, _ := entry.Info()
		item := map[string]any{
			"name": entry.Name(),
			"type": func() string {
				if entry.IsDir() {
					return "directory"
				} else {
					return "file"
				}
			}(),
			"size":    finfo.Size(),
			"modTime": finfo.ModTime().Format("2006-01-02 15:04:05"),
			"mode":    finfo.Mode().String(),
		}

		// If recursive mode and entry is a directory, list its children
		if recursive && entry.IsDir() {
			subDir := filepath.Join(dirPath, entry.Name())
			subEntries, err := os.ReadDir(subDir)
			if err == nil {
				children := make([]map[string]any, 0)
				for _, subEntry := range subEntries {
					if !showHidden && strings.HasPrefix(subEntry.Name(), ".") {
						continue
					}
					subFinfo, _ := subEntry.Info()
					child := map[string]any{
						"name": subEntry.Name(),
						"type": func() string {
							if subEntry.IsDir() {
								return "directory"
							} else {
								return "file"
							}
						}(),
						"size":    subFinfo.Size(),
						"modTime": subFinfo.ModTime().Format("2006-01-02 15:04:05"),
					}
					children = append(children, child)
				}
				item["children"] = children
			}
		}

		items = append(items, item)
	}

	return map[string]any{
		"success":     true,
		"path":        dirPath,
		"total_items": len(items),
		"items":       items,
		"message":     fmt.Sprintf("Listed %d item(s) in '%s'", len(items), dirPath),
	}, nil
}
