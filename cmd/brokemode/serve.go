package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/alileza/brokemode/internal/gateway"
	"github.com/alileza/brokemode/internal/metrics"
	"github.com/alileza/brokemode/internal/ollama"
	"github.com/alileza/brokemode/internal/registry"
	"github.com/alileza/brokemode/internal/telemetry"
	"github.com/alileza/brokemode/web"
)

func newServeCmd() *cobra.Command {
	var (
		addr        string
		gatewayAddr string
		withGateway bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Metrics + web dashboard on :9101 (and the gateway on :9100)",
		Long: `Serves /metrics (Prometheus), / (embedded dashboard), and /api/stream
(1Hz SSE telemetry). By default it also runs the Claude Code gateway in the
same process so the dashboard's request table sees live gateway traffic;
disable with --gateway=false if you run 'brokemode gateway' separately.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}

			// Host telemetry at 1Hz, folded into the mac_* gauges.
			collector := telemetry.NewCollector(sudoAvailable())
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			go collector.Run(ctx, sharedMetrics.UpdateHostGauges)

			warnOllamaVersion(ollama.New(flagOllamaHost), reg)
			if withGateway {
				gw := &gateway.Server{
					Client:   ollama.New(flagOllamaHost),
					Registry: reg,
					Metrics:  sharedMetrics,
					Logger:   log.New(os.Stderr, "gateway: ", log.LstdFlags),
				}
				gwSrv := &http.Server{Addr: gatewayAddr, Handler: gw.Handler(), ReadHeaderTimeout: 10 * time.Second}
				go func() {
					log.Printf("gateway listening on %s", gatewayAddr)
					if err := gwSrv.ListenAndServe(); err != http.ErrServerClosed {
						log.Printf("gateway: %v", err)
					}
				}()
				defer func() { _ = gwSrv.Close() }()
			}

			mux, err := serveMux(reg, sharedMetrics, collector)
			if err != nil {
				return err
			}
			srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
			log.Printf("dashboard on http://%s | /metrics | /api/stream", addr)
			return srv.ListenAndServe()
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:9101", "dashboard/metrics listen address")
	cmd.Flags().StringVar(&gatewayAddr, "gateway-addr", "127.0.0.1:9100", "gateway listen address")
	cmd.Flags().BoolVar(&withGateway, "gateway", true, "also run the Claude Code gateway in this process")
	return cmd
}

func payload(reg *registry.Registry, m *metrics.Metrics, c *telemetry.Collector) metrics.StreamPayload {
	last := m.LastRequest()
	return metrics.StreamPayload{
		Telemetry: c.Latest(),
		BudgetGB:  reg.MaxRSSGB,
		DecodeTPS: last.DecodeTPS,
		TTFTMs:    last.TTFTMs,
		Recent:    m.Recent(),
	}
}

func serveMux(reg *registry.Registry, m *metrics.Metrics, c *telemetry.Collector) (http.Handler, error) {
	dist, err := web.Dist()
	if err != nil {
		return nil, fmt.Errorf("embedded dashboard missing: %w (run `make build`)", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	mux.Handle("/", http.FileServerFS(dist))

	mux.HandleFunc("GET /api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload(reg, m, c))
	})

	mux.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			data, err := json.Marshal(payload(reg, m, c))
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "event: telemetry\ndata: %s\n\n", data); err != nil {
				return
			}
			fl.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
			}
		}
	})
	return mux, nil
}
