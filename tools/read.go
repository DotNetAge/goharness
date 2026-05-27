package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/DotNetAge/goreact/events"
	"github.com/DotNetAge/goreact/store"
)

// Read implements a tool for reading files from the filesystem.
type Read struct {
	info   *ToolInfo
	limits FileReadingLimits
}

// NewReadTool creates a file read tool with default limits.
func NewReadTool() FuncTool {
	return NewReadToolWithLimits(DefaultFileReadingLimits())
}

// NewReadToolWithLimits creates a read tool with custom limits.
func NewReadToolWithLimits(limits FileReadingLimits) FuncTool {
	return &Read{
		limits: limits,
		info: &ToolInfo{
			Name:        "Read",
			Description: "Reads a file from the local filesystem.",
			Prompt: `Reads a file from the local filesystem. You can access any file directly by using this tool.
Assume this tool is able to read all files on the machine. If the User provides a path to a file assume that path is valid. It is okay to read a file that does not exist; an error will be returned.

Usage:
- The file_path parameter must be an absolute path, not a relative path
- By default, it reads up to 2000 lines starting from the beginning of the file
- Results are returned using cat -n format, with line numbers starting at 1
- This tool allows reading images (eg PNG, JPG, etc). When reading an image file the contents are presented visually
- This tool can read PDF files (.pdf). For large PDFs (more than 10 pages), you MUST provide the pages parameter to read specific page ranges
- This tool can read Jupyter notebooks (.ipynb files) and returns all cells with their outputs
- This tool can only read files, not directories. To read a directory, use an ls command via the bash tool
- You will regularly be asked to read screenshots. If the user provides a path to a screenshot, ALWAYS use this tool to view the file at the path

Skills may expose a "Base directory" in their Skill tool result. When a skill says "Base directory: <path>", use Read to access reference files in that directory — the skill instructions are a guide, not the full reference.`,
			Tags:               []string{"file", "filesystem", "read", "content"},
			SecurityLevel:      events.LevelSafe,
			IsReadOnly:         true,
			MaxResultSizeChars: -1,
			Parameters: []Parameter{
				{
					Name:        "path",
					Type:        "string",
					Required:    true,
					Description: "The absolute path to the file to read.",
				},
				{
					Name:        "offset",
					Type:        "integer",
					Required:    false,
					Description: "The line number to start reading from (1-based).",
				},
				{
					Name:        "limit",
					Type:        "integer",
					Required:    false,
					Description: "The maximum number of lines to read. Defaults to 500.",
				},
			},
		},
	}
}

func (r *Read) Info() *ToolInfo {
	return r.info
}

func (r *Read) Execute(ctx context.Context, params map[string]any) (any, error) {
	path, err := ValidateRequiredString(params, "path")
	if err != nil {
		return nil, err
	}

	logger := getLogger(ctx)

	tc := GetToolContext(ctx)
	resolvedPath, scope := ResolveTargetPath(path, tc.ProjectDir, tc.SessionDir)

	logger.Info("reading file",
		"input_path", path,
		"resolved_path", resolvedPath,
		"scope", scope,
	)

	if err := ValidateFileSafety(resolvedPath, tc.ProjectDir); err != nil {
		return nil, err
	}

	// Pre-read: check file exists, size, and type via fs.FS
	cleanPath := strings.TrimLeft(resolvedPath, "/")
	info, err := fs.Stat(store.OS, cleanPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("file does not exist: %s", resolvedPath)
		}
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory, not a file: %s", resolvedPath)
	}
	if r.limits.MaxSizeBytes > 0 && info.Size() > r.limits.MaxSizeBytes {
		return map[string]any{
			"success":    false,
			"path":       resolvedPath,
			"scope":      scope,
			"size_bytes": info.Size(),
			"error": fmt.Sprintf(
				"file too large (%.2f KB), maximum allowed is %d KB. "+
					"Use offset and limit parameters to read specific sections, "+
					"or use grep/glob to locate the relevant parts first.",
				float64(info.Size())/1024, r.limits.MaxSizeBytes/1024),
		}, nil
	}

	// Read file content via fs.FS
	data, err := store.ReadFileFromFS(store.OS, resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Get pagination parameters
	startLine := 1
	if offset, ok := ToFloat64(params["offset"]); ok && offset > 0 {
		startLine = int(offset)
	}
	maxLines := r.limits.DefaultLines
	if limit, ok := ToFloat64(params["limit"]); ok && limit > 0 {
		maxLines = int(limit)
	}
	endLine := startLine + maxLines - 1

	// Split into lines and select requested range
	allLines := strings.Split(string(data), "\n")
	totalLines := len(allLines)

	var content strings.Builder
	lineNum := 0
	linesRead := 0
	for i, line := range allLines {
		lineNum = i + 1
		if lineNum < startLine {
			continue
		}
		if lineNum > endLine {
			break
		}
		content.WriteString(fmt.Sprintf("%d\t%s\n", lineNum, line))
		linesRead++
	}

	// Track whether token budget truncated content (distinct from line-limit truncation)
	tokenTruncated := false

	// Token budget check (post-read).
	// When truncation occurs, prepend the note to the content so it's visible
	// both in the raw result AND in the persisted preview (first 2000 bytes).
	outputChars := content.Len()
	estimatedTokens := outputChars / 3
	if r.limits.MaxTokens > 0 && estimatedTokens > r.limits.MaxTokens {
		targetChars := r.limits.MaxTokens * 3
		runes := []rune(content.String())
		if len(runes) > targetChars {
			truncationNote := fmt.Sprintf(
				"\n[... content truncated: token budget of %d tokens exceeded."+
					" Remaining lines: %d-%d of %d."+
					" Use offset and limit to narrow the range. ...]\n",
				r.limits.MaxTokens, endLine+1, totalLines, totalLines)
			content.Reset()
			content.WriteString(truncationNote)
			content.WriteString(string(runes[:targetChars]))
			tokenTruncated = true
		}
	}

	// Build a top-level note field. Go sorts map keys alphabetically, so
	// "_note" sorts before all letter keys and appears first in JSON output —
	// critical for visibility in persisted-result previews (<persisted-output>,
	// first 2000 bytes).
	hasRemainingLines := linesRead >= maxLines && lineNum < totalLines
	noteParts := ""
	if tokenTruncated {
		noteParts = fmt.Sprintf("Truncated at %d tokens. Lines %d-%d remain. Use narrower offset/limit.",
			r.limits.MaxTokens, endLine+1, totalLines)
	} else if hasRemainingLines {
		noteParts = fmt.Sprintf("More content available at offset %d (lines %d-%d of %d).",
			endLine+1, endLine+1, totalLines, totalLines)
	}

	result := map[string]any{
		"_note":       noteParts,
		"success":     true,
		"path":        resolvedPath,
		"scope":       scope,
		"size_bytes":  info.Size(),
		"lines_read":  linesRead,
		"total_lines": totalLines,
		"content":     content.String(),
		"start_line":  startLine,
	}

	if hasRemainingLines || tokenTruncated {
		result["has_more"] = true
		if tokenTruncated && !hasRemainingLines {
			result["next_offset"] = startLine
			result["suggestion"] = "narrow_range"
		} else {
			result["next_offset"] = endLine + 1
		}
	}

	return result, nil
}
