package registry

import (
	"strings"
	"testing"
)

const sample = `
max_rss_gb: 11
models:
  - name: qwen3.5:9b
    quantization: Q4_K_M
    disk_gb: 5.9
    peak_rss_gb: 7.8
    num_ctx: 16384
    default: true
    expected_tps: 19
    aliases: [sonnet]
    use_when: "daily driver"
  - name: gemma4:12b
    quantization: Q4_K_M
    disk_gb: 7.6
    peak_rss_gb: 10.2
    num_ctx: 8192
    expected_tps: 13
    use_when: "max quality"
`

func TestParseAndResolve(t *testing.T) {
	r, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		query   string
		want    string
		wantErr bool
	}{
		{"qwen3.5:9b", "qwen3.5:9b", false},
		{"sonnet", "qwen3.5:9b", false},
		{"gemma4:12b", "gemma4:12b", false},
		{"gpt-5", "", true},
	}
	for _, tt := range tests {
		m, err := r.Resolve(tt.query)
		if tt.wantErr != (err != nil) {
			t.Errorf("Resolve(%q) err=%v, wantErr=%v", tt.query, err, tt.wantErr)
			continue
		}
		if err == nil && m.Name != tt.want {
			t.Errorf("Resolve(%q)=%s, want %s", tt.query, m.Name, tt.want)
		}
	}
}

func TestBudgetEnforcement(t *testing.T) {
	r, err := Parse([]byte(strings.Replace(sample, "max_rss_gb: 11", "max_rss_gb: 8", 1)))
	if err != nil {
		t.Fatal(err)
	}
	ok, _ := r.Resolve("qwen3.5:9b")
	if err := r.CheckBudget(ok); err != nil {
		t.Errorf("qwen3.5:9b (7.8GB) should fit an 8GB budget: %v", err)
	}
	over, _ := r.Resolve("gemma4:12b")
	err = r.CheckBudget(over)
	if err == nil {
		t.Fatal("gemma4:12b (10.2GB) must be refused under an 8GB budget")
	}
	if !strings.Contains(err.Error(), "REFUSING") {
		t.Errorf("budget error should be loud, got: %v", err)
	}
}

func TestValidation(t *testing.T) {
	bad := []string{
		"models: []",
		"max_rss_gb: 11",
		"max_rss_gb: 11\nmodels:\n  - name: a\n    num_ctx: 0",
		// duplicate alias across models
		"max_rss_gb: 11\nmodels:\n  - name: a\n    num_ctx: 1\n    aliases: [x]\n  - name: b\n    num_ctx: 1\n    aliases: [x]",
	}
	for i, y := range bad {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestDefaults(t *testing.T) {
	r, _ := Parse([]byte(sample))
	d := r.Defaults()
	if len(d) != 1 || d[0].Name != "qwen3.5:9b" {
		t.Errorf("Defaults()=%v, want [qwen3.5:9b]", d)
	}
}
