package reactor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	gochat "github.com/DotNetAge/gochat"
	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goreact/core"
)

// CallInput describes what the caller wants from an LLM call.
// SystemPromptSections contains pre-built SystemMessage sections
// from the Prompt struct (stable across rounds).
// History, UserMessage, and Tools provide runtime context.
type CallInput struct {
	SessionID            string
	SystemPromptSections []gochatcore.Message // pre-built system messages (from Prompt)
	UserMessage          string               // the user's original input
	History              ConversationHistory  // conversation history to include
	Tools                []gochatcore.Tool    // native function calling tools (full schema, stable)
}

// CallResult holds the response, native tool calls, and token usage from a single LLM call.
//
// Content contains the text response — this is always populated.
// ToolCalls contains native function calling results from the LLM (non-streaming path only).
// When ToolCalls is non-empty, callers should prefer it over text-based tool call parsing.
type CallResult struct {
	Content    string
	ToolCalls  []gochatcore.ToolCall // native function call results (non-streaming) or nil
	TokenUsage core.TokenUsage
	Error      error // non-nil when the LLM call itself failed (network, auth, timeout, etc.)
	TimedOut   bool  // true when the call was cancelled due to streamTimeout
}

// StreamChunkCallback is called for each content chunk during streaming.
type StreamChunkCallback func(chunk string)

// StreamThinkingCallback is called for each thinking/reasoning chunk during streaming.
// Used by models with extended thinking (DeepSeek-R1, o1, etc.) to emit raw thinking stream.
type StreamThinkingCallback func(chunk string)

// MockLLMFunc is the signature for a mock LLM function used in testing.
type MockLLMFunc func(ctx context.Context, input CallInput) (*gochatcore.Response, error)

// LLMCaller encapsulates all LLM calling logic: request building, context window
// management, token estimation, sliding window, token usage recording and persistence.
//
// It replaces the scattered LLM responsibilities previously spread across Reactor:
//   - buildLLMBuilder / callLLMWithHistory / callLLMStream → Call / CallStream / CallGate
//   - estimateInputTokens → internal to the request-building path
//   - checkSlide → internal, automatically invoked before request assembly
//   - Token usage recording → built-in, persisted via SessionStore
type LLMCaller struct {
	mu sync.RWMutex

	// LLM configuration
	modelName        string
	temperature      float64
	topP             float64
	topK             int
	presencePenalty  float64
	frequencyPenalty float64
	maxTokens        int
	contextLength     int
	systemPrompt     string
	clientType       gochat.ClientType

	// Infrastructure
	client         gochat.ClientBuilder
	tokenEstimator core.TokenEstimator
	contextWindow  *core.ContextWindow
	slideConfig    core.SlideConfig
	sessionStore   core.SessionStore
	logger         core.Logger // Unified logging interface

	// Token usage records for this session
	records []core.TokenUsage

	// Slide handler — called when messages are evicted from context window
	slideHandler core.SlideHandler

	// Testing support
	mockLLM       MockLLMFunc
	streamTimeout time.Duration // per-call timeout for streaming (default 3min)
}

// LLMCallerConfig holds the configuration needed to create an LLMCaller.
type LLMCallerConfig struct {
	ModelName        string
	SystemPrompt     string
	Temperature      float64
	TopP             float64
	TopK             int
	PresencePenalty  float64
	FrequencyPenalty float64
	MaxTokens        int
	ContextLength    int
	ClientType       gochat.ClientType
	Logger           core.Logger // Unified logging interface
}

