package goreact

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/DotNetAge/goreact/core"
)

func TestDebug_AgentHTTPRequestBody(t *testing.T) {
	var (
		requestBody string
		mu          sync.Mutex
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		mu.Lock()
		requestBody = string(body)
		mu.Unlock()

		var prettyJSON map[string]any
		json.Unmarshal(body, &prettyJSON)
		formatted, _ := json.MarshalIndent(prettyJSON, "", "  ")

		output := fmt.Sprintf("URL: %s %s\n\n%s\n", r.Method, r.URL.String(), string(formatted))
		os.WriteFile("./request_body.json", []byte(output), 0644)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	agent, err := NewAgent(
		WithConfig(&core.AgentConfig{
			Name:         "test-agent",
			Role:         "assistant",
			Description:  "A test assistant",
			Introduction: "You are a helpful assistant.",
		}),
		WithModel(&core.ModelConfig{
			Name:      "test-model",
			BaseURL:   server.URL,
			APIKey:    "sk-test",
			MaxTokens: 4096,
			Enabled:   true,
		}),
	)
	if err != nil {
		t.Fatalf("NewAgent failed: %v", err)
	}

	agent.Ask("test-session", "帮我写一个客户程序")

	mu.Lock()
	body := requestBody
	mu.Unlock()

	if body == "" {
		t.Fatal("No request was captured")
	}
	t.Logf("Full request body saved to ./request_body.json (%d bytes)", len(body))
}
