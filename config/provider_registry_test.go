package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// providerYAML 沿用 models.yml 真实结构：provider 层统管连接参数，
// model 层只写 name/provider/context_length，连接参数靠继承。
const providerYAML = `
providers:
  - name: zhipu
    base_url: https://open.bigmodel.cn/api/paas/v4
    api_key: provider-level-key
models:
  - name: glm-4.7
    provider: zhipu
    context_length: 128000
`

// TestModelRegistry_ProviderCRUD 验证 RegisterProvider/GetProvider/Providers：
// 含「provider.Name 为空时用注册名补齐」与「同名覆盖」两个契约。
func TestModelRegistry_ProviderCRUD(t *testing.T) {
	reg := &ModelRegistry{}

	reg.RegisterProvider("zhipu", &ProviderConfig{
		BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		APIKey:  "provider-level-key",
	})
	got := reg.GetProvider("zhipu")
	if got == nil {
		t.Fatal("GetProvider(zhipu) 应命中")
	}
	if got.Name != "zhipu" {
		t.Errorf("未显式命名时应用注册名补齐，实际 Name=%q", got.Name)
	}
	if got.BaseURL != "https://open.bigmodel.cn/api/paas/v4" || got.APIKey != "provider-level-key" {
		t.Errorf("BaseURL/APIKey 不匹配: %+v", got)
	}

	// 同名覆盖
	reg.RegisterProvider("zhipu", &ProviderConfig{Name: "zhipu", BaseURL: "https://backup.example.com/v4"})
	if got := reg.GetProvider("zhipu"); got.BaseURL != "https://backup.example.com/v4" {
		t.Errorf("同名注册应覆盖，实际 BaseURL=%q", got.BaseURL)
	}

	// 未注册返回 nil
	if reg.GetProvider("openai") != nil {
		t.Error("未注册的 provider 应返回 nil")
	}

	// Providers 返回全部条目（map 无序，按集合断言）
	ps := reg.Providers()
	if len(ps) != 1 {
		t.Fatalf("Providers 应返回 1 条，实际 %d", len(ps))
	}
	found := false
	for _, p := range ps {
		if p.Name == "zhipu" {
			found = true
		}
	}
	if !found {
		t.Error("Providers 未包含 zhipu")
	}
}

// TestModelRegistry_ProviderRegistryAdapter 验证 ProviderRegistry 视图适配器：
// 只读契约——Get 命中/未命中语义、Register 被拒绝、List/Size 与注册表一致。
func TestModelRegistry_ProviderRegistryAdapter(t *testing.T) {
	path, cleanup := writeModelsYAML(t, providerYAML)
	defer cleanup()
	reg, err := LoadModels(path)
	if err != nil {
		t.Fatalf("LoadModels 失败: %v", err)
	}

	var pr ProviderRegistry = reg.ProviderRegistry()

	// Get 命中
	p, err := pr.Get("zhipu")
	if err != nil {
		t.Fatalf("Get(zhipu) 失败: %v", err)
	}
	if p.BaseURL != "https://open.bigmodel.cn/api/paas/v4" {
		t.Errorf("Get 返回的 BaseURL 不匹配: %q", p.BaseURL)
	}

	// Get 未命中 → ErrProviderNotFound
	if _, err := pr.Get("openai"); !errors.Is(err, ErrProviderNotFound) {
		t.Errorf("未命中应返回 ErrProviderNotFound，实际 %v", err)
	}

	// Register 属于视图层只读能力，必须被拒绝
	if err := pr.Register("openai", &ProviderConfig{BaseURL: "https://api.openai.com/v1"}); err == nil {
		t.Error("适配器 Register 应返回错误（视图层不可写）")
	}

	// List/Size 与加载的配置一致
	names := pr.List()
	if len(names) != 1 || names[0] != "zhipu" {
		t.Errorf("List = %v，期望 [zhipu]", names)
	}
	if got := pr.Size(); got != 1 {
		t.Errorf("Size = %d，期望 1", got)
	}
}

