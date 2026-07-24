package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alileza/brokemode/internal/metrics"
	"github.com/alileza/brokemode/internal/ollama"
	"github.com/alileza/brokemode/internal/registry"
)

const testModels = `
max_rss_gb: 11
models:
  - name: qwen3.5:9b
    quantization: Q4_K_M
    peak_rss_gb: 7.8
    num_ctx: 16384
    aliases: [claude-sonnet-5, sonnet]
  - name: gemma4:12b
    peak_rss_gb: 10.2
    num_ctx: 8192
    aliases: [opus]
  - name: way-too-big:70b
    peak_rss_gb: 40
    num_ctx: 4096
`

// chunkScript is what the fake Ollama streams for one request.
type chunkScript []ollama.ChatResponse

// fakeOllama records the last /api/chat request and replies from a script.
type fakeOllama struct {
	lastReq ollama.ChatRequest
	script  func(req ollama.ChatRequest) chunkScript
}

func doneChunk(content string, toolCalls []ollama.ToolCall, doneReason string) ollama.ChatResponse {
	return ollama.ChatResponse{
		Message:    ollama.Message{Role: "assistant", Content: content, ToolCalls: toolCalls},
		Done:       true,
		DoneReason: doneReason,
		Timing: ollama.Timing{
			PromptEvalCount: 57, EvalCount: 23,
			PromptEvalDuration: 100e6, EvalDuration: 1e9, LoadDuration: 50e6, TotalDuration: 2e9,
		},
	}
}

func (f *fakeOllama) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &f.lastReq); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		chunks := f.script(f.lastReq)
		if !f.lastReq.Stream {
			// Non-streaming: reply with the final chunk only, merging text.
			var text strings.Builder
			var calls []ollama.ToolCall
			var last ollama.ChatResponse
			for _, c := range chunks {
				text.WriteString(c.Message.Content)
				calls = append(calls, c.Message.ToolCalls...)
				last = c
			}
			last.Message.Content = text.String()
			last.Message.ToolCalls = calls
			json.NewEncoder(w).Encode(last)
			return
		}
		enc := json.NewEncoder(w)
		for _, c := range chunks {
			enc.Encode(c)
		}
	})
	return mux
}

func newTestServer(t *testing.T, script func(ollama.ChatRequest) chunkScript) (*httptest.Server, *fakeOllama, *metrics.Metrics) {
	t.Helper()
	fake := &fakeOllama{script: script}
	ollamaSrv := httptest.NewServer(fake.handler())
	t.Cleanup(ollamaSrv.Close)

	reg, err := registry.Parse([]byte(testModels))
	if err != nil {
		t.Fatal(err)
	}
	m := metrics.New()
	gw := &Server{Client: ollama.New(ollamaSrv.URL), Registry: reg, Metrics: m}
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	return srv, fake, m
}

func post(t *testing.T, srv *httptest.Server, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if headers == nil {
		req.Header.Set("Authorization", "Bearer local")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// sseEvent is one parsed SSE frame.
type sseEvent struct {
	name string
	data map[string]any
}

func parseSSE(t *testing.T, r io.Reader) []sseEvent {
	t.Helper()
	var events []sseEvent
	sc := bufio.NewScanner(r)
	var name string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var data map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
				t.Fatalf("bad SSE data %q: %v", line, err)
			}
			events = append(events, sseEvent{name: name, data: data})
		}
	}
	return events
}

func names(events []sseEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.name
	}
	return out
}

// withoutPings drops ping events (legal at any position).
func withoutPings(events []sseEvent) []sseEvent {
	var out []sseEvent
	for _, e := range events {
		if e.name != "ping" {
			out = append(out, e)
		}
	}
	return out
}

