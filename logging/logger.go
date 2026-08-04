// Package logging 提供结构化日志接口和 slog 适配器。
// 它定义了一个可由任意日志后端实现的 Logger 接口，
// 并内置了 Go 标准库 log/slog 包的适配器。
package logging

import "log/slog"

// Logger 定义结构化日志操作的接口。
// 实现应支持以下日志级别：Info、Error、Debug、Warn。
type Logger interface {
	// Info 记录一条信息级别的消息，可附带可选的键值对。
	Info(msg string, keyvals ...any)
	// Error 记录一条错误消息，包含错误对象和可选的键值对。
	Error(msg string, err error, keyvals ...any)
	// Debug 记录一条调试消息，可附带可选的键值对。
	Debug(msg string, keyvals ...any)
	// Warn 记录一条警告消息，可附带可选的键值对。
	Warn(msg string, keyvals ...any)
}

// SlogAdapter 使用 Go 标准库 log/slog 包实现 Logger。
type SlogAdapter struct {
	logger *slog.Logger
}

// NewSlogAdapter 创建一个新的 SlogAdapter，包装给定的 slog.Logger。
// 若 logger 为 nil，则使用 slog.Default()。
func NewSlogAdapter(logger *slog.Logger) *SlogAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogAdapter{logger: logger}
}

// DefaultLogger 返回使用默认 slog.Logger 的 Logger。
func DefaultLogger() Logger {
	return &SlogAdapter{logger: slog.Default()}
}

// Info 在 Info 级别记录一条信息消息。
func (l *SlogAdapter) Info(msg string, keyvals ...any) {
	l.logger.Info(msg, keyvals...)
}

// Error 在 Error 级别记录一条错误消息，并包含错误值。
func (l *SlogAdapter) Error(msg string, err error, keyvals ...any) {
	args := make([]any, 0, 2+len(keyvals))
	if err != nil {
		args = append(args, "error", err)
	}
	args = append(args, keyvals...)
	l.logger.Error(msg, args...)
}

// Debug 在 Debug 级别记录一条调试消息。
func (l *SlogAdapter) Debug(msg string, keyvals ...any) {
	l.logger.Debug(msg, keyvals...)
}

// Warn 在 Warn 级别记录一条警告消息。
func (l *SlogAdapter) Warn(msg string, keyvals ...any) {
	l.logger.Warn(msg, keyvals...)
}

// NopLogger 是丢弃所有日志消息的 Logger 实现。
type NopLogger struct{}

// NewNopLogger 返回一个丢弃所有消息的 Logger。
// NOTES: 此方法仅用于测试，禁止用于实际应用场景
func NewNopLogger() Logger {
	return &NopLogger{}
}

func (n *NopLogger) Info(msg string, keyvals ...any)  {}
func (n *NopLogger) Error(msg string, err error, keyvals ...any) {}
func (n *NopLogger) Debug(msg string, keyvals ...any) {}
func (n *NopLogger) Warn(msg string, keyvals ...any)  {}