// TestModelRegistry_GetRawVsGetInheritance 验证 Get 与 GetRaw 的继承语义差异：
// Get 返回从 provider 继承了 BaseURL/APIKey 的解析副本，
// GetRaw 返回原始配置（连接参数为空），且原始配置不被污染。
func TestModelRegistry_GetRawVsGetInheritance(t *testing.T) {
	path, cleanup := writeModelsYAML(t, providerYAML)
	defer cleanup()
	reg, err := LoadModels(path)
	if err != nil {
		t.Fatalf("LoadModels 失败: %v", err)
	}

	raw := reg.GetRaw("glm-4.7")
	if raw == nil {
		t.Fatal("GetRaw(glm-4.7) 应命中")
	}
	if raw.BaseURL != "" || raw.APIKey != "" {
		t.Errorf("GetRaw 应返回未继承的原始配置，实际 BaseURL=%q APIKey=%q", raw.BaseURL, raw.APIKey)
	}

	resolved := reg.Get("glm-4.7")
	if resolved == nil {
		t.Fatal("Get(glm-4.7) 应命中")
	}
	if resolved.BaseURL != "https://open.bigmodel.cn/api/paas/v4" {
		t.Errorf("Get 应继承 provider 的 BaseURL，实际 %q", resolved.BaseURL)
	}
	if resolved.APIKey != "provider-level-key" {
		t.Errorf("Get 应继承 provider 的 APIKey，实际 %q", resolved.APIKey)
	}

	// 解析副本不得污染原始配置
	if reg.GetRaw("glm-4.7").APIKey != "" {
		t.Error("Get 的继承结果不应写回注册表原始配置")
	}
}

// TestModelConfig_ResolveProvider 验证 ModelConfig.ResolveProvider：
// 只填空缺字段，模型自带的值不被 provider 覆盖；
// provider 不存在 / registry 为 nil / Provider 为空时原样返回。
func TestModelConfig_ResolveProvider(t *testing.T) {
	registry := &ModelRegistry{}
	registry.RegisterProvider("zhipu", &ProviderConfig{
		Name:    "zhipu",
		BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		APIKey:  "provider-level-key",
	})

	// 模型自带 BaseURL：只继承 APIKey，不覆盖已有值
	m := &ModelConfig{Name: "glm-4.7", Provider: "zhipu", BaseURL: "https://custom.example.com/v4"}
	r := m.ResolveProvider(registry.ProviderRegistry())
	if r.BaseURL != "https://custom.example.com/v4" {
		t.Errorf("模型自带的 BaseURL 不应被覆盖，实际 %q", r.BaseURL)
	}
	if r.APIKey != "provider-level-key" {
		t.Errorf("空缺的 APIKey 应从 provider 继承，实际 %q", r.APIKey)
	}
	if r == m {
		t.Error("发生了继承时应返回副本而非原指针")
	}

	// provider 不存在 → 原样返回
	missing := &ModelConfig{Name: "m1", Provider: "openai"}
	if got := missing.ResolveProvider(registry.ProviderRegistry()); got != missing {
		t.Error("provider 不存在时应原样返回原配置指针")
	}

	// registry 为 nil → 原样返回
	if got := m.ResolveProvider(nil); got != m {
		t.Error("registry 为 nil 时应原样返回原配置指针")
	}

	// Provider 为空 → 原样返回
	noProvider := &ModelConfig{Name: "local-model"}
	if got := noProvider.ResolveProvider(registry.ProviderRegistry()); got != noProvider {
		t.Error("Provider 为空时应原样返回原配置指针")
	}
}

// TestModelRegistry_SaveRoundTrip 验证 Save 的持久化契约：
// 内存注册 + 落盘 models.yml 后，用 LoadModels 重新加载能读回修改；
// 空名模型被拒绝。
func TestModelRegistry_SaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.yml")
	if err := os.WriteFile(path, []byte(providerYAML), 0644); err != nil {
		t.Fatalf("写初始 models.yml 失败: %v", err)
	}
	reg, err := LoadModels(path)
	if err != nil {
		t.Fatalf("LoadModels 失败: %v", err)
	}

	// 空名拒绝
	if err := reg.Save(&ModelConfig{Provider: "zhipu"}); err == nil {
		t.Error("空名模型应被 Save 拒绝")
	}

	// 新增模型并落盘
	newModel := &ModelConfig{Name: "glm-4.7-air", Provider: "zhipu", ContextLength: 32000}
	if err := reg.Save(newModel); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	// 重新加载读回
	reg2, err := LoadModels(path)
	if err != nil {
		t.Fatalf("重新 LoadModels 失败: %v", err)
	}
	back := reg2.Get("zhipu/glm-4.7-air")
	if back == nil {
		t.Fatal("Save 后重新加载应能读到新模型")
	}
	if back.ContextLength != 32000 {
		t.Errorf("ContextLength 不匹配: %d", back.ContextLength)
	}
}
