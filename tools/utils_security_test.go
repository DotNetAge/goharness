package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateFileSafety_ValidPaths(t *testing.T) {
	tmpDir := t.TempDir()

	testCases := []struct {
		name       string
		path       string
		projectDir string
		wantErr    bool
	}{
		{
			name:       "valid file in project root",
			path:       filepath.Join(tmpDir, "file.txt"),
			projectDir: tmpDir,
			wantErr:    false,
		},
		{
			name:       "valid nested path",
			path:       filepath.Join(tmpDir, "subdir", "deep", "file.txt"),
			projectDir: tmpDir,
			wantErr:    false,
		},
		{
			name:       "project directory itself",
			path:       tmpDir,
			projectDir: tmpDir,
			wantErr:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {

			if tc.path != tmpDir {
				dir := filepath.Dir(tc.path)
				os.MkdirAll(dir, 0755)
				os.WriteFile(tc.path, []byte("test"), 0644)
			}

			err := ValidateFileSafety(tc.path, tc.projectDir)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateFileSafety() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateFileSafety_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	otherDir := t.TempDir()

	testCases := []struct {
		name        string
		path        string
		projectDir  string
		wantErr     bool
		errContains string
	}{
		{
			name:        "directory traversal with ..",
			path:        filepath.Join(tmpDir, "..", filepath.Base(otherDir), "secret.txt"),
			projectDir:  tmpDir,
			wantErr:     true,
			errContains: "越权操作",
		},
		{
			name:        "absolute path outside project",
			path:        "/etc/passwd",
			projectDir:  tmpDir,
			wantErr:     true,
			errContains: "越权操作",
		},
		{
			name:        "symlink escape attempt",
			path:        filepath.Join(tmpDir, "link_to_etc"),
			projectDir:  tmpDir,
			wantErr:     true,
			errContains: "越权操作",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "symlink escape attempt" {
				os.Symlink("/etc", filepath.Join(tmpDir, "link_to_etc"))
			}

			err := ValidateFileSafety(tc.path, tc.projectDir)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateFileSafety() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if err != nil && tc.errContains == "" && !containsString(err.Error(), tc.errContains) {
				t.Errorf("ValidateFileSafety() error = %v, want to contain %v", err, tc.errContains)
			}
		})
	}
}

func TestValidateFileSafety_SensitiveFiles(t *testing.T) {
	tmpDir := t.TempDir()

	sensitiveFiles := []string{
		".env",
		"id_rsa",
		"id_ed25519",
		"passwd",
		"shadow",
		"sudoers",
		".ssh_config",
		"known_hosts",
	}

	for _, filename := range sensitiveFiles {
		t.Run("sensitive_file_"+filename, func(t *testing.T) {
			path := filepath.Join(tmpDir, filename)
			os.WriteFile(path, []byte("sensitive"), 0644)

			err := ValidateFileSafety(path, tmpDir)
			if err == nil {
				t.Errorf("ValidateFileSafety() should block access to %s", filename)
			}
			if err != nil && !containsString(err.Error(), "敏感文件") {
				t.Errorf("ValidateFileSafety() error should mention security restriction, got: %v", err)
			}
		})
	}
}

func TestResolveTargetPath_Comprehensive(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("无法获取用户主目录: %v", err)
	}
	testCases := []struct {
		name        string
		inputPath   string
		projectDir  string
		sessionDir  string
		wantAbsPath string
		wantScope   PathScope
	}{
		{
			name:        "empty path returns project scope",
			inputPath:   "",
			projectDir:  "/project",
			sessionDir:  "/sessions/abc",
			wantAbsPath: "",
			wantScope:   PathScopeProject,
		},
		{
			name:        "absolute path returned as-is",
			inputPath:   "/etc/hosts",
			projectDir:  "/project",
			sessionDir:  "/sessions/abc",
			wantAbsPath: "/etc/hosts",
			wantScope:   "",
		},
		{
			name:        "session prefix with session dir",
			inputPath:   "session:file.txt",
			projectDir:  "/project",
			sessionDir:  "/sessions/abc",
			wantAbsPath: "/sessions/abc/file.txt",
			wantScope:   PathScopeSession,
		},
		{
			name:        "session prefix falls back to project when session dir empty",
			inputPath:   "session:file.txt",
			projectDir:  "/project",
			sessionDir:  "",
			wantAbsPath: "/project/file.txt",
			wantScope:   PathScopeProject,
		},
		{
			name:        "relative path resolved against project dir",
			inputPath:   "src/main.go",
			projectDir:  "/project",
			sessionDir:  "/sessions/abc",
			wantAbsPath: "/project/src/main.go",
			wantScope:   PathScopeProject,
		},
		{
			name:        "dot-slash prefix normalized against project dir",
			inputPath:   "./src/main.go",
			projectDir:  "/project",
			sessionDir:  "/sessions/abc",
			wantAbsPath: "/project/src/main.go",
			wantScope:   PathScopeProject,
		},
		{
			name:        "bare dot resolves to project dir",
			inputPath:   ".",
			projectDir:  "/project",
			sessionDir:  "/sessions/abc",
			wantAbsPath: "/project",
			wantScope:   PathScopeProject,
		},
		{
			name:        "dot-dot climbs above project dir textually",
			inputPath:   "../outside.txt",
			projectDir:  "/project",
			sessionDir:  "/sessions/abc",
			wantAbsPath: "/outside.txt",
			wantScope:   PathScopeProject,
		},
		{
			name:        "double dot-dot climbs further",
			inputPath:   "../../etc/passwd",
			projectDir:  "/project",
			sessionDir:  "/sessions/abc",
			wantAbsPath: "/etc/passwd",
			wantScope:   PathScopeProject,
		},
		{
			name:        "tilde expands to home dir",
			inputPath:   "~/workspaces",
			projectDir:  "/project",
			sessionDir:  "/sessions/abc",
			wantAbsPath: filepath.Join(home, "workspaces"),
			wantScope:   "",
		},
		{
			name:        "bare tilde expands to home dir",
			inputPath:   "~",
			projectDir:  "/project",
			sessionDir:  "/sessions/abc",
			wantAbsPath: home,
			wantScope:   "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotAbsPath, gotScope := ResolveTargetPath(tc.inputPath, tc.projectDir, tc.sessionDir)
			if gotAbsPath != tc.wantAbsPath {
				t.Errorf("ResolveTargetPath() absPath = %v, want %v", gotAbsPath, tc.wantAbsPath)
			}
			if gotScope != tc.wantScope {
				t.Errorf("ResolveTargetPath() scope = %v, want %v", gotScope, tc.wantScope)
			}
		})
	}
}

