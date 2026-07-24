package main

import (
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/alileza/brokemode/internal/bench"
	"github.com/alileza/brokemode/internal/ollama"
)

func newBenchCmd() *cobra.Command {
	var (
		models   []string
		outDir   string
		warmups  int
		measured int
	)
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Benchmark models with the prompt suite (3 warmup + 5 measured runs)",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			targets := models
			if len(targets) == 0 {
				for _, m := range reg.Defaults() {
					targets = append(targets, m.Name)
				}
			}
			sudoOK := sudoAvailable()
			for _, name := range targets {
				model, err := reg.Resolve(name)
				if err != nil {
					return err
				}
				r := &bench.Runner{
					Client:   ollama.New(flagOllamaHost),
					Registry: reg,
					Model:    model,
					Warmups:  warmups,
					Measured: measured,
					OutDir:   outDir,
					Log:      os.Stdout,
					SudoOK:   sudoOK,
				}
				if _, err := r.Run(cmd.Context()); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&models, "model", nil, "model(s) to bench (default: registry defaults)")
	cmd.Flags().StringVar(&outDir, "results", "results", "output directory for JSON reports and summary.md")
	cmd.Flags().IntVar(&warmups, "warmups", 3, "warmup runs per case")
	cmd.Flags().IntVar(&measured, "runs", 5, "measured runs per case")
	return cmd
}

// sudoAvailable reports whether powermetrics can run without a password
// prompt. Never prompt mid-benchmark: -n fails fast instead.
func sudoAvailable() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	return exec.Command("sudo", "-n", "true").Run() == nil
}
