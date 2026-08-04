package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateSkillName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "code-review", false},
		{"valid with numbers", "pdf-processing-2", false},
		{"valid single char", "a", false},
		{"invalid empty", "", true},
		{"invalid uppercase", "PDF-Processing", true},
		{"invalid hyphen prefix", "-code-review", true},
		{"invalid hyphen suffix", "code-review-", true},
		{"invalid double hyphen", "code--review", true},
		{"invalid special chars", "code@review", true},
		{"invalid spaces", "code review", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSkillName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSkillName(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSkillDescription(t *testing.T) {
	shortDesc := "short desc"
	longDesc := ""
	for i := 0; i < 1025; i++ {
		longDesc += "a"
	}
	maxDesc := ""
	for i := 0; i < 1024; i++ {
		maxDesc += "a"
	}

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"short description", shortDesc, false},
		{"max length", maxDesc, false},
		{"too long", longDesc, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSkillDescription(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSkillDescription(%q...) error = %v, wantErr = %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestParseYamlFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "simple",
			input:   "---\nname: my-skill\ndescription: A test skill\n---\nBody content",
			wantErr: false,
		},
		{
			name:    "no frontmatter",
			input:   "Just body content",
			wantErr: true,
		},
		{
			name:    "empty",
			input:   "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseYamlFrontmatter(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseYamlFrontmatter() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestFileSystemSkillLoader_Load(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建一个有效的技能目录
	skillDir := filepath.Join(tmpDir, "my-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillContent := "---\nname: my-skill\ndescription: A test skill\n---\nBody content"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 创建一个非技能目录（无 SKILL.md）
	nonSkillDir := filepath.Join(tmpDir, "non-skill")
	if err := os.MkdirAll(nonSkillDir, 0755); err != nil {
		t.Fatal(err)
	}

	loader := NewFileSystemSkillLoader(tmpDir)
	skills, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("expected skill name 'my-skill', got %q", skills[0].Name)
	}
}

func TestFileSystemSkillLoader_NonExistentDir(t *testing.T) {
	loader := NewFileSystemSkillLoader("/path/does/not/exist")
	skills, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected no skills for non-existent dir, got %d", len(skills))
	}
}

func TestFileSystemSkillLoader_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	loader := NewFileSystemSkillLoader(tmpDir)
	skills, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected no skills for empty dir, got %d", len(skills))
	}
}

func TestFileSystemSkillLoader_MultipleSkills(t *testing.T) {
	tmpDir := t.TempDir()

	skillNames := []string{"skill-a", "skill-b", "skill-c"}
	for _, name := range skillNames {
		dir := filepath.Join(tmpDir, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: " + name + " description\n---\nBody"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	loader := NewFileSystemSkillLoader(tmpDir)
	skills, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(skills))
	}
}

func TestFileSystemSkillLoader_SanitizesName(t *testing.T) {
	tmpDir := t.TempDir()

	dir := filepath.Join(tmpDir, "My Skill")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: My Skill\ndescription: Skill with spaces\n---\nBody"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	loader := NewFileSystemSkillLoader(tmpDir)
	skills, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(skills) == 0 {
		t.Fatal("expected at least 1 skill")
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("expected sanitized name 'my-skill', got %q", skills[0].Name)
	}
}
