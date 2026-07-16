package tools

import (
	"reflect"
	"testing"
)

func TestSplitPipeSegments(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want []string
	}{
		{
			name: "无管道符",
			s:    "go build ./...",
			want: []string{"go build ./..."},
		},
		{
			name: "简单管道",
			s:    "go doc fmt | head -10",
			want: []string{"go doc fmt ", " head -10"},
		},
		{
			name: "多个管道",
			s:    "a | b | c",
			want: []string{"a ", " b ", " c"},
		},
		{
			name: "双引号内的管道符不分割",
			s:    `grep -i "type.*Value\|Value "`,
			want: []string{`grep -i "type.*Value\|Value "`},
		},
		{
			name: "单引号内的管道符不分割",
			s:    "grep -i 'hello|world'",
			want: []string{"grep -i 'hello|world'"},
		},
		{
			name: "引号内外的管道符混合",
			s:    `echo "a|b" | grep c`,
			want: []string{`echo "a|b" `, " grep c"},
		},
		{
			name: "空字符串",
			s:    "",
			want: []string{""},
		},
		{
			name: "只有管道符",
			s:    "|",
			want: []string{"", ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitPipeSegments(tt.s)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitPipeSegments(%q) = %#v, want %#v", tt.s, got, tt.want)
			}
		})
	}
}

func TestExtractCommands(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "空命令",
			command: "",
			want:    nil,
		},
		{
			name:    "单条命令",
			command: "ls -la",
			want:    []string{"ls"},
		},
		{
			name:    "&& 连接多条命令",
			command: "cd /tmp && git status",
			want:    []string{"cd", "git"},
		},
		{
			name:    "管道连接多条命令",
			command: "ps aux | grep go",
			want:    []string{"ps", "grep"},
		},
		{
			name:    "&& 和管道混合",
			command: "cd /tmp && go build ./... | head -10",
			want:    []string{"cd", "go", "head"},
		},
		{
			name:    "2>&1 重定向不影响命令提取",
			command: "go doc fmt 2>&1 | head -10",
			want:    []string{"go", "head"},
		},
		{
			name:    "上报的 bug：grep 模式中的 | 不应被当作管道分割",
			command: `cd /Users/workspace && go doc github.com/yalue/onnxruntime_go 2>&1 | grep -i "type.*Value\|Value " | head -10`,
			want:    []string{"cd", "go", "grep", "head"},
		},
		{
			name:    "for 循环中的命令",
			command: "for i in 1 2 3; do echo $i; done",
			want:    []string{"echo"},
		},
		{
			name:    "if 条件中的命令",
			command: "if [ -f file ]; then cat file; fi",
			want:    []string{"cat"},
		},
		{
			name:    "分号连接多条命令",
			command: "cd /tmp; ls -la; pwd",
			want:    []string{"cd", "ls", "pwd"},
		},
		{
			name:    "重复命令只出现一次",
			command: "ls -la && ls -R",
			want:    []string{"ls"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCommands(tt.command)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("extractCommands(%q) = %#v, want %#v", tt.command, got, tt.want)
			}
		})
	}
}

func TestExtractCommands_NoFalsePositiveValue(t *testing.T) {
	// 回归测试：确保 grep 模式中的 Value 不会被提取为命令名
	cmd := `cd /Users/workspace && go doc github.com/yalue/onnxruntime_go 2>&1 | grep -i "type.*Value\|Value " | head -10`
	cmds := extractCommands(cmd)

	for _, c := range cmds {
		if c == "Value" {
			t.Errorf("extractCommands(%q) 错误地将 VALUE 提取为命令名：%#v", cmd, cmds)
		}
	}

	// 验证白名单检查通过
	tool := NewBashTool().(*BashTool)
	allowed, failed := tool.isCommandWhitelisted(cmd)
	if !allowed {
		t.Errorf("isCommandWhitelisted(%q) 应该通过白名单检查，但失败于命令 %q", cmd, failed)
	}
}

func TestDetectDangerousCommand_SubshellNotBlocked(t *testing.T) {
	// 回归测试：$() 和反引号是正常的 shell 变量捕获，不应被危险命令拦截
	cmds := []string{
		`result=$(go run ./cmd/ 2>&1)`,
		`echo "$(echo hello | head -1)"`,
		"output=`ls -la`",
		`cd /tmp && for v in v1.0; do result=$(go run .) && echo "$result"; done`,
	}
	for _, cmd := range cmds {
		if blocked := detectDangerousCommand(cmd); blocked != "" {
			t.Errorf("detectDangerousCommand(%q) 不应阻止正常命令，但被阻止：%s", cmd, blocked)
		}
	}
}
