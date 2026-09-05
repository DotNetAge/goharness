package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeModelsYAML 将内容写入临时 models.yml，返回文件路径与清理函数。
func writeModelsYAML(t *testing.T, content string) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "models.yml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写临时 models.yml 失败: %v", err)
	}
	return path, func() { _ = os.Remove(path) }
}

const collisionYAML = `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
  - name: zhipu
    base_url: https://open.bigmodel.cn/api/paas/v4
models:
  - name: gpt-4o
    provider: openai
    base_url: https://api.openai.com/v1
    context_length: 128000
  - name: gpt-4o
    provider: zhipu
    base_url: https://open.bigmodel.cn/api/paas/v4
    context_length: 128000
`

func TestLoadModels_CompositeKeyCollision(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)

		path, cleanup := writeModelsYAML(t, collisionYAML)
		defer cleanup()

		reg, err := LoadModels(path)
		if err != nil {
			t.Errorf("LoadModels 失败: %v", err)
			return
		}

		// 两个供应商同名模型应共存，而不是后者覆盖前者
		if got := reg.ListRaw(); len(got) != 2 {
			t.Errorf("ListRaw 数量应为 2，实际 %d", len(got))
			return
		}

		// 按裸名查询存在歧义，应返回 nil（由组合键精确寻址）
		if got := reg.Get("gpt-4o"); got != nil {
			t.Errorf("裸名 'gpt-4o' 存在歧义，应返回 nil，实际返回 provider=%s", got.Provider)
			return
		}

		// 按组合键 openai/gpt-4o 应命中 openai 实例
		oa := reg.Get("openai/gpt-4o")
		if oa == nil || oa.Provider != "openai" {
			t.Errorf("组合键 openai/gpt-4o 应命中 openai，实际 %v", oa)
			return
		}

		// 按组合键 zhipu/gpt-4o 应命中 zhipu 实例
		zp := reg.Get("zhipu/gpt-4o")
		if zp == nil || zp.Provider != "zhipu" {
			t.Errorf("组合键 zhipu/gpt-4o 应命中 zhipu，实际 %v", zp)
			return
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("测试执行超时")
	}
}

func TestLoadModels_UniqueNameBackwardCompat(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)

		path, cleanup := writeModelsYAML(t, `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
models:
  - name: gpt-4o
    provider: openai
    context_length: 128000
`)
		defer cleanup()

		reg, err := LoadModels(path)
		if err != nil {
			t.Errorf("LoadModels 失败: %v", err)
			return
		}

		// 无同名冲突时，裸名仍能唯一解析（向后兼容）
		got := reg.Get("gpt-4o")
		if got == nil || got.Provider != "openai" {
			t.Errorf("裸名 'gpt-4o' 应命中 openai，实际 %v", got)
			return
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("测试执行超时")
	}
}

func TestLoadModels_CompositeDeleteKeepsSibling(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)

		path, cleanup := writeModelsYAML(t, collisionYAML)
		defer cleanup()

		reg, err := LoadModels(path)
		if err != nil {
			t.Errorf("LoadModels 失败: %v", err)
			return
		}

		// 按组合键删除 openai/gpt-4o，应只移除 openai 实例，zhipu 实例保留
		if err := reg.Delete("openai/gpt-4o"); err != nil {
			t.Errorf("Delete 失败: %v", err)
			return
		}
		if got := reg.Get("openai/gpt-4o"); got != nil {
			t.Errorf("删除后 openai/gpt-4o 应不存在，实际 %v", got)
			return
		}
		if got := reg.Get("zhipu/gpt-4o"); got == nil || got.Provider != "zhipu" {
			t.Errorf("删除 openai 后 zhipu/gpt-4o 应保留，实际 %v", got)
			return
		}

		// 组合键注册应与已有同名模型共存，且不覆盖
		reg.Register("gpt-4o", &ModelConfig{Name: "gpt-4o", Provider: "openai", BaseURL: "https://new/v1"})
		if got := reg.Get("openai/gpt-4o"); got == nil || got.Provider != "openai" {
			t.Errorf("重注册 openai/gpt-4o 应命中新实例，实际 %v", got)
			return
		}
		if got := reg.Get("zhipu/gpt-4o"); got == nil {
			t.Errorf("重注册 openai 后 zhipu/gpt-4o 应保留，实际为 nil")
			return
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("测试执行超时")
	}
}
