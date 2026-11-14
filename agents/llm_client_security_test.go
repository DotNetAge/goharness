package agents

import (
	"strings"
	"testing"
)

// 回归：401 排障信息只能出现脱敏后的密钥与来源，不得包含完整值。
func TestAuthDebugContext_MasksAPIKey(t *testing.T) {
	const key = "sk-verify-secret"
	c := &gochatLLMClient{apiKey: key, baseURL: "https://api.example.com", provider: "deepseek"}
	info := c.authDebugContext()

	if strings.Contains(info, key) {
		t.Fatalf("排障信息包含完整 API Key: %q", info)
	}
	if !strings.Contains(info, "***") {
		t.Fatalf("未输出脱敏标记: %q", info)
	}
	if !strings.Contains(info, "key_source=") {
		t.Fatalf("未输出密钥来源: %q", info)
	}
}

func TestMaskSecret(t *testing.T) {
	if got := maskSecret("short"); got != "***" {
		t.Fatalf("短密钥应全隐去: %q", got)
	}
	if got := maskSecret("sk-1234567890abcd"); got != "sk-1***abcd" {
		t.Fatalf("脱敏格式不符: %q", got)
	}
}
