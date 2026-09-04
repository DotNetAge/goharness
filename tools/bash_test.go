package tools

import (
	"reflect"
	"testing"
)

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
		{
			name:    "which && python -m 带引号代码",
			command: `which python && python -m "print('hello world')"`,
			want:    []string{"which", "python"},
		},
		{
			name:    "cp 带引号路径 && cd && ls",
			command: `cp "/tmp/src" "/tmp/dst" && cd /tmp/dst && ls .`,
			want:    []string{"cp", "cd", "ls"},
		},
		{
			name:    "node 带引号复杂代码",
			command: `node "console.log('hello'); process.exit(0)"`,
			want:    []string{"node"},
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
}

func TestExtractCommands_EdgeCases(t *testing.T) {
	// 极端情况测试：确保带引号字符串、路径的连续指令能正确解析
	edgeCases := []string{
		`which python && python -m "print('hello world')"`,
		`cp "/tmp/src" "/tmp/dst" && cd /tmp/dst && ls .`,
		`node "console.log('hello'); process.exit(0)"`,
	}
	for _, cmd := range edgeCases {
		// 验证 extractCommands 不会错误提取引号内的内容作为命令名
		extracted := extractCommands(cmd)
		for _, c := range extracted {
			if c == "print" || c == "hello" || c == "console" || c == "process" || c == "exit" {
				t.Errorf("extractCommands(%q) 错误地将引号内的代码提取为命令名：%#v", cmd, extracted)
			}
		}
	}
}

func TestBashGrant_SubshellNotBlocked(t *testing.T) {
	// 回归测试：$() 和反引号是正常的 shell 变量捕获，不应被沙箱危险模式拦截。
	// 命令安全决策统一由沙箱 CheckCommand 负责（工具自身不做授权检查）。
	cmds := []string{
		`result=$(go run ./cmd/ 2>&1)`,
		`echo "$(echo hello | head -1)"`,
		"output=`ls -la`",
		`cd /tmp && for v in v1.0; do result=$(go run .) && echo "$result"; done`,
	}
	for _, cmd := range cmds {
		granted, reason := NewBashTool().(*BashTool).Grant(newSandboxCtx(t, t.TempDir(), nil), map[string]any{"command": cmd})
		if !granted {
			t.Errorf("Grant(%q) 不应被沙箱拦截，但被拒绝：%s", cmd, reason)
		}
	}
}

func TestGenerateKeyVariants(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want []string
	}{
		{
			name: "多词组键名",
			key:  "working_dir",
			want: []string{
				"WORKING_DIR",
				"workingdir",
				"WORKINGDIR",
				"working-dir",
				"WORKING-DIR",
				"WorkingDir",
				"workingDir",
			},
		},
		{
			name: "三词组键名",
			key:  "add_blocked_by",
			want: []string{
				"ADD_BLOCKED_BY",
				"addblockedby",
				"ADDBLOCKEDBY",
				"add-blocked-by",
				"ADD-BLOCKED-BY",
				"AddBlockedBy",
				"addBlockedBy",
			},
		},
		{
			name: "单词组键名",
			key:  "command",
			want: []string{
				"COMMAND",
				"Command",
			},
		},
		{
			name: "无下划线单词组",
			key:  "timeout",
			want: []string{
				"TIMEOUT",
				"Timeout",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateKeyVariants(tt.key)
			for _, want := range tt.want {
				found := false
				for _, g := range got {
					if g == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("GenerateKeyVariants(%q) 缺少变体 %q，结果：%#v", tt.key, want, got)
				}
			}
		})
	}
}

