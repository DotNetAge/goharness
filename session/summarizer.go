package session

import "context"

type Summarizer interface {
	Summarize(ctx context.Context, messages []Message) (string, error)
}
