package tools

import (
	"math/rand"
	"strings"
	"testing"
)

// 语义取自 stealth_config.go：
//   - 缺省：profile=auto，timeout=15，maxRetries=3
//   - 非法整数回退默认值；timeout<=0 / maxRetries<1 由兜底逻辑拉回
//   - 布尔开关只认 1/true/yes（大小写不敏感）

// TestLoadStealthConfig_Defaults 未设置任何环境变量时的默认配置。
func TestLoadStealthConfig_Defaults(t *testing.T) {
	cfg := LoadStealthConfig()
	if cfg.Disabled {
		t.Error("默认应启用隐蔽后端（Disabled=false）")
	}
	if cfg.ProfileName != "auto" {
		t.Errorf("默认 ProfileName 应为 auto，实际 %q", cfg.ProfileName)
	}
	if cfg.TimeoutSec != 15 {
		t.Errorf("默认 TimeoutSec 应为 15，实际 %d", cfg.TimeoutSec)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("默认 MaxRetries 应为 3，实际 %d", cfg.MaxRetries)
	}
	if cfg.DisableHTTP3 {
		t.Error("默认应启用 HTTP3（DisableHTTP3=false）")
	}
}

// TestLoadStealthConfig_EnvOverride 环境变量覆盖各字段。
func TestLoadStealthConfig_EnvOverride(t *testing.T) {
	t.Setenv("GOHARNESS_STEALTH_PROFILE", "chrome_144")
	t.Setenv("GOHARNESS_STEALTH_TIMEOUT", "60")
	t.Setenv("GOHARNESS_STEALTH_MAX_RETRIES", "5")
	t.Setenv("GOHARNESS_STEALTH_DISABLE", "1")
	t.Setenv("GOHARNESS_STEALTH_DISABLE_HTTP3", "yes")

	cfg := LoadStealthConfig()
	if cfg.ProfileName != "chrome_144" {
		t.Errorf("ProfileName = %q，期望 chrome_144", cfg.ProfileName)
	}
	if cfg.TimeoutSec != 60 {
		t.Errorf("TimeoutSec = %d，期望 60", cfg.TimeoutSec)
	}
	if cfg.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d，期望 5", cfg.MaxRetries)
	}
	if !cfg.Disabled {
		t.Error("GOHARNESS_STEALTH_DISABLE=1 应置 Disabled=true")
	}
	if !cfg.DisableHTTP3 {
		t.Error("GOHARNESS_STEALTH_DISABLE_HTTP3=yes 应置 DisableHTTP3=true")
	}
}

// TestLoadStealthConfig_InvalidAndBoundary 非法值回退与越界兜底。
func TestLoadStealthConfig_InvalidAndBoundary(t *testing.T) {
	// 非法整数回退默认
	t.Setenv("GOHARNESS_STEALTH_TIMEOUT", "not-a-number")
	t.Setenv("GOHARNESS_STEALTH_MAX_RETRIES", "abc")
	cfg := LoadStealthConfig()
	if cfg.TimeoutSec != 15 {
		t.Errorf("非法超时应回退 15，实际 %d", cfg.TimeoutSec)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("非法重试次数应回退 3，实际 %d", cfg.MaxRetries)
	}

	// timeout<=0 / maxRetries<1 由兜底拉回
	t.Setenv("GOHARNESS_STEALTH_TIMEOUT", "0")
	t.Setenv("GOHARNESS_STEALTH_MAX_RETRIES", "-1")
	cfg = LoadStealthConfig()
	if cfg.TimeoutSec != 15 {
		t.Errorf("TimeoutSec<=0 应兜底为 15，实际 %d", cfg.TimeoutSec)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries<1 应兜底为 3，实际 %d", cfg.MaxRetries)
	}

	// 布尔值只认 1/true/yes，其他（含 0）为假
	t.Setenv("GOHARNESS_STEALTH_DISABLE", "0")
	if LoadStealthConfig().Disabled {
		t.Error("DISABLE=0 不应禁用")
	}
	t.Setenv("GOHARNESS_STEALTH_DISABLE", "TRUE")
	if !LoadStealthConfig().Disabled {
		t.Error("DISABLE=TRUE（大写）应禁用")
	}
}

// TestPickUAProfile_NameNormalization 已有 stealth_client_test.go 覆盖 auto/fixed/未知回退；
// 此处补充其未覆盖的归一化语义：profile 名会先 TrimSpace + ToLower 再匹配，
// "  CHROME_146  " 应命中 chrome_146 的固定配对而非落入 auto 池。
func TestPickUAProfile_NameNormalization(t *testing.T) {
	cfg := StealthConfig{ProfileName: "  CHROME_146  "}
	pair := pickUAProfile(cfg, rand.New(rand.NewSource(42)))
	if !strings.Contains(pair.UA, "Chrome/146.0.0.0") {
		t.Errorf("带空白与大写的 profile 名应命中 chrome_146 配对，实际 %q", pair.UA)
	}
}
