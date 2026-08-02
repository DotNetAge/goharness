package tools

import (
	"math/rand"
	"os"
	"strconv"
	"strings"

	"github.com/bogdanfinn/tls-client/profiles"
)

// StealthConfig 持有从环境变量解析的隐蔽客户端全局配置。
// 所有 GOHARNESS_STEALTH_* 环境变量在此统一解析，零配置时默认启用 tls-client 隐蔽后端。
type StealthConfig struct {
	Disabled     bool   // GOHARNESS_STEALTH_DISABLE=1 时退回标准库后端（排障用）
	ProfileName  string // GOHARNESS_STEALTH_PROFILE：auto|chrome_146|chrome_144|firefox_147|safari_18
	TimeoutSec   int    // GOHARNESS_STEALTH_TIMEOUT，单次请求超时秒（不含重试退避）
	MaxRetries   int    // GOHARNESS_STEALTH_MAX_RETRIES，最大尝试次数（含首次）
	DisableHTTP3 bool   // GOHARNESS_STEALTH_DISABLE_HTTP3，禁用 QUIC（UDP 被封时用）
}

// LoadStealthConfig 从环境变量读取隐蔽客户端配置，缺失项用默认值。
// 进程内每次创建客户端读一次，无需缓存（开销可忽略）。
func LoadStealthConfig() StealthConfig {
	cfg := StealthConfig{
		ProfileName: stealthEnvStr("GOHARNESS_STEALTH_PROFILE", "auto"),
		TimeoutSec:  stealthEnvInt("GOHARNESS_STEALTH_TIMEOUT", 15),
		MaxRetries:  stealthEnvInt("GOHARNESS_STEALTH_MAX_RETRIES", 3),
	}
	cfg.Disabled = stealthEnvBool("GOHARNESS_STEALTH_DISABLE")
	cfg.DisableHTTP3 = stealthEnvBool("GOHARNESS_STEALTH_DISABLE_HTTP3")
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 15
	}
	if cfg.MaxRetries < 1 {
		cfg.MaxRetries = 3
	}
	return cfg
}

// uaProfilePair 把 UA 字符串与 tls-client ClientProfile 绑定，保证 TLS 指纹与 UA 一致。
// 真实浏览器会话内 UA 不会跳变，故 stealthClient 生命周期内固定一个配对，
// 比每请求随机轮换 UA 更隐蔽（同会话 UA 跳变反而是爬虫特征）。
type uaProfilePair struct {
	UA      string
	Profile profiles.ClientProfile
}

// uaProfilePool 是 auto 模式的随机配对池，统一用 Chrome 146（2026 主流稳定版，
// 市占率最高、最不 suspicious），覆盖三大桌面平台。
// 替换 web_search.go 旧 uaPool（Chrome 131 等过时版本）。
var uaProfilePool = []uaProfilePair{
	{
		UA:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		Profile: profiles.Chrome_146,
	},
	{
		UA:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		Profile: profiles.Chrome_146,
	},
	{
		UA:      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		Profile: profiles.Chrome_146,
	},
}

// fixedProfiles 是 GOHARNESS_STEALTH_PROFILE 强制指定时的配对，UA 版本与 profile 严格对应。
var fixedProfiles = map[string]uaProfilePair{
	"chrome_146": {
		UA:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		Profile: profiles.Chrome_146,
	},
	"chrome_144": {
		UA:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
		Profile: profiles.Chrome_144,
	},
	"firefox_147": {
		UA:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:147.0) Gecko/20100101 Firefox/147.0",
		Profile: profiles.Firefox_147,
	},
	"safari_18": {
		UA:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15",
		Profile: profiles.Safari_16_0,
	},
}

// pickUAProfile 按配置选择 UA+profile 配对。
// auto：从 uaProfilePool 随机（Chrome 146 三平台）；强制 profile 名则取 fixedProfiles 对应配对；
// 未知 profile 名回退 auto，保证永不返回零值。
func pickUAProfile(cfg StealthConfig, rng *rand.Rand) uaProfilePair {
	name := strings.ToLower(strings.TrimSpace(cfg.ProfileName))
	if name == "" || name == "auto" {
		return uaProfilePool[rng.Intn(len(uaProfilePool))]
	}
	if pair, ok := fixedProfiles[name]; ok {
		return pair
	}
	return uaProfilePool[rng.Intn(len(uaProfilePool))]
}

// stealthEnvStr 读取字符串环境变量，缺失返回默认值。
func stealthEnvStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// stealthEnvInt 读取整型环境变量，缺失或非法返回默认值。
func stealthEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// stealthEnvBool 读取布尔环境变量，1/true/yes 视为真。
func stealthEnvBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes"
}
