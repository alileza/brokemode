// Package gateway implements the Anthropic Messages API surface Claude
// Code requires, translated onto a local Ollama server.
package gateway

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/alileza/brokemode/internal/metrics"
	"github.com/alileza/brokemode/internal/ollama"
	"github.com/alileza/brokemode/internal/registry"
	"github.com/alileza/brokemode/internal/telemetry"
)

// Server is the gateway HTTP server.
type Server struct {
	Client   *ollama.Client
	Registry *registry.Registry
	Metrics  *metrics.Metrics
	Logger   *log.Logger
}

// Handler returns the full gateway mux with auth applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", s.handleMessages)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return s.auth(mux)
}

// auth accepts any non-empty bearer token or x-api-key — the gateway is
// local; the check only keeps random LAN scanners honest.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
		if token == "" {
			token = strings.TrimSpace(r.Header.Get("x-api-key"))
		}
		if token == "" {
			s.writeError(w, http.StatusUnauthorized, "authentication_error",
				"missing credentials: send any non-empty Authorization: Bearer token or x-api-key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger.Printf(format, args...)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, typ, msg string) {
	var e APIError
	e.Type = "error"
	e.Error.Type = typ
	e.Error.Message = msg
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}

// resolveModel maps the requested model through models.yaml. Besides exact
// name/alias matches it accepts dated variants (claude-sonnet-5-20250929
// matches alias claude-sonnet-5) so stock Claude Code model ids work.
func (s *Server) resolveModel(requested string) (*registry.Model, error) {
	if m, err := s.Registry.Resolve(requested); err == nil {
		return m, nil
	}
	for i := range s.Registry.Models {
		m := &s.Registry.Models[i]
		for _, alias := range append([]string{m.Name}, m.Aliases...) {
			if strings.HasPrefix(requested, alias+"-") {
				return m, nil
			}
		}
	}
	return s.Registry.Resolve(requested) // return the original error
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	resp := ModelsResponse{Data: []ModelInfo{}}
	for _, m := range s.Registry.Models {
		for _, id := range append([]string{m.Name}, m.Aliases...) {
			resp.Data = append(resp.Data, ModelInfo{
				Type:        "model",
				ID:          id,
				DisplayName: m.Name + " (" + m.Quantization + ", local)",
				CreatedAt:   "2026-01-01T00:00:00Z",
			})
		}
	}
	if n := len(resp.Data); n > 0 {
		resp.FirstID = &resp.Data[0].ID
		resp.LastID = &resp.Data[n-1].ID
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// rssWatcher samples ollama RSS every 500ms and keeps the peak observed
// during a single gateway request.
type rssWatcher struct {
	cancel context.CancelFunc
	done   chan struct{}
	peak   uint64
}

func watchRSS(parent context.Context) *rssWatcher {
	ctx, cancel := context.WithCancel(parent)
	w := &rssWatcher{cancel: cancel, done: make(chan struct{})}
	sampler := telemetry.NewRSSSampler()
	go func() {
		defer close(w.done)
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			var snap telemetry.Snapshot
			if err := sampler.Sample(ctx, &snap); err == nil && snap.OllamaRSSBytes > w.peak {
				w.peak = snap.OllamaRSSBytes
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return w
}

func (w *rssWatcher) stop() uint64 {
	w.cancel()
	<-w.done
	return w.peak
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	var req MessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON: "+err.Error())
		return
	}
	if req.Model == "" || len(req.Messages) == 0 {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "model and messages are required")
		return
	}

	model, err := s.resolveModel(req.Model)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "not_found_error", err.Error())
		return
	}
	if err := s.Registry.CheckBudget(model); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	chatReq, err := toOllamaChat(&req, model)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	rec := metrics.RequestRecord{
		Time:   time.Now(),
		Model:  model.Name,
		Alias:  req.Model,
		Stream: req.Stream,
	}
	watcher := watchRSS(r.Context())
	start := time.Now()

	if req.Stream {
		s.streamMessages(w, r, chatReq, &req, &rec, start)
	} else {
		s.completeMessages(w, r, chatReq, &req, &rec, start)
	}

	rec.PeakRSS = watcher.stop()
	if s.Metrics != nil {
		s.Metrics.RecordRequest(rec)
	}
	s.logf("%s %s stream=%v status=%d ttft=%.0fms decode=%.1ftok/s in=%d out=%d",
		req.Model, model.Name, req.Stream, rec.Status, rec.TTFTMs, rec.DecodeTPS, rec.TokensIn, rec.TokensOut)
}

func (s *Server) completeMessages(w http.ResponseWriter, r *http.Request, chatReq *ollama.ChatRequest, req *MessagesRequest, rec *metrics.RequestRecord, start time.Time) {
	resp, err := s.Client.Chat(r.Context(), *chatReq)
	if err != nil {
		rec.Status = http.StatusBadGateway
		s.writeError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}

	blocks := fromOllamaMessage(resp.Message)
	out := MessagesResponse{
		ID:         newID("msg"),
		Type:       "message",
		Role:       "assistant",
		Model:      req.Model,
		Content:    blocks,
		StopReason: mapStopReason(resp.DoneReason, len(resp.Message.ToolCalls) > 0),
		Usage:      Usage{InputTokens: resp.PromptEvalCount, OutputTokens: resp.EvalCount},
	}

	rec.Status = http.StatusOK
	rec.TTFTMs = float64(resp.LoadDuration+resp.PromptEvalDuration) / float64(time.Millisecond)
	if rec.TTFTMs == 0 {
		rec.TTFTMs = float64(time.Since(start)) / float64(time.Millisecond)
	}
	if sec := resp.EvalDuration.Seconds(); sec > 0 {
		rec.DecodeTPS = float64(resp.EvalCount) / sec
	}
	rec.TokensIn = resp.PromptEvalCount
	rec.TokensOut = resp.EvalCount

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
