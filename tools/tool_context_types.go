package tools

import (
	"context"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/store"
)

type toolCtxKeyType struct{}

var toolCtxKey toolCtxKeyType

type ToolContext struct {
	EmitEvent   func(events.ReactEvent)
	ResultStore *store.ResultStore
	KVStore     store.KVStore
	FileStore   store.FileStore
	SessionID   string
	Logger      logging.Logger

	ProjectDir string
	SessionDir string
}

func WithToolContext(ctx context.Context, tc *ToolContext) context.Context {
	return context.WithValue(ctx, toolCtxKey, tc)
}

func GetToolContext(ctx context.Context) *ToolContext {
	tc, _ := ctx.Value(toolCtxKey).(*ToolContext)
	if tc == nil {
		return &ToolContext{}
	}
	return tc
}
