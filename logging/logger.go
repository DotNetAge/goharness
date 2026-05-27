package logging

import "log/slog"

type Logger interface {
	Info(msg string, keyvals ...any)
	Error(msg string, err error, keyvals ...any)
	Debug(msg string, keyvals ...any)
	Warn(msg string, keyvals ...any)
}

type SlogAdapter struct {
	logger *slog.Logger
}

func NewSlogAdapter(logger *slog.Logger) *SlogAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogAdapter{logger: logger}
}

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
