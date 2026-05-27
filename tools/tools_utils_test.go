package tools

import (
	"testing"
)

func TestValidateRequired(t *testing.T) {
	tests := []struct {
		name    string
		params  map[string]any
		key     string
		wantErr bool
	}{
		{"key exists", map[string]any{"foo": "bar"}, "foo", false},
		{"key missing", map[string]any{}, "foo", true},
		{"nil value", map[string]any{"foo": nil}, "foo", false},
		{"empty string is valid", map[string]any{"foo": ""}, "foo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRequired(tt.params, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRequired() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRequiredString(t *testing.T) {
	tests := []struct {
		name    string
		params  map[string]any
		key     string
		wantVal string
		wantErr bool
	}{
		{"valid string", map[string]any{"cmd": "echo hi"}, "cmd", "echo hi", false},
		{"missing key", map[string]any{}, "cmd", "", true},
		{"wrong type int", map[string]any{"cmd": 42}, "cmd", "", true},
		{"wrong type bool", map[string]any{"cmd": true}, "cmd", "", true},
		{"empty string ok", map[string]any{"cmd": ""}, "cmd", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateRequiredString(tt.params, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRequiredString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.wantVal {
				t.Errorf("ValidateRequiredString() = %q, want %q", got, tt.wantVal)
			}
		})
	}
}

func TestValidateFileSafety(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"current dir safe", ".", false},
		{"subdir safe", "./test_data", false},
		{"parent traversal blocked", "../etc/passwd", true},
		{"absolute path outside cwd blocked", "/etc/hosts", true},
		{"double parent blocked", "../../tmp/test", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFileSafety(tt.path, "")
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateFileSafety(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestTruncateStringEdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{"short unchanged", "hello world", 20, "hello world"},
		{"exact length", "hello", 5, "hello"},
		{"truncate ascii", "hello world", 7, "hell..."},
		{"truncate unicode", "你好世界测试", 4, "你..."},
		{"maxLen=3 edge case", "abcdef", 3, "abc"},
		{"empty string", "", 10, ""},
		{"maxLen=0 returns empty", "abc", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateString(tt.s, tt.maxLen)
			if got != tt.want {
				t.Errorf("TruncateString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name   string
		v      any
		want   float64
		wantOk bool
	}{
		{"float64", float64(3.14), 3.14, true},
		{"float32", float32(2.71), 2.71, true},
		{"int", int(42), 42.0, true},
		{"int64", int64(100), 100.0, true},
		{"int32", int32(50), 50.0, true},
		{"string", "hello", 0, false},
		{"nil", nil, 0, false},
		{"bool", true, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotOk := ToFloat64(tt.v)
			if gotOk != tt.wantOk {
				t.Errorf("ToFloat64() ok = %v, want %v", gotOk, tt.wantOk)
				return
			}
			if gotOk && (got < tt.want-0.001 || got > tt.want+0.001) {
				t.Errorf("ToFloat64() = %v, want ~%v", got, tt.want)
			}
		})
	}
}