// NewLLMCaller creates an LLMCaller with the given configuration.
// The client is used as the base for building requests (model, temperature, etc. are
// applied on top). If client is nil, a new default client is created from the config.
func NewLLMCaller(
	cfg LLMCallerConfig,
	client gochat.ClientBuilder,
	tokenEstimator core.TokenEstimator,
	sessionStore core.SessionStore,
	opts ...LLMCallerOption,
) *LLMCaller {
	if client == nil {
		client = gochat.Client()
	}
	c := &LLMCaller{
		modelName:        cfg.ModelName,
		systemPrompt:     cfg.SystemPrompt,
		temperature:      cfg.Temperature,
		topP:             cfg.TopP,
		topK:             cfg.TopK,
		presencePenalty:  cfg.PresencePenalty,
		frequencyPenalty: cfg.FrequencyPenalty,
		maxTokens:        cfg.MaxTokens,
		clientType:       cfg.ClientType,
		client:           client,
		tokenEstimator:   tokenEstimator,
		slideConfig:      core.DefaultSlideConfig,
		sessionStore:     sessionStore,
		logger:           cfg.Logger,
		records:          make([]core.TokenUsage, 0),
		streamTimeout:    3 * time.Minute,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// LLMCallerOption configures an LLMCaller during creation.
type LLMCallerOption func(*LLMCaller)

// WithLLMCallerMock sets a mock LLM function for testing.
func WithLLMCallerMock(fn MockLLMFunc) LLMCallerOption {
	return func(c *LLMCaller) {
		c.mockLLM = fn
	}
}

// WithLLMCallerSessionStore sets a custom session store.
func WithLLMCallerSessionStore(ss core.SessionStore) LLMCallerOption {
	return func(c *LLMCaller) {
		c.sessionStore = ss
	}
}

// WithLLMCallerSlideHandler sets a callback for context window slide events.
func WithLLMCallerSlideHandler(handler core.SlideHandler) LLMCallerOption {
	return func(c *LLMCaller) {
		c.slideHandler = handler
	}
}

// SetConfig updates the LLM caller's configuration at runtime.
// Changes take effect on the next LLM call. Safe to call concurrently with
// running calls — in-flight requests use the previous values.
func (c *LLMCaller) SetConfig(cfg LLMCallerConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.modelName = cfg.ModelName
	c.systemPrompt = cfg.SystemPrompt
	c.temperature = cfg.Temperature
	c.topP = cfg.TopP
	c.topK = cfg.TopK
	c.presencePenalty = cfg.PresencePenalty
	c.frequencyPenalty = cfg.FrequencyPenalty
	c.maxTokens = cfg.MaxTokens
	c.clientType = cfg.ClientType
	if cfg.Logger != nil {
		c.logger = cfg.Logger
	}
}

// SetClient replaces the underlying gochat client builder.
// Used when API key, base URL, or auth token change.
func (c *LLMCaller) SetClient(client gochat.ClientBuilder) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.client = client
}

// ---------------------------------------------------------------------------
// Public accessors
// ---------------------------------------------------------------------------

// ContextWindow returns the current context window.
func (c *LLMCaller) ContextWindow() *core.ContextWindow {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.contextWindow
}

// SetContextWindow replaces the current context window.
func (c *LLMCaller) SetContextWindow(cw *core.ContextWindow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.contextWindow = cw
}

// SlideConfig returns the current slide configuration.
func (c *LLMCaller) SlideConfig() core.SlideConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.slideConfig
}

// Estimator returns the underlying token estimator.
func (c *LLMCaller) Estimator() core.TokenEstimator {
	return c.tokenEstimator
}

// SessionStore returns the underlying session store.
func (c *LLMCaller) SessionStore() core.SessionStore {
	return c.sessionStore
}

// ---------------------------------------------------------------------------
// RebuildContext
// ---------------------------------------------------------------------------

