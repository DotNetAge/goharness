package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// newCaptureLogger 构造写入内存 buffer 的 SlogAdapter（Debug 级别全开），
// 用于实证各级别日志与属性顺序的真实输出。
func newCaptureLogger() (*SlogAdapter, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return NewSlogAdapter(slog.New(h)), &buf
}

// TestSlogAdapter_Levels 验证 Info/Warn/Debug 各级别均到达 handler 且键值对完整。
func TestSlogAdapter_Levels(t *testing.T) {
	l, buf := newCaptureLogger()

	l.Info("会话已创建", "session", "s-1")
	l.Warn("压缩跳过", "reason", "no-sink")
	l.Debug("工具注册", "name", "Bash")

	out := buf.String()
	for _, want := range []string{
		`level=INFO msg=会话已创建 session=s-1`,
		`level=WARN msg=压缩跳过 reason=no-sink`,
		`level=DEBUG msg=工具注册 name=Bash`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("日志输出缺少 %q，实际:\n%s", want, out)
		}
	}
}

// TestSlogAdapter_Error 验证 Error 的契约：err 为 nil 时不产生 error 属性，
// 非 nil 时以 "error" 键记录且排在调用方键值对之前。
func TestSlogAdapter_Error(t *testing.T) {
	l, buf := newCaptureLogger()

	sentinel := errors.New("kv 写入失败")
	l.Error("持久化白名单失败", sentinel, "path", "/tmp/session-wl.json")

	out := buf.String()
	if !strings.Contains(out, `error="kv 写入失败"`) {
		t.Errorf("输出应包含 error 属性，实际:\n%s", out)
	}
	errIdx := strings.Index(out, "error=")
	pathIdx := strings.Index(out, "path=")
	if errIdx < 0 || pathIdx < 0 || errIdx > pathIdx {
		t.Errorf("error 属性应先于调用方键值对，实际:\n%s", out)
	}

	buf.Reset()
	l.Error("忽略 nil 错误", nil, "op", "compact")
	if strings.Contains(buf.String(), "error=") {
		t.Errorf("err 为 nil 时不应产生 error 属性，实际:\n%s", buf.String())
	}
}

// TestNewSlogAdapter_NilFallback nil logger 应回退到 slog.Default()，调用不 panic。
func TestNewSlogAdapter_NilFallback(t *testing.T) {
	l := NewSlogAdapter(nil)
	if l == nil {
		t.Fatal("NewSlogAdapter(nil) 不应返回 nil")
	}
	l.Info("fallback ok") // 不 panic 即通过
}

// TestNopLogger NopLogger 丢弃全部消息且各方法可安全调用。
func TestNopLogger(t *testing.T) {
	l := NewNopLogger()
	if _, ok := l.(*NopLogger); !ok {
		t.Fatalf("NewNopLogger 应返回 *NopLogger，实际 %T", l)
	}
	l.Info("dropped", "k", "v")
	l.Warn("dropped")
	l.Debug("dropped")
	l.Error("dropped", errors.New("ignored"), "k", "v")
}
