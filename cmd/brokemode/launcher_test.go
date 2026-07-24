package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alileza/brokemode/internal/ollama"
)

func TestIsPulled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "qwen3.5:9b"},
				{"name": "tiny:latest"},
			},
		})
	}))
	defer srv.Close()
	client := ollama.New(srv.URL)

	tests := []struct {
		name string
		want bool
	}{
		{"qwen3.5:9b", true},
		{"tiny", true}, // :latest suffix normalized
		{"gemma4:12b", false},
	}
	for _, tt := range tests {
		got, err := isPulled(context.Background(), client, tt.name)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("isPulled(%q)=%v, want %v", tt.name, got, tt.want)
		}
	}

	// Unreachable ollama is an error, not a silent "not pulled".
	dead := ollama.New("http://127.0.0.1:1")
	if _, err := isPulled(context.Background(), dead, "x"); err == nil {
		t.Error("unreachable ollama must surface an error")
	}
}
