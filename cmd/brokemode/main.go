// Command brokemode runs and benchmarks local LLMs on Apple Silicon.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	brokemode "github.com/alileza/brokemode"
	"github.com/alileza/brokemode/internal/registry"
)

// version is stamped by the release pipeline via
// -ldflags "-X main.version=v...".
var version = "dev"

var (
	flagModelsYAML string
	flagOllamaHost string
)

// registrySource resolves the raw models.yaml bytes: explicit flag, then
// the on-disk search path (CWD, binary dir, ~/.brokemode), then the copy
// embedded in the binary — so a bare release download always works.
func registrySource() ([]byte, string, error) {
	if flagModelsYAML != "" {
		data, err := os.ReadFile(flagModelsYAML)
		return data, flagModelsYAML, err
	}
	if path, err := registry.DefaultPath(); err == nil {
		data, err := os.ReadFile(path)
		return data, path, err
	}
	return brokemode.DefaultModelsYAML, "embedded", nil
}

func loadRegistry() (*registry.Registry, error) {
	data, source, err := registrySource()
	if err != nil {
		return nil, err
	}
	reg, err := registry.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return reg, nil
}

func main() {
	root := &cobra.Command{
		Use:           "brokemode",
		Short:         "Local LLMs on Apple Silicon — zero token cost",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Bare `brokemode` on a terminal opens the launcher; piped or
			// scripted invocations get the normal help.
			if stdinIsTTY() {
				return runLauncher(cmd)
			}
			return cmd.Help()
		},
	}
	root.PersistentFlags().StringVar(&flagModelsYAML, "models-yaml", "", "path to models.yaml (default: CWD, binary dir, ~/.brokemode, embedded)")
	root.PersistentFlags().StringVar(&flagOllamaHost, "ollama-host", "", "Ollama base URL (default: $OLLAMA_HOST or http://127.0.0.1:11434)")

	root.AddCommand(newBenchCmd())
	root.AddCommand(newGatewayCmd())
	root.AddCommand(newServeCmd())
	root.AddCommand(newTUICmd())
	root.AddCommand(newModelsCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newPullCmd())
	root.AddCommand(newUpdateCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "brokemode: %v\n", err)
		os.Exit(1)
	}
}

func newModelsCmd() *cobra.Command {
	var export bool
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Print the model registry and budget verdicts",
		RunE: func(cmd *cobra.Command, args []string) error {
			if export {
				// Raw models.yaml for scripts (install.sh seeds
				// ~/.brokemode/models.yaml from this on fresh machines).
				data, _, err := registrySource()
				if err != nil {
					return err
				}
				_, err = os.Stdout.Write(data)
				return err
			}
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			_, source, _ := registrySource()
			fmt.Printf("registry: %s\n\n", source)
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
	cmd.Flags().BoolVar(&export, "export", false, "print the raw models.yaml this binary resolves (for scripts)")
	return cmd
}
