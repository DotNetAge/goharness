package core

import (
	"log/slog"
)

// Logger defines a unified logging interface compatible with zap-style calling convention.
// External logger implementations (logrus, zap, zerolog, slog, etc.) can be used
// as long as they implement this interface with matching method signatures.
//
// Design principle:
//   - Interface-based: allows dependency injection from Agent → ReActor → Tools
//   - Zap-compatible: follows uber-go/zap's calling convention
//   - Structured logging: all methods accept optional key-value pairs
//
// Example usage:
//
//	logger.Info("search completed", "query", "test", "results", 10)
//	logger.Warn("slow response", "duration", 2.5)
//	logger.Error("connection failed", err, "addr", "127.0.0.1")
//	logger.Debug("processing item", "id", 123)
type Logger interface {
	// Info logs an informational message with optional key-value pairs.
	Info(msg string, keyvals ...any)

	// Error logs an error message with the error object and optional key-value pairs.
	Error(msg string, err error, keyvals ...any)

	// Debug logs a debug message with optional key-value pairs.
	Debug(msg string, keyvals ...any)

	// Warn logs a warning message with optional key-value pairs.
	Warn(msg string, keyvals ...any)
}

// SlogAdapter adapts slog.Logger to implement the Logger interface.
// This is the default implementation when no custom logger is injected.
type SlogAdapter struct {
	logger *slog.Logger
}

// NewSlogAdapter creates a new SlogAdapter wrapping slog.Logger.
// Falls back to slog.Default() if logger is nil.
func NewSlogAdapter(logger *slog.Logger) *SlogAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogAdapter{logger: logger}
}

// DefaultLogger returns a Logger backed by slog.Default().
func DefaultLogger() Logger {
	return &SlogAdapter{logger: slog.Default()}
}

func (l *SlogAdapter) Info(msg string, keyvals ...any) {
	l.logger.Info(msg, keyvals...)
}

func (l *SlogAdapter) Error(msg string, err error, keyvals ...any) {
	args := make([]any, 0, 2+len(keyvals))
	if err != nil {
		args = append(args, "error", err)
	}
	args = append(args, keyvals...)
	l.logger.Error(msg, args...)
}

func (l *SlogAdapter) Debug(msg string, keyvals ...any) {
	l.logger.Debug(msg, keyvals...)
}

func (l *SlogAdapter) Warn(msg string, keyvals ...any) {
	l.logger.Warn(msg, keyvals...)
}
