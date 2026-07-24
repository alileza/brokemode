package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/alileza/brokemode/internal/gateway"
	"github.com/alileza/brokemode/internal/metrics"
	"github.com/alileza/brokemode/internal/ollama"
)

// sharedMetrics is the process-wide metrics instance; gateway and serve
// share it when both run in one process.
var sharedMetrics = metrics.New()

func newGatewayCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Anthropic Messages API gateway for Claude Code, backed by Ollama",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			gw := &gateway.Server{
				Client:   ollama.New(flagOllamaHost),
				Registry: reg,
				Metrics:  sharedMetrics,
				Logger:   log.New(os.Stderr, "gateway: ", log.LstdFlags),
			}
			srv := &http.Server{
				Addr:              addr,
				Handler:           gw.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}
			log.Printf("gateway listening on %s (point Claude Code at ANTHROPIC_BASE_URL=http://%s)", addr, addr)
			return srv.ListenAndServe()
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:9100", "listen address")
	return cmd
}