// RebuildContext loads conversation history from the SessionStore for the
// given session and agent, and rebuilds the ContextWindow from it.
// If the session doesn't exist, a new empty context window is created.
func (c *LLMCaller) RebuildContext(ctx context.Context, sessionID, agentName string) error {
	if c.sessionStore == nil {
		c.ensureContextWindow(sessionID)
		return nil
	}

	msgs, err := c.sessionStore.Get(ctx, sessionID)
	if err != nil {
		c.ensureContextWindow(sessionID)
		return nil
	}

	winSize := c.contextLength
	if winSize <= 0 {
		winSize = c.maxTokens
	}
	cw := core.NewContextWindowWithRole(sessionID, agentName, int64(winSize))
	for _, m := range msgs {
		cw.AddMessageWithTimestamp(m.Role, m.Content, m.Timestamp)
	}

	c.mu.Lock()
	c.contextWindow = cw
	c.records = make([]core.TokenUsage, 0)
	c.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------------------
// Main API: Call, CallStream, CallGate
// ---------------------------------------------------------------------------

// Call makes a synchronous LLM call with full context management:
// token estimation, sliding window check, message assembly, sending,
// token usage recording and persistence.
//
// When native function calling tools are provided via CallInput.Tools and the LLM
// responds with native tool calls, they are returned in CallResult.ToolCalls.
// Callers should prefer ToolCalls over text-based tool call parsing when available.
func (c *LLMCaller) Call(ctx context.Context, input CallInput) CallResult {
	c.mu.RLock()
	cw := c.contextWindow
	c.mu.RUnlock()

	if cw == nil {
		c.ensureContextWindow(input.SessionID)
		c.mu.RLock()
		cw = c.contextWindow
		c.mu.RUnlock()
	}

	// 1. Check slide
	c.doSlide(input)

	// 2. Assemble messages
	messages := c.assembleMessages(input)

	// 3. Calculate precise input tokens
	preciseInput := c.calcPreciseTokens(messages)

	// 4. Handle mock LLM
	if c.mockLLM != nil {
		resp, err := c.mockLLM(ctx, input)
		if err != nil {
			return c.buildErrorResult(ctx, input, err, preciseInput)
		}
		var toolCalls []gochatcore.ToolCall
		if resp != nil && len(resp.Message.ToolCalls) > 0 {
			toolCalls = resp.Message.ToolCalls
		}
		return c.recordResult(ctx, input, resp.Content, preciseInput, resp, messages, toolCalls)
	}

	// 5. Build request and send
	builder := c.buildClient(messages, input.Tools)
	resp, err := builder.GetResponseFor(c.clientType)
	if err != nil {
		return c.buildErrorResult(ctx, input, err, preciseInput)
	}

	content := ""
	var toolCalls []gochatcore.ToolCall
	if resp != nil {
		content = resp.Content
		if len(resp.Message.ToolCalls) > 0 {
			toolCalls = resp.Message.ToolCalls
		}
	}
	return c.recordResult(ctx, input, content, preciseInput, resp, messages, toolCalls)
}

// CallStream makes a streaming LLM call. Token estimation, sliding, and recording
// happen automatically. The onChunk callback is invoked for each content fragment.
// The onThinking callback (if non-nil) is invoked for each thinking/reasoning fragment
// from models with extended thinking (DeepSeek-R1, o1, etc.).
//
// Native tool calls are NOT available in the streaming path (gochat Stream interface
// does not expose ToolCalls). Tool call parsing in the streaming path relies on
// text-based extraction via ParseThinkResponse or similar.
// For native tool call support, use Call() (non-streaming).
func (c *LLMCaller) CallStream(ctx context.Context, input CallInput, onChunk StreamChunkCallback, onThinking ...StreamThinkingCallback) (cr CallResult) {
	defer func() {
		if p := recover(); p != nil {
			c.logger.Error("CallStream panicked", fmt.Errorf("%v", p),
				"panic", p,
			)
			cr.Error = fmt.Errorf("CallStream panic: %v", p)
		}
	}()

	c.mu.RLock()
	cw := c.contextWindow
	c.mu.RUnlock()

	if cw == nil {
		c.ensureContextWindow(input.SessionID)
		c.mu.RLock()
		cw = c.contextWindow
		c.mu.RUnlock()
	}

	callCtx, cancel := context.WithTimeout(ctx, c.streamTimeout)
	defer cancel()

	// 1. Check slide
	c.doSlide(input)

	// 2. Assemble messages
	messages := c.assembleMessages(input)

	// 3. Calculate precise input tokens
	preciseInput := c.calcPreciseTokens(messages)

	// 4. Handle mock LLM (ToolCalls from mock are preserved)
	var toolCalls []gochatcore.ToolCall
	if c.mockLLM != nil {
		resp, err := c.mockLLM(callCtx, input)
		if err != nil {
			return c.buildErrorResult(callCtx, input, err, preciseInput)
		}
		if resp != nil {
			onChunk(resp.Content)
			if len(resp.Message.ToolCalls) > 0 {
				toolCalls = resp.Message.ToolCalls
			}
		}
		return c.recordResult(callCtx, input, resp.Content, preciseInput, resp, messages, toolCalls)
	}

	// 5. Build request and stream
	llmStart := time.Now()
	c.logger.Info("llm call start",
		"session_id", input.SessionID,
		"model", c.modelName,
		"input_tokens", preciseInput,
		"tools", len(input.Tools),
	)
	builder := c.buildClient(messages, input.Tools)
	stream, err := builder.GetStreamFor(c.clientType)
	if err != nil {
		c.logger.Error("llm call failed", err,
			"session_id", input.SessionID,
			"model", c.modelName,
			"elapsed_ms", time.Since(llmStart).Milliseconds(),
		)
		return c.buildErrorResult(callCtx, input, fmt.Errorf("stream LLM call failed: %w", err), preciseInput)
	}
	defer stream.Close()

	var contentBuf strings.Builder
	var thinkingBuf strings.Builder
	var thinkingCB StreamThinkingCallback
	if len(onThinking) > 0 && onThinking[0] != nil {
		thinkingCB = onThinking[0]
	}

	for stream.Next() {
		select {
		case <-callCtx.Done():
			stream.Close()
			elapsed := time.Since(llmStart)
			c.logger.Error("llm call timed out",
				fmt.Errorf("%w (timeout: %s, elapsed: %s)", callCtx.Err(), c.streamTimeout, elapsed),
				"session_id", input.SessionID,
				"model", c.modelName,
				"elapsed_ms", elapsed.Milliseconds(),
				"output_chars", contentBuf.Len(),
				"thinking_chars", thinkingBuf.Len(),
			)
			result := c.recordPartialResult(callCtx, input, contentBuf.String(), preciseInput, fmt.Errorf("llm call timed out after %v (%s)", c.streamTimeout, callCtx.Err()))
			result.TimedOut = true
			return result
		default:
		}

		event := stream.Event()
		if event.Err != nil {
			return c.recordPartialResult(callCtx, input, contentBuf.String(), preciseInput, event.Err)
		}
		switch event.Type {
		case gochatcore.EventContent:
			contentBuf.WriteString(event.Content)
			onChunk(event.Content)
		case gochatcore.EventThinking:
			thinkingBuf.WriteString(event.Content)
			if thinkingCB != nil {
				thinkingCB(event.Content)
			}
		case gochatcore.EventError:
			return c.recordPartialResult(callCtx, input, contentBuf.String(), preciseInput, event.Err)
		case gochatcore.EventDone:
		}
	}

	c.logger.Info("llm call end",
		"session_id", input.SessionID,
		"model", c.modelName,
		"elapsed_ms", time.Since(llmStart).Milliseconds(),
		"output_chars", contentBuf.Len(),
		"thinking_chars", thinkingBuf.Len(),
	)

	// Collect native tool calls accumulated from streaming deltas.
	// The Stream accumulates tool call deltas internally during Next() iteration
	// and finalizes them when EventDone is received (finish_reason="tool_calls").
	streamToolCalls := stream.ToolCalls()
	if len(streamToolCalls) > 0 {
		toolCalls = make([]gochatcore.ToolCall, len(streamToolCalls))
		for i, tc := range streamToolCalls {
			toolCalls[i] = gochatcore.ToolCall{
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: tc.Arguments,
			}
		}
	}

	outputTokens := 0
	if usage := stream.Usage(); usage != nil {
		if usage.CompletionTokens > 0 {
			outputTokens = usage.CompletionTokens
		} else if usage.TotalTokens > 0 {
			outputTokens = usage.TotalTokens - preciseInput
			if outputTokens < 0 {
				outputTokens = usage.TotalTokens
			}
		}
	}

	return c.recordResult(ctx, input, contentBuf.String(), preciseInput, outputTokens, messages, toolCalls)
}

// ---------------------------------------------------------------------------
// Token statistics
// ---------------------------------------------------------------------------

// PreviewTokens estimates the token consumption for a hypothetical call
// without sending the request.
func (c *LLMCaller) PreviewTokens(input CallInput) int {
	messages := c.assembleMessages(input)
	return c.calcPreciseTokens(messages)
}

// RemainTokens returns the remaining token capacity of the context window.
func (c *LLMCaller) RemainTokens() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.contextWindow == nil {
		return c.maxTokens
	}
	return int(c.contextWindow.TokensRemaining())
}

