package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/alileza/brokemode/internal/metrics"
	"github.com/alileza/brokemode/internal/ollama"
	"github.com/alileza/brokemode/internal/registry"
)

// sseWriter emits Anthropic-format SSE events and flushes each one; a
// buffered event stream stalls Claude Code.
type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSEWriter(w http.ResponseWriter) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	return &sseWriter{w: w, f: f}, true
}

func (s *sseWriter) event(name string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, data)
	s.f.Flush()
}

// pingEvery is how many content_block_delta events pass between pings.
const pingEvery = 25

// streamMessages runs the Ollama chat stream and emits the exact event
// sequence Claude Code requires:
//
//	message_start
//	(ping)
//	content_block_start / content_block_delta* / content_block_stop  (per block)
//	message_delta
//	message_stop
type streamState struct {
	sse          *sseWriter
	blockIndex   int
	blockOpen    bool
	deltasSinceP int
	sawToolCall  bool
	outputTokens int
}

func (st *streamState) openTextBlock() {
	if st.blockOpen {
		return
	}
	st.sse.event("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         st.blockIndex,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	st.blockOpen = true
}

func (st *streamState) closeBlock() {
	if !st.blockOpen {
		return
	}
	st.sse.event("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": st.blockIndex,
	})
	st.blockOpen = false
	st.blockIndex++
}

func (st *streamState) textDelta(text string) {
	st.openTextBlock()
	st.sse.event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": st.blockIndex,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
	st.deltasSinceP++
	if st.deltasSinceP >= pingEvery {
		st.sse.event("ping", map[string]any{"type": "ping"})
		st.deltasSinceP = 0
	}
}

// toolUseBlock emits a complete tool_use block: start (empty input), one
// input_json_delta carrying the full arguments, then stop.
func (st *streamState) toolUseBlock(tc ollama.ToolCall) {
	st.closeBlock()
	st.sawToolCall = true
	args := tc.Function.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	st.sse.event("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": st.blockIndex,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    newID("toolu"),
			"name":  tc.Function.Name,
			"input": map[string]any{},
		},
	})
	st.sse.event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": st.blockIndex,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)},
	})
	st.sse.event("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": st.blockIndex,
	})
	st.blockIndex++
}

func (s *Server) streamMessages(w http.ResponseWriter, r *http.Request, chatReq *ollama.ChatRequest, req *MessagesRequest, model *registry.Model, rec *metrics.RequestRecord, start time.Time) {
	sse, ok := newSSEWriter(w)
	if !ok {
		rec.Status = http.StatusInternalServerError
		s.writeError(w, http.StatusInternalServerError, "api_error", "response writer does not support streaming")
		return
	}

	msgID := newID("msg")
	sse.event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         req.Model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         Usage{InputTokens: 0, OutputTokens: 0},
		},
	})
	sse.event("ping", map[string]any{"type": "ping"})

	st := &streamState{sse: sse}
	var final ollama.ChatResponse
	firstChunk := true

	err := s.Client.ChatStream(r.Context(), *chatReq, func(chunk ollama.ChatResponse) error {
		if firstChunk {
			rec.TTFTMs = float64(time.Since(start)) / float64(time.Millisecond)
			firstChunk = false
		}
		if chunk.Message.Content != "" {
			st.textDelta(chunk.Message.Content)
		}
		for _, tc := range chunk.Message.ToolCalls {
			st.toolUseBlock(tc)
		}
		if chunk.Done {
			final = chunk
		}
		return nil
	})
	if err != nil {
		// Mid-stream failure: the Anthropic protocol's answer is an error
		// event; the HTTP status is already 200.
		rec.Status = http.StatusBadGateway
		sse.event("error", map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": err.Error()},
		})
		return
	}

	// A stream that produced no blocks still needs one (empty) text block —
	// Claude Code expects at least one content block per message.
	if st.blockIndex == 0 && !st.blockOpen {
		st.openTextBlock()
	}
	st.closeBlock()

	stopReason := mapStopReason(final.DoneReason, st.sawToolCall)
	sse.event("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"input_tokens":  final.PromptEvalCount,
			"output_tokens": final.EvalCount,
		},
	})
	sse.event("message_stop", map[string]any{"type": "message_stop"})

	rec.Status = http.StatusOK
	rec.TokensIn = final.PromptEvalCount
	rec.TokensOut = final.EvalCount
	if sec := final.EvalDuration.Seconds(); sec > 0 {
		rec.DecodeTPS = float64(final.EvalCount) / sec
	}
}