func TestSafeOpenFile_Integration(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("content"), 0644)

	t.Run("valid file opens successfully", func(t *testing.T) {
		file, err := SafeOpenFile(testFile, tmpDir, os.O_RDONLY, 0)
		if err != nil {
			t.Fatalf("SafeOpenFile() unexpected error: %v", err)
		}
		defer file.Close()

		data := make([]byte, 7)
		n, _ := file.Read(data)
		if n != 7 || string(data) != "content" {
			t.Errorf("SafeOpenFile() read unexpected content: %s", data)
		}
	})

	t.Run("outside workspace is blocked", func(t *testing.T) {
		_, err := SafeOpenFile("/etc/hostname", tmpDir, os.O_RDONLY, 0)
		if err == nil {
			t.Error("SafeOpenFile() should block access outside workspace")
		}
	})
}

func TestSafeCreateFile_Integration(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("creates new file successfully", func(t *testing.T) {
		newFile := filepath.Join(tmpDir, "new.txt")

		file, err := SafeCreateFile(newFile, tmpDir, 0644)
		if err != nil {
			t.Fatalf("SafeCreateFile() unexpected error: %v", err)
		}
		defer file.Close()

		file.WriteString("new content")
		file.Close()

		data, _ := os.ReadFile(newFile)
		if string(data) != "new content" {
			t.Errorf("SafeCreateFile() wrote unexpected content: %s", data)
		}
	})

	t.Run("blocks creation outside workspace", func(t *testing.T) {
		_, err := SafeCreateFile("/tmp/outside.txt", tmpDir, 0644)
		if err == nil {
			t.Error("SafeCreateFile() should block creation outside workspace")
		}
	})
}

func TestSessionContextKey_Operations(t *testing.T) {
	ctx := WithSessionID(context.Background(), "test-session-123")

	t.Run("extracts embedded session ID", func(t *testing.T) {
		sessionID := ExtractSessionID(ctx)
		if sessionID != "test-session-123" {
			t.Errorf("ExtractSessionID() = %v, want %v", sessionID, "test-session-123")
		}
	})

	t.Run("returns empty for context without session ID", func(t *testing.T) {
		bgCtx := context.Background()
		sessionID := ExtractSessionID(bgCtx)
		if sessionID != "" {
			t.Errorf("ExtractSessionID() from background = %v, want empty", sessionID)
		}
	})

	t.Run("overwrites existing session ID", func(t *testing.T) {
		ctx1 := WithSessionID(context.Background(), "first-session")
		ctx2 := WithSessionID(ctx1, "second-session")

		sessionID := ExtractSessionID(ctx2)
		if sessionID != "second-session" {
			t.Errorf("ExtractSessionID() after overwrite = %v, want %v", sessionID, "second-session")
		}
	})
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstringHelper(s, substr))
}

func containsSubstringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