// AddContextMessage adds a message to the context window token tracking.
// Used for intermediate step summaries that need to be tracked for context
// budget but are not sent as part of the next LLM call (they're part of
// ConversationHistory).
func (c *LLMCaller) AddContextMessage(role, content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.contextWindow != nil {
		c.contextWindow.AddMessage(role, content)
		tokens := int64(c.tokenEstimator.Estimate(content))
		c.contextWindow.AddTokens(tokens)
	}
}

// TotalInputTokens returns the total input tokens from the context window.
// Calculated by traversing all current messages, not by summing call records.
func (c *LLMCaller) TotalInputTokens() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.contextWindow == nil {
		return 0
	}
	return int(c.contextWindow.TokensUsed)
}

// TotalOutputTokens returns the total output tokens across all recorded calls.
func (c *LLMCaller) TotalOutputTokens() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var total int
	for _, r := range c.records {
		total += r.OutputTokens
	}
	return total
}

// TokenRecords returns a copy of all token usage records for external statistics.
func (c *LLMCaller) TokenRecords() []core.TokenUsage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]core.TokenUsage, len(c.records))
	copy(out, c.records)
	return out
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// ensureContextWindow creates a new ContextWindow if none exists.
func (c *LLMCaller) ensureContextWindow(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.contextWindow == nil {
			winSize := c.contextLength
			if winSize <= 0 {
				winSize = c.maxTokens
			}
			c.contextWindow = core.NewContextWindow(sessionID, int64(winSize))
	}
}

