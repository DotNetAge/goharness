package session

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestMessage_ImageSerialization 验证带图片的消息可完整序列化/反序列化（yaml 与 json）。
func TestMessage_ImageSerialization(t *testing.T) {
	msg := Message{
		Role:      "user",
		Content:   "请分析这张图",
		Timestamp: 1700000000,
		Images: []ImageBlock{
			{MediaType: "image/png", Base64Data: "aGVsbG8=", AltText: "512x300"},
		},
	}

	t.Run("yaml 往返", func(t *testing.T) {
		data, err := yaml.Marshal(msg)
		if err != nil {
			t.Fatalf("yaml 序列化失败: %v", err)
		}
		if !strings.Contains(string(data), "aGVsbG8=") {
			t.Error("yaml 序列化应包含图片 base64 数据")
		}
		var back Message
		if err := yaml.Unmarshal(data, &back); err != nil {
			t.Fatalf("yaml 反序列化失败: %v", err)
		}
		if len(back.Images) != 1 {
			t.Fatalf("期望 1 个图片块，得到 %d", len(back.Images))
		}
		if back.Images[0].MediaType != "image/png" || back.Images[0].Base64Data != "aGVsbG8=" {
			t.Errorf("图片块往返不一致: %+v", back.Images[0])
		}
	})

	t.Run("json 往返", func(t *testing.T) {
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("json 序列化失败: %v", err)
		}
		var back Message
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("json 反序列化失败: %v", err)
		}
		if len(back.Images) != 1 || back.Images[0].Base64Data != "aGVsbG8=" {
			t.Errorf("json 图片块往返不一致: %+v", back.Images)
		}
	})
}

// TestMessage_ImageOmitEmpty 验证无图片的旧消息序列化时不包含 images 字段（向后兼容）。
func TestMessage_ImageOmitEmpty(t *testing.T) {
	msg := Message{Role: "tool", Content: "普通结果", Timestamp: 1700000000}

	yamlData, err := yaml.Marshal(msg)
	if err != nil {
		t.Fatalf("yaml 序列化失败: %v", err)
	}
	if strings.Contains(string(yamlData), "images") {
		t.Error("无图片时 yaml 不应包含 images 字段")
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("json 序列化失败: %v", err)
	}
	if strings.Contains(string(jsonData), "images") {
		t.Error("无图片时 json 不应包含 images 字段")
	}
}