func TestSSEEventSequence(t *testing.T) {
	textChunk := func(s string) ollama.ChatResponse {
		return ollama.ChatResponse{Message: ollama.Message{Role: "assistant", Content: s}}
	}
	toolCall := ollama.ToolCall{Function: ollama.ToolCallFunction{
		Name: "get_weather", Arguments: json.RawMessage(`{"city":"Innsbruck"}`),
	}}

	tests := []struct {
		name       string
		chunks     chunkScript
		wantSeq    []string // event names after removing pings
		wantStop   string
		wantOutTok float64
	}{
		{
			name:   "plain text stream",
			chunks: chunkScript{textChunk("Hel"), textChunk("lo"), doneChunk("", nil, "stop")},
			wantSeq: []string{"message_start", "content_block_start", "content_block_delta",
				"content_block_delta", "content_block_stop", "message_delta", "message_stop"},
			wantStop:   "end_turn",
			wantOutTok: 23,
		},
		{
			name: "text then tool call",
			chunks: chunkScript{
				textChunk("checking"),
				{Message: ollama.Message{Role: "assistant", ToolCalls: []ollama.ToolCall{toolCall}}},
				doneChunk("", nil, "stop"),
			},
			wantSeq: []string{"message_start",
				"content_block_start", "content_block_delta", "content_block_stop", // text
				"content_block_start", "content_block_delta", "content_block_stop", // tool_use
				"message_delta", "message_stop"},
			wantStop:   "tool_use",
			wantOutTok: 23,
		},
		{
			name:   "empty response still has one block",
			chunks: chunkScript{doneChunk("", nil, "stop")},
			wantSeq: []string{"message_start", "content_block_start", "content_block_stop",
				"message_delta", "message_stop"},
			wantStop:   "end_turn",
			wantOutTok: 23,
		},
		{
			name:   "max tokens",
			chunks: chunkScript{textChunk("truncat"), doneChunk("", nil, "length")},
			wantSeq: []string{"message_start", "content_block_start", "content_block_delta",
				"content_block_stop", "message_delta", "message_stop"},
			wantStop:   "max_tokens",
			wantOutTok: 23,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newTestServer(t, func(ollama.ChatRequest) chunkScript { return tt.chunks })
			resp := post(t, srv, `{"model":"sonnet","max_tokens":100,"stream":true,
				"messages":[{"role":"user","content":"hi"}]}`, nil)
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
				t.Fatalf("content-type %q", ct)
			}

			all := parseSSE(t, resp.Body)
			events := withoutPings(all)
			if got := names(events); !equalStrings(got, tt.wantSeq) {
				t.Fatalf("sequence mismatch\n got: %v\nwant: %v", got, tt.wantSeq)
			}
			// At least one ping must appear (after message_start).
			if len(all) == len(events) {
				t.Error("no ping event emitted")
			}

			// message_start must carry an assistant message envelope.
			ms := events[0].data["message"].(map[string]any)
			if ms["role"] != "assistant" || ms["type"] != "message" {
				t.Errorf("bad message_start envelope: %v", ms)
			}

			// Block indexes must nest correctly: start/stop pairs, in order.
			depth, index := 0, -1
			for _, e := range events {
				switch e.name {
				case "content_block_start":
					if depth != 0 {
						t.Fatal("content_block_start while a block is open")
					}
					depth++
					i := int(e.data["index"].(float64))
					if i != index+1 {
						t.Fatalf("block index %d, want %d", i, index+1)
					}
					index = i
				case "content_block_delta":
					if depth != 1 {
						t.Fatal("delta outside an open block")
					}
					if i := int(e.data["index"].(float64)); i != index {
						t.Fatalf("delta index %d, want %d", i, index)
					}
				case "content_block_stop":
					if depth != 1 {
						t.Fatal("content_block_stop without open block")
					}
					depth--
				}
			}
			if depth != 0 {
				t.Fatal("unclosed content block at message end")
			}

			// message_delta carries stop_reason and usage from *_count.
			md := events[len(events)-2]
			delta := md.data["delta"].(map[string]any)
			if delta["stop_reason"] != tt.wantStop {
				t.Errorf("stop_reason=%v, want %s", delta["stop_reason"], tt.wantStop)
			}
			usage := md.data["usage"].(map[string]any)
			if usage["output_tokens"].(float64) != tt.wantOutTok {
				t.Errorf("output_tokens=%v, want %v", usage["output_tokens"], tt.wantOutTok)
			}
			if usage["input_tokens"].(float64) != 57 {
				t.Errorf("input_tokens=%v, want 57", usage["input_tokens"])
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestToolCallRoundTrip(t *testing.T) {
	toolCall := ollama.ToolCall{Function: ollama.ToolCallFunction{
		Name: "get_weather", Arguments: json.RawMessage(`{"city":"Innsbruck"}`),
	}}
	srv, fake, _ := newTestServer(t, func(req ollama.ChatRequest) chunkScript {
		for _, m := range req.Messages {
			if m.Role == "tool" {
				return chunkScript{doneChunk("It is raining, pack the shell.", nil, "stop")}
			}
		}
		return chunkScript{doneChunk("", []ollama.ToolCall{toolCall}, "stop")}
	})

	// Leg 1: user asks; model must return a tool_use block.
	resp := post(t, srv, `{"model":"sonnet","max_tokens":200,
		"system":"You are a hiking assistant.",
		"tools":[{"name":"get_weather","description":"weather by city",
		          "input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"messages":[{"role":"user","content":"Weather in Innsbruck?"}]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("leg1 status %d: %s", resp.StatusCode, body)
	}
	var leg1 MessagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&leg1); err != nil {
		t.Fatal(err)
	}

	// Tools and system prompt must reach Ollama translated.
	if len(fake.lastReq.Tools) != 1 || fake.lastReq.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("tools not translated: %+v", fake.lastReq.Tools)
	}
	if fake.lastReq.Messages[0].Role != "system" || !strings.Contains(fake.lastReq.Messages[0].Content, "hiking") {
		t.Fatalf("system prompt not first ollama message: %+v", fake.lastReq.Messages)
	}
	if fake.lastReq.Model != "qwen3.5:9b" {
		t.Errorf("alias not resolved: sent model %q", fake.lastReq.Model)
	}
	if fake.lastReq.Options == nil || fake.lastReq.Options.NumCtx != 16384 {
		t.Errorf("per-model num_ctx not applied: %+v", fake.lastReq.Options)
	}

	if leg1.StopReason != "tool_use" {
		t.Fatalf("leg1 stop_reason=%s, want tool_use", leg1.StopReason)
	}
	var toolUse *ContentBlock
	for i := range leg1.Content {
		if leg1.Content[i].Type == "tool_use" {
			toolUse = &leg1.Content[i]
		}
	}
	if toolUse == nil || toolUse.Name != "get_weather" || toolUse.ID == "" {
		t.Fatalf("no valid tool_use block: %+v", leg1.Content)
	}
	if !bytes.Contains(toolUse.Input, []byte("Innsbruck")) {
		t.Fatalf("tool_use input lost: %s", toolUse.Input)
	}
	if leg1.Usage.InputTokens != 57 || leg1.Usage.OutputTokens != 23 {
		t.Errorf("usage=%+v, want 57/23", leg1.Usage)
	}

	// Leg 2: send the tool_result back, referencing the tool_use id.
	leg2Body := fmt.Sprintf(`{"model":"sonnet","max_tokens":200,
		"tools":[{"name":"get_weather","input_schema":{"type":"object"}}],
		"messages":[
		  {"role":"user","content":"Weather in Innsbruck?"},
		  {"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"get_weather","input":{"city":"Innsbruck"}}]},
		  {"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"11C, showers, 80%% rain"}]}
		]}`, toolUse.ID, toolUse.ID)
	resp2 := post(t, srv, leg2Body, nil)
	defer resp2.Body.Close()
	var leg2 MessagesResponse
	if err := json.NewDecoder(resp2.Body).Decode(&leg2); err != nil {
		t.Fatal(err)
	}

	// The tool_result must arrive at Ollama as a tool-role message carrying
	// the tool name (mapped from the tool_use id) and the result text.
	var toolMsg *ollama.Message
	for i := range fake.lastReq.Messages {
		if fake.lastReq.Messages[i].Role == "tool" {
			toolMsg = &fake.lastReq.Messages[i]
		}
	}
	if toolMsg == nil {
		t.Fatalf("no tool-role message sent to ollama: %+v", fake.lastReq.Messages)
	}
	if toolMsg.ToolName != "get_weather" || !strings.Contains(toolMsg.Content, "showers") {
		t.Errorf("tool_result mistranslated: %+v", toolMsg)
	}
	// And the prior assistant tool_use must round-trip as tool_calls.
	var sawToolCalls bool
	for _, m := range fake.lastReq.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			sawToolCalls = true
			if m.ToolCalls[0].Function.Name != "get_weather" {
				t.Errorf("assistant tool_calls mistranslated: %+v", m.ToolCalls)
			}
		}
	}
	if !sawToolCalls {
		t.Error("assistant tool_use block did not become ollama tool_calls")
	}

	if leg2.StopReason != "end_turn" || !strings.Contains(leg2.Content[0].Text, "raining") {
		t.Errorf("leg2 final answer wrong: %+v", leg2)
	}
}

func TestStreamingToolCallRoundTrip(t *testing.T) {
	toolCall := ollama.ToolCall{Function: ollama.ToolCallFunction{
		Name: "list_files", Arguments: json.RawMessage(`{"path":"/tmp"}`),
	}}
	srv, _, _ := newTestServer(t, func(req ollama.ChatRequest) chunkScript {
		return chunkScript{
			{Message: ollama.Message{Role: "assistant", ToolCalls: []ollama.ToolCall{toolCall}}},
			doneChunk("", nil, "stop"),
		}
	})
	resp := post(t, srv, `{"model":"sonnet","max_tokens":100,"stream":true,
		"tools":[{"name":"list_files","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"ls /tmp"}]}`, nil)
	defer resp.Body.Close()
	events := withoutPings(parseSSE(t, resp.Body))

	var start, delta sseEvent
	for _, e := range events {
		if e.name == "content_block_start" {
			start = e
		}
		if e.name == "content_block_delta" {
			delta = e
		}
	}
	cb := start.data["content_block"].(map[string]any)
	if cb["type"] != "tool_use" || cb["name"] != "list_files" || cb["id"] == "" {
		t.Fatalf("bad tool_use content_block_start: %v", cb)
	}
	d := delta.data["delta"].(map[string]any)
	if d["type"] != "input_json_delta" || !strings.Contains(d["partial_json"].(string), "/tmp") {
		t.Fatalf("bad input_json_delta: %v", d)
	}
	last := events[len(events)-2]
	if last.data["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Fatalf("streaming tool call must end with stop_reason=tool_use")
	}
}

func TestAuth(t *testing.T) {
	srv, _, _ := newTestServer(t, func(ollama.ChatRequest) chunkScript {
		return chunkScript{doneChunk("ok", nil, "stop")}
	})
	body := `{"model":"sonnet","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`

	tests := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no credentials", map[string]string{}, 401},
		{"empty bearer", map[string]string{"Authorization": "Bearer "}, 401},
		{"any bearer token", map[string]string{"Authorization": "Bearer whatever"}, 200},
		{"x-api-key", map[string]string{"x-api-key": "sk-local"}, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := post(t, srv, body, tt.headers)
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Errorf("status=%d, want %d", resp.StatusCode, tt.want)
			}
			if tt.want == 401 {
				var e APIError
				json.NewDecoder(resp.Body).Decode(&e)
				if e.Error.Type != "authentication_error" {
					t.Errorf("error type=%q", e.Error.Type)
				}
			}
		})
	}
}

func TestModelDiscoveryAndBudget(t *testing.T) {
	srv, _, _ := newTestServer(t, func(ollama.ChatRequest) chunkScript {
		return chunkScript{doneChunk("ok", nil, "stop")}
	})

	req, _ := http.NewRequest("GET", srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var models ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, m := range models.Data {
		ids[m.ID] = true
	}
	for _, want := range []string{"qwen3.5:9b", "claude-sonnet-5", "sonnet", "opus"} {
		if !ids[want] {
			t.Errorf("GET /v1/models missing id %q (got %v)", want, ids)
		}
	}

	// Stock Claude Code model-id variants resolve: dated suffixes, the
	// bracketed 1M-context marker, and both combined.
	for _, id := range []string{"claude-sonnet-5-20250929", "claude-sonnet-5[1m]", "claude-sonnet-5-20250929[1m]", "opus[1m]"} {
		r2 := post(t, srv, `{"model":"`+id+`","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
		if r2.StatusCode != 200 {
			t.Errorf("model id %q: status %d, want 200", id, r2.StatusCode)
		}
		r2.Body.Close()
	}

	// Over-budget model is refused before any Ollama call.
	r3 := post(t, srv, `{"model":"way-too-big:70b","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
	defer r3.Body.Close()
	if r3.StatusCode != 400 {
		t.Errorf("over-budget: status %d, want 400", r3.StatusCode)
	}
	b, _ := io.ReadAll(r3.Body)
	if !bytes.Contains(b, []byte("REFUSING")) {
		t.Errorf("over-budget error not loud: %s", b)
	}

	// Unknown model is a 404 with not_found_error.
	r4 := post(t, srv, `{"model":"gpt-6","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
	defer r4.Body.Close()
	if r4.StatusCode != 404 {
		t.Errorf("unknown model: status %d, want 404", r4.StatusCode)
	}
}

func TestMetricsInstrumentation(t *testing.T) {
	srv, _, m := newTestServer(t, func(ollama.ChatRequest) chunkScript {
		return chunkScript{doneChunk("ok", nil, "stop")}
	})
	resp := post(t, srv, `{"model":"sonnet","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
	resp.Body.Close()

	recent := m.Recent()
	if len(recent) != 1 {
		t.Fatalf("recent=%d, want 1", len(recent))
	}
	r := recent[0]
	if r.Model != "qwen3.5:9b" || r.Alias != "sonnet" {
		t.Errorf("record model/alias: %+v", r)
	}
	if r.TokensIn != 57 || r.TokensOut != 23 {
		t.Errorf("record tokens: %+v", r)
	}
	if r.DecodeTPS < 22 || r.DecodeTPS > 24 { // 23 tokens / 1s
		t.Errorf("record decode tps: %+v", r)
	}
	if r.TTFTMs != 150 { // 50ms load + 100ms prompt eval
		t.Errorf("record ttft: %+v", r)
	}
}

func TestStopReasonMapping(t *testing.T) {
	tests := []struct {
		doneReason string
		toolCalls  bool
		want       string
	}{
		{"stop", false, "end_turn"},
		{"length", false, "max_tokens"},
		{"stop", true, "tool_use"},
		{"", false, "end_turn"},
	}
	for _, tt := range tests {
		if got := mapStopReason(tt.doneReason, tt.toolCalls); got != tt.want {
			t.Errorf("mapStopReason(%q,%v)=%s, want %s", tt.doneReason, tt.toolCalls, got, tt.want)
		}
	}
}