// doSlide checks whether the context window needs sliding and triggers it if necessary.
func (c *LLMCaller) doSlide(input CallInput) {
	c.mu.RLock()
	cw := c.contextWindow
	sc := c.slideConfig
	c.mu.RUnlock()

	if cw == nil || c.sessionStore == nil {
		return
	}

	if !cw.SlideTriggered(sc) {
		return
	}

	estimateFn := func(s string) int { return c.tokenEstimator.Estimate(s) }
	slided := cw.Slide(sc, estimateFn)

	if len(slided.Messages) > 0 {
		c.logger.Warn("context window slid messages",
			"session_id", cw.SessionID,
			"evicted", len(slided.Messages),
			"tokens_freed", slided.TokenCount,
		)
		event := core.SlideEvent{
			SessionID: cw.SessionID,
			Slided:    slided.Messages,
			Remaining: len(cw.Messages),
			Timestamp: time.Now().Unix(),
		}
		core.EmitSlideEvent(c.slideHandler, context.Background(), event)
	}
}

// assembleMessages constructs the message sequence:
//
//  1. SystemPromptSections (from Prompt struct) — stable across rounds for KV cache
//  2. ChatMessage(History...) — conversation history
//  3. UserMessage(userMsg) — user input
//
// Tools are added separately via buildClient.Tools().
func (c *LLMCaller) assembleMessages(input CallInput) []gochatcore.Message {
	var msgs []gochatcore.Message

	// Layer 1: Pre-built system prompt sections (from Prompt.ToSectionedMessages)
	if len(input.SystemPromptSections) > 0 {
		msgs = append(msgs, input.SystemPromptSections...)
	}

	// Layer 2: Conversation history
	for _, m := range input.History {
		msg := gochatcore.NewTextMessage(m.Role, m.Content)
		// Propagate ToolCallID for role="tool" messages (required by strict APIs like DeepSeek)
		if m.ToolCallID != "" {
			msg.ToolCallID = m.ToolCallID
		}
		msgs = append(msgs, msg)
	}

	// Layer 3: User message
	if input.UserMessage != "" {
		msgs = append(msgs, gochatcore.NewUserMessage(input.UserMessage))
	}

	return msgs
}

