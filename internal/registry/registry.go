// Package registry parses models.yaml and enforces the global RSS budget.
package registry

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Model is one entry in models.yaml.
type Model struct {
	Name         string   `yaml:"name"`
	Quantization string   `yaml:"quantization"`
	DiskGB       float64  `yaml:"disk_gb"`
	PeakRSSGB    float64  `yaml:"peak_rss_gb"`
	NumCtx       int      `yaml:"num_ctx"`
	Default      bool     `yaml:"default"`
	ExpectedTPS  float64  `yaml:"expected_tps"`
	Aliases      []string `yaml:"aliases"`
	UseWhen      string   `yaml:"use_when"`
}

// Registry is the parsed models.yaml plus the global RSS budget.
type Registry struct {
	MaxRSSGB float64 `yaml:"max_rss_gb"`
	Models   []Model `yaml:"models"`
}

// Load reads and validates a models.yaml file.
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes and validates models.yaml content.
func Parse(data []byte) (*Registry, error) {
	var r Registry
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse models.yaml: %w", err)
	}
	if r.MaxRSSGB <= 0 {
		return nil, fmt.Errorf("models.yaml: max_rss_gb must be > 0, got %v", r.MaxRSSGB)
	}
	if len(r.Models) == 0 {
		return nil, fmt.Errorf("models.yaml: no models defined")
	}
	seen := map[string]string{}
	for i, m := range r.Models {
		if m.Name == "" {
			return nil, fmt.Errorf("models.yaml: model %d has no name", i)
		}
		if m.NumCtx <= 0 {
			return nil, fmt.Errorf("models.yaml: %s has no num_ctx", m.Name)
		}
		for _, key := range append([]string{m.Name}, m.Aliases...) {
			if prev, dup := seen[key]; dup {
				return nil, fmt.Errorf("models.yaml: %q maps to both %s and %s", key, prev, m.Name)
			}
			seen[key] = m.Name
		}
	}
	return &r, nil
}

// DefaultPath looks for models.yaml next to the binary, in the CWD, and in
// ~/.brokemode, in that order.
func DefaultPath() (string, error) {
	candidates := []string{"models.yaml"}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "models.yaml"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".brokemode", "models.yaml"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("models.yaml not found (looked in CWD, binary dir, ~/.brokemode)")
}

// Resolve maps a model name or alias to its registry entry.
func (r *Registry) Resolve(nameOrAlias string) (*Model, error) {
	for i := range r.Models {
		m := &r.Models[i]
		if m.Name == nameOrAlias {
			return m, nil
		}
		for _, a := range m.Aliases {
			if a == nameOrAlias {
				return m, nil
			}
		}
	}
	return nil, fmt.Errorf("model %q not in registry (models.yaml)", nameOrAlias)
}

// CheckBudget returns an error if the model's measured peak RSS exceeds the
// global budget. Both install.sh and every CLI load path go through this.
func (r *Registry) CheckBudget(m *Model) error {
	if m.PeakRSSGB > r.MaxRSSGB {
		return fmt.Errorf("REFUSING to load %s: peak RSS %.1fGB exceeds the %.1fGB budget (max_rss_gb in models.yaml)",
			m.Name, m.PeakRSSGB, r.MaxRSSGB)
	}
	return nil
}

// Defaults returns the models marked default: true.
func (r *Registry) Defaults() []Model {
	var out []Model
	for _, m := range r.Models {
		if m.Default {
			out = append(out, m)
		}
	}
	return out
}