func TestGetParam(t *testing.T) {
	tests := []struct {
		name    string
		params  map[string]any
		key     string
		wantVal any
		wantOK  bool
	}{
		{
			name:    "nil参数映射",
			params:  nil,
			key:     "working_dir",
			wantVal: nil,
			wantOK:  false,
		},
		{
			name:    "空参数映射",
			params:  map[string]any{},
			key:     "working_dir",
			wantVal: nil,
			wantOK:  false,
		},
		{
			name:    "精确匹配 working_dir",
			params:  map[string]any{"working_dir": "/tmp"},
			key:     "working_dir",
			wantVal: "/tmp",
			wantOK:  true,
		},
		{
			name:    "全大写+下划线 WORKING_DIR",
			params:  map[string]any{"WORKING_DIR": "/tmp"},
			key:     "working_dir",
			wantVal: "/tmp",
			wantOK:  true,
		},
		{
			name:    "全小写无分隔符 workingdir",
			params:  map[string]any{"workingdir": "/tmp"},
			key:     "working_dir",
			wantVal: "/tmp",
			wantOK:  true,
		},
		{
			name:    "全大写无分隔符 WORKINGDIR",
			params:  map[string]any{"WORKINGDIR": "/tmp"},
			key:     "working_dir",
			wantVal: "/tmp",
			wantOK:  true,
		},
		{
			name:    "小写+连字符 working-dir",
			params:  map[string]any{"working-dir": "/tmp"},
			key:     "working_dir",
			wantVal: "/tmp",
			wantOK:  true,
		},
		{
			name:    "大写+连字符 WORKING-DIR",
			params:  map[string]any{"WORKING-DIR": "/tmp"},
			key:     "working_dir",
			wantVal: "/tmp",
			wantOK:  true,
		},
		{
			name:    "大驼峰 WorkingDir",
			params:  map[string]any{"WorkingDir": "/tmp"},
			key:     "working_dir",
			wantVal: "/tmp",
			wantOK:  true,
		},
		{
			name:    "小驼峰 workingDir",
			params:  map[string]any{"workingDir": "/tmp"},
			key:     "working_dir",
			wantVal: "/tmp",
			wantOK:  true,
		},
		{
			name:    "单词组精确匹配 command",
			params:  map[string]any{"command": "ls"},
			key:     "command",
			wantVal: "ls",
			wantOK:  true,
		},
		{
			name:    "单词组全大写 COMMAND",
			params:  map[string]any{"COMMAND": "ls"},
			key:     "command",
			wantVal: "ls",
			wantOK:  true,
		},
		{
			name:    "单词组大驼峰 Command",
			params:  map[string]any{"Command": "ls"},
			key:     "command",
			wantVal: "ls",
			wantOK:  true,
		},
		{
			name:    "三词组小驼峰 addBlockedBy",
			params:  map[string]any{"addBlockedBy": []any{"id1"}},
			key:     "add_blocked_by",
			wantVal: []any{"id1"},
			wantOK:  true,
		},
		{
			name:    "三词组 PascalCase AddBlockedBy",
			params:  map[string]any{"AddBlockedBy": []any{"id1"}},
			key:     "add_blocked_by",
			wantVal: []any{"id1"},
			wantOK:  true,
		},
		{
			name:    "匹配 false 值",
			params:  map[string]any{"recursive": false},
			key:     "recursive",
			wantVal: false,
			wantOK:  true,
		},
		{
			name:    "匹配 0 值",
			params:  map[string]any{"timeout": float64(0)},
			key:     "timeout",
			wantVal: float64(0),
			wantOK:  true,
		},
		{
			name:    "匹配空字符串",
			params:  map[string]any{"working_dir": ""},
			key:     "working_dir",
			wantVal: "",
			wantOK:  true,
		},
		{
			name:    "精确匹配优先于变体",
			params:  map[string]any{"working_dir": "/exact", "WORKING_DIR": "/caps"},
			key:     "working_dir",
			wantVal: "/exact",
			wantOK:  true,
		},
		{
			name:    "未匹配到任何变体",
			params:  map[string]any{"unrelated": "value"},
			key:     "working_dir",
			wantVal: nil,
			wantOK:  false,
		},
		{
			name:    "键完全不存在",
			params:  map[string]any{"command": "ls"},
			key:     "working_dir",
			wantVal: nil,
			wantOK:  false,
		},
		{
			name:    "string 类型值",
			params:  map[string]any{"max_results": "10"},
			key:     "max_results",
			wantVal: "10",
			wantOK:  true,
		},
		{
			name:    "float64 类型值",
			params:  map[string]any{"duration_ms": float64(5000)},
			key:     "duration_ms",
			wantVal: float64(5000),
			wantOK:  true,
		},
		{
			name:    "slice 类型值",
			params:  map[string]any{"allowed_domains": []any{"example.com"}},
			key:     "allowed_domains",
			wantVal: []any{"example.com"},
			wantOK:  true,
		},
		{
			name:    "map 类型值",
			params:  map[string]any{"metadata": map[string]any{"key": "val"}},
			key:     "metadata",
			wantVal: map[string]any{"key": "val"},
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := GetParam(tt.params, tt.key)
			if ok != tt.wantOK {
				t.Errorf("GetParam(%q) ok = %v, wantOK %v", tt.key, ok, tt.wantOK)
			}
			if !reflect.DeepEqual(got, tt.wantVal) {
				t.Errorf("GetParam(%q) = %v (%T), want %v (%T)", tt.key, got, got, tt.wantVal, tt.wantVal)
			}
		})
	}
}