// buildClient creates a gochat ClientBuilder configured with all the LLM parameters
// and pre-assembled messages. Tools are appended when provided.
func (c *LLMCaller) buildClient(messages []gochatcore.Message, tools []gochatcore.Tool) gochat.ClientBuilder {
	builder := c.client.
		Model(c.modelName).
		Temperature(c.temperature).
		MaxTokens(c.maxTokens).
		EnableThinking(true).
		ParallelToolCalls(true).
		ToolChoice("auto")

	if c.topP > 0 {
		builder = builder.TopP(c.topP)
	}
	if c.topK > 0 {
		builder = builder.TopK(c.topK)
	}
	if c.presencePenalty != 0 {
		builder = builder.PresencePenalty(c.presencePenalty)
	}
	if c.frequencyPenalty != 0 {
		builder = builder.FrequencyPenalty(c.frequencyPenalty)
	}

	// Add all assembled messages
	builder = builder.Messages(messages...)

	// Native tools (appended after messages)
	if len(tools) > 0 {
		builder = builder.Tools(tools...)
	}

	return builder
}

// calcPreciseTokens estimates the total token count for the given message sequence.
// Uses TokenEstimator for each message and sums them up.
func (c *LLMCaller) calcPreciseTokens(messages []gochatcore.Message) int {
	var total int
	for _, m := range messages {
		for _, block := range m.Content {
			if block.Type == "text" || block.Text != "" {
				total += c.tokenEstimator.Estimate(block.Text)
			}
		}
	}
	return total
}

// recordResult builds a TokenUsage record via recordTokenUsage, and returns a CallResult.
// toolCalls carries native function call results from the LLM response.
func (c *LLMCaller) recordResult(ctx context.Context, input CallInput, content string, inputTokens int, respOrTokens interface{}, messages []gochatcore.Message, toolCalls []gochatcore.ToolCall) CallResult {
	outputTokens := 0
	switch v := respOrTokens.(type) {
	case *gochatcore.Response:
		if v != nil && v.Usage != nil && v.Usage.TotalTokens > 0 {
			outputTokens = v.Usage.TotalTokens - inputTokens
			if outputTokens < 0 {
				outputTokens = v.Usage.TotalTokens
			}
		}
	case int:
		outputTokens = v
	case error:
	}

	usage := c.recordTokenUsage(ctx, input, inputTokens, outputTokens)

	return CallResult{
		Content:    content,
		ToolCalls:  toolCalls,
		TokenUsage: usage,
	}
}

// recordPartialResult records a partial result (e.g., streaming error after partial content).
func (c *LLMCaller) recordPartialResult(ctx context.Context, input CallInput, content string, inputTokens int, err error) CallResult {
	result := c.recordResult(ctx, input, content, inputTokens, err, nil, nil)
	result.Error = err
	return result
}

// buildErrorResult creates a CallResult for failed calls, records the token usage,
// persists it to SessionStore, and returns the result.
func (c *LLMCaller) buildErrorResult(ctx context.Context, input CallInput, err error, inputTokens int) CallResult {
	usage := c.recordTokenUsage(ctx, input, inputTokens, 0)

	return CallResult{
		Content:    fmt.Sprintf("[llmcaller error] %v", err),
		ToolCalls:  nil,
		TokenUsage: usage,
		Error:      err,
	}
}

// recordTokenUsage records token usage: calculates remain tokens, builds TokenUsage,
// updates context window, appends to internal records, and persists to SessionStore.
func (c *LLMCaller) recordTokenUsage(ctx context.Context, input CallInput, inputTokens int, outputTokens int) core.TokenUsage {
	remainTokens := c.maxTokens
	c.mu.RLock()
	if c.contextWindow != nil {
		remainTokens = int(c.contextWindow.TokensRemaining())
	}
	c.mu.RUnlock()

	usage := core.TokenUsage{
		Timestamp:    time.Now(),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		RemainTokens: remainTokens,
	}

	c.mu.Lock()
	if c.contextWindow != nil && inputTokens > 0 {
		c.contextWindow.AddTokens(int64(inputTokens))
	}
	c.records = append(c.records, usage)
	c.mu.Unlock()

	sessionID := input.SessionID
	if sessionID == "" {
		c.mu.RLock()
		if c.contextWindow != nil {
			sessionID = c.contextWindow.SessionID
		}
		c.mu.RUnlock()
	}
	if c.sessionStore != nil && sessionID != "" {
		if persistErr := c.sessionStore.AppendTokenUsage(ctx, sessionID, usage); persistErr != nil {
			c.logger.Warn("failed to persist token usage",
				"session_id", sessionID,
				"error", persistErr,
			)
		}
	}

	return usage
}
