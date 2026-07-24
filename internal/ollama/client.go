// Package ollama is a minimal client for the Ollama HTTP API:
// /api/generate, /api/chat, and /api/tags.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Client talks to a local Ollama server.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a client for baseURL, or $OLLAMA_HOST /
// http://127.0.0.1:11434 when baseURL is empty.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = os.Getenv("OLLAMA_HOST")
	}
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434"
	}
	return &Client{
		BaseURL: baseURL,
		// Model loads on a cold 12B can take minutes; no client timeout —
		// callers control lifetime via ctx.
		HTTP: &http.Client{},
	}
}

// Options are per-request model options (subset we use).
type Options struct {
	NumCtx      int      `json:"num_ctx,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int     `json:"top_k,omitempty"`
	NumPredict  int      `json:"num_predict,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

// GenerateRequest is the body for POST /api/generate.
type GenerateRequest struct {
	Model   string   `json:"model"`
	Prompt  string   `json:"prompt"`
	System  string   `json:"system,omitempty"`
	Stream  bool     `json:"stream"`
	Format  string   `json:"format,omitempty"`
	Options *Options `json:"options,omitempty"`
}

// Timing carries Ollama's native nanosecond timing fields.
type Timing struct {
	TotalDuration      time.Duration `json:"total_duration"`
	LoadDuration       time.Duration `json:"load_duration"`
	PromptEvalCount    int           `json:"prompt_eval_count"`
	PromptEvalDuration time.Duration `json:"prompt_eval_duration"`
	EvalCount          int           `json:"eval_count"`
	EvalDuration       time.Duration `json:"eval_duration"`
}

// GenerateResponse is the (non-streamed) reply from /api/generate.
type GenerateResponse struct {
	Model      string `json:"model"`
	Response   string `json:"response"`
	Done       bool   `json:"done"`
	DoneReason string `json:"done_reason"`
	Timing
}

// Message is one chat turn.
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"`
}

// ToolCall is Ollama's tool invocation shape.
type ToolCall struct {
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction names the tool and carries its arguments.
type ToolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Tool declares a callable tool to the model.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the tool's schema.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ChatRequest is the body for POST /api/chat.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
	Options  *Options  `json:"options,omitempty"`
}

// ChatResponse is one reply (or stream chunk) from /api/chat.
type ChatResponse struct {
	Model      string  `json:"model"`
	Message    Message `json:"message"`
	Done       bool    `json:"done"`
	DoneReason string  `json:"done_reason"`
	Timing
}

// TagsResponse is the reply from GET /api/tags.
type TagsResponse struct {
	Models []TagModel `json:"models"`
}

// TagModel is one locally available model.
type TagModel struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama %s: %w (is ollama running? brew services start ollama)", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama %s: HTTP %d: %s", path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	return resp, nil
}

// Generate calls /api/generate with stream:false and returns the full
// response including native timing fields.
func (c *Client) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	req.Stream = false
	resp, err := c.post(ctx, "/api/generate", req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out GenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode /api/generate response: %w", err)
	}
	return &out, nil
}

// Chat calls /api/chat with stream:false.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	req.Stream = false
	resp, err := c.post(ctx, "/api/chat", req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode /api/chat response: %w", err)
	}
	return &out, nil
}

// ChatStream calls /api/chat with stream:true and invokes fn for every
// NDJSON chunk. The final chunk has Done=true and carries timing fields.
func (c *Client) ChatStream(ctx context.Context, req ChatRequest, fn func(ChatResponse) error) error {
	req.Stream = true
	resp, err := c.post(ctx, "/api/chat", req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var chunk ChatResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			return fmt.Errorf("decode /api/chat stream chunk: %w", err)
		}
		if err := fn(chunk); err != nil {
			return err
		}
		if chunk.Done {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read /api/chat stream: %w", err)
	}
	return fmt.Errorf("ollama stream ended without a done chunk")
}

// Tags calls GET /api/tags.
func (c *Client) Tags(ctx context.Context) (*TagsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama /api/tags: %w (is ollama running?)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama /api/tags: HTTP %d", resp.StatusCode)
	}
	var out TagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode /api/tags response: %w", err)
	}
	return &out, nil
}
