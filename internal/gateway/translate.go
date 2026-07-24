package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alileza/brokemode/internal/ollama"
	"github.com/alileza/brokemode/internal/registry"
)

// newID returns an Anthropic-style random identifier like "msg_a1b2...".
func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// systemText flattens the request's system field (string or block list).
func systemText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("system must be a string or an array of text blocks")
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// contentBlocks normalizes a message's content (string or block array).
func contentBlocks(raw json.RawMessage) ([]ContentBlock, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []ContentBlock{{Type: "text", Text: s}}, nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("message content must be a string or an array of blocks")
	}
	return blocks, nil
}

// toolResultText flattens a tool_result's content (string or block list)
// into the plain string Ollama expects in a tool-role message.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return string(raw)
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// toolNameByID maps tool_use ids to tool names so tool_result turns can be
// labeled for Ollama, which addresses tools by name rather than id.
func toolNameByID(msgs []InMessage) map[string]string {
	out := map[string]string{}
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		blocks, err := contentBlocks(m.Content)
		if err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.ID != "" {
				out[b.ID] = b.Name
			}
		}
	}
	return out
}

// toOllamaChat translates an Anthropic Messages request into an Ollama
// /api/chat request, applying the registry model's num_ctx.
func toOllamaChat(req *MessagesRequest, model *registry.Model) (*ollama.ChatRequest, error) {
	out := &ollama.ChatRequest{Model: model.Name}

	sys, err := systemText(req.System)
	if err != nil {
		return nil, err
	}
	if sys != "" {
		out.Messages = append(out.Messages, ollama.Message{Role: "system", Content: sys})
	}

	names := toolNameByID(req.Messages)
	for i, m := range req.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			return nil, fmt.Errorf("messages[%d]: unsupported role %q", i, m.Role)
		}
		blocks, err := contentBlocks(m.Content)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}

		var text []string
		var toolCalls []ollama.ToolCall
		for _, b := range blocks {
			switch b.Type {
			case "text":
				text = append(text, b.Text)
			case "tool_use":
				args := b.Input
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				toolCalls = append(toolCalls, ollama.ToolCall{
					Function: ollama.ToolCallFunction{Name: b.Name, Arguments: args},
				})
			case "tool_result":
				// Each tool_result becomes its own tool-role message.
				out.Messages = append(out.Messages, ollama.Message{
					Role:     "tool",
					ToolName: names[b.ToolUseID],
					Content:  toolResultText(b.Content),
				})
			case "image":
				return nil, fmt.Errorf("messages[%d]: image blocks are not supported by the local gateway", i)
			default:
				// Ignore unknown block types (e.g. thinking) rather than
				// failing the whole request.
			}
		}
		if len(text) > 0 || len(toolCalls) > 0 {
			out.Messages = append(out.Messages, ollama.Message{
				Role:      m.Role,
				Content:   strings.Join(text, "\n"),
				ToolCalls: toolCalls,
			})
		}
	}

	for _, t := range req.Tools {
		out.Tools = append(out.Tools, ollama.Tool{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	opts := &ollama.Options{
		NumCtx:      model.NumCtx,
		NumPredict:  req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		TopK:        req.TopK,
		Stop:        req.StopSequences,
	}
	out.Options = opts
	return out, nil
}

// fromOllamaMessage converts an Ollama assistant message into Anthropic
// content blocks (text first, then tool_use blocks with fresh ids).
func fromOllamaMessage(msg ollama.Message) []ContentBlock {
	var blocks []ContentBlock
	if msg.Content != "" {
		blocks = append(blocks, ContentBlock{Type: "text", Text: msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		input := tc.Function.Arguments
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		blocks = append(blocks, ContentBlock{
			Type:  "tool_use",
			ID:    newID("toolu"),
			Name:  tc.Function.Name,
			Input: input,
		})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, ContentBlock{Type: "text", Text: ""})
	}
	return blocks
}

// mapStopReason translates Ollama's done_reason to Anthropic stop_reason.
func mapStopReason(doneReason string, hadToolCalls bool) string {
	if hadToolCalls {
		return "tool_use"
	}
	switch doneReason {
	case "length":
		return "max_tokens"
	case "stop", "":
		return "end_turn"
	default:
		return "end_turn"
	}
}
