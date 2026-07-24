package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/alileza/brokemode/internal/recommend"
	"github.com/alileza/brokemode/internal/registry"
)

// pullModel shells out to `ollama pull` with live output, translating the
// registry's 412 "requires a newer version of Ollama" refusal into an
// actionable message.
func pullModel(reg *registry.Registry, name string) error {
	m, err := reg.Resolve(name)
	if err != nil {
		return err
	}
	if err := reg.CheckBudget(m); err != nil {
		return err
	}
	fmt.Printf("pulling %s (%.1fGB on disk)\n", m.Name, m.DiskGB)
	var stderr bytes.Buffer
	cmd := exec.Command("ollama", "pull", m.Name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "requires a newer version of Ollama") {
			return fmt.Errorf("%s needs a newer Ollama than the installed one — run: brew upgrade ollama && brew services restart ollama (or install the latest from https://ollama.com/download), then retry", m.Name)
		}
		return fmt.Errorf("ollama pull %s: %w", m.Name, err)
	}
	return nil
}

// recommendedModel picks the best comfortable model for this machine,
// falling back to the registry's first default.
func recommendedModel(reg *registry.Registry) string {
	a := recommend.For(gatherHost(context.Background()), reg)
	if a.Recommended != "" {
		return a.Recommended
	}
	if d := reg.Defaults(); len(d) > 0 {
		return d[0].Name
	}
	return reg.Models[0].Name
}

func newPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull [model...]",
		Short: "Pull models via ollama (default: this machine's recommended pick)",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			targets := args
			if len(targets) == 0 {
				targets = []string{recommendedModel(reg)}
			}
			for _, name := range targets {
				if err := pullModel(reg, name); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
