// Package logging provides a structured logging interface and slog adapter.
// It defines a Logger interface that can be implemented by any logging backend,
// with a built-in adapter for Go's standard log/slog package.
package logging

import "log/slog"

// Logger defines the interface for structured logging operations.
// Implementations should support log levels: Info, Error, Debug, Warn.
type Logger interface {
	// Info logs an informational message with optional key-value pairs.
	Info(msg string, keyvals ...any)
	// Error logs an error message with the error and optional key-value pairs.
	Error(msg string, err error, keyvals ...any)
	// Debug logs a debug message with optional key-value pairs.
	Debug(msg string, keyvals ...any)
	// Warn logs a warning message with optional key-value pairs.
	Warn(msg string, keyvals ...any)
}

// SlogAdapter implements Logger using Go's standard log/slog package.
type SlogAdapter struct {
	logger *slog.Logger
}

// NewSlogAdapter creates a new SlogAdapter wrapping the given slog.Logger.
// If logger is nil, uses slog.Default().
func NewSlogAdapter(logger *slog.Logger) *SlogAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogAdapter{logger: logger}
}

// DefaultLogger returns a Logger using the default slog.Logger.
func DefaultLogger() Logger {
	return &SlogAdapter{logger: slog.Default()}
}

// Info logs an informational message at Info level.
func (l *SlogAdapter) Info(msg string, keyvals ...any) {
	l.logger.Info(msg, keyvals...)
}

// Error logs an error message at Error level, including the error value.
func (l *SlogAdapter) Error(msg string, err error, keyvals ...any) {
	args := make([]any, 0, 2+len(keyvals))
	if err != nil {
		args = append(args, "error", err)
	}
	args = append(args, keyvals...)
	l.logger.Error(msg, args...)
}

// Debug logs a debug message at Debug level.
func (l *SlogAdapter) Debug(msg string, keyvals ...any) {
	l.logger.Debug(msg, keyvals...)
}

// Warn logs a warning message at Warn level.
func (l *SlogAdapter) Warn(msg string, keyvals ...any) {
	l.logger.Warn(msg, keyvals...)
}

// NopLogger is a Logger implementation that discards all log messages.
type NopLogger struct{}

// NewNopLogger returns a Logger that discards all messages.
// NOTES: 此方法仅用于测试，禁止用于实际应用场景
func NewNopLogger() Logger {
	return &NopLogger{}
}

func (n *NopLogger) Info(msg string, keyvals ...any)  {}
func (n *NopLogger) Error(msg string, err error, keyvals ...any) {}
func (n *NopLogger) Debug(msg string, keyvals ...any) {}
func (n *NopLogger) Warn(msg string, keyvals ...any)  {}
