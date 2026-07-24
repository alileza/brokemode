// Command brokemode runs and benchmarks local LLMs on Apple Silicon.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/alileza/brokemode/internal/registry"
)

var (
	flagModelsYAML string
	flagOllamaHost string
)

func loadRegistry() (*registry.Registry, error) {
	path := flagModelsYAML
	if path == "" {
		var err error
		path, err = registry.DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	return registry.Load(path)
}

func main() {
	root := &cobra.Command{
		Use:           "brokemode",
		Short:         "Local LLMs on Apple Silicon — zero token cost",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&flagModelsYAML, "models-yaml", "", "path to models.yaml (default: CWD, binary dir, ~/.brokemode)")
	root.PersistentFlags().StringVar(&flagOllamaHost, "ollama-host", "", "Ollama base URL (default: $OLLAMA_HOST or http://127.0.0.1:11434)")

	root.AddCommand(newBenchCmd())
	root.AddCommand(newGatewayCmd())
	root.AddCommand(newServeCmd())
	root.AddCommand(newTUICmd())
	root.AddCommand(newModelsCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "brokemode: %v\n", err)
		os.Exit(1)
	}
}

func newModelsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "Print the model registry and budget verdicts",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			fmt.Printf("%-14s %-8s %10s %12s %8s %8s  %s\n", "MODEL", "QUANT", "DISK(GB)", "PEAK RSS(GB)", "NUM_CTX", "BUDGET", "USE WHEN")
			for i := range reg.Models {
				m := &reg.Models[i]
				verdict := "ok"
				if err := reg.CheckBudget(m); err != nil {
					verdict = "OVER"
				}
				fmt.Printf("%-14s %-8s %10.1f %12.1f %8d %8s  %s\n",
					m.Name, m.Quantization, m.DiskGB, m.PeakRSSGB, m.NumCtx, verdict, m.UseWhen)
			}
			fmt.Printf("\nbudget: max_rss_gb = %.1f\n", reg.MaxRSSGB)
			return nil
		},
	}
}
