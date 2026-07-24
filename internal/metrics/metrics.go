// Package metrics owns the Prometheus registry and the ring buffer of
// recent gateway requests that the web dashboard renders.
package metrics

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics bundles every exported series. One instance per process.
type Metrics struct {
	reg *prometheus.Registry

	DecodeTPS     prometheus.Gauge
	TTFT          prometheus.Histogram
	ResidentBytes prometheus.Gauge

	GPUActiveRatio prometheus.Gauge
	MemoryPressure prometheus.Gauge
	ThermalLevel   prometheus.Gauge

	RequestsTotal     *prometheus.CounterVec
	InputTokensTotal  prometheus.Counter
	OutputTokensTotal prometheus.Counter

	mu     sync.Mutex
	recent []RequestRecord
}

// RequestRecord is one gateway request, kept for the dashboard table.
type RequestRecord struct {
	Time      time.Time `json:"time"`
	Model     string    `json:"model"`
	Alias     string    `json:"alias,omitempty"`
	Stream    bool      `json:"stream"`
	Status    int       `json:"status"`
	TTFTMs    float64   `json:"ttft_ms"`
	DecodeTPS float64   `json:"decode_tps"`
	TokensIn  int       `json:"tokens_in"`
	TokensOut int       `json:"tokens_out"`
	PeakRSS   uint64    `json:"peak_rss_bytes"`
}

const recentCap = 100

// New builds a fresh registry with every brokemode series registered.
func New() *Metrics {
	m := &Metrics{reg: prometheus.NewRegistry()}
	m.DecodeTPS = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "brokemode_decode_tokens_per_second",
		Help: "Decode rate of the most recent gateway request or bench run.",
	})
	m.TTFT = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "brokemode_ttft_seconds",
		Help:    "Time to first token per gateway request.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2, 5, 10, 30},
	})
	m.ResidentBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "brokemode_resident_bytes",
		Help: "Resident set size of the ollama processes.",
	})
	m.GPUActiveRatio = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "mac_gpu_active_ratio",
		Help: "GPU active residency, 0..1 (powermetrics).",
	})
	m.MemoryPressure = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "mac_memory_pressure_percent",
		Help: "System memory pressure percentage (100 - free%).",
	})
	m.ThermalLevel = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "mac_thermal_level",
		Help: "Thermal throttle level: 100 - CPU_Speed_Limit; 0 is nominal.",
	})
	m.RequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "brokemode_requests_total",
		Help: "Gateway requests by model and status code.",
	}, []string{"model", "status"})
	m.InputTokensTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "brokemode_input_tokens_total",
		Help: "Prompt tokens consumed across all gateway requests.",
	})
	m.OutputTokensTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "brokemode_output_tokens_total",
		Help: "Tokens generated across all gateway requests.",
	})

	m.reg.MustRegister(m.DecodeTPS, m.TTFT, m.ResidentBytes,
		m.GPUActiveRatio, m.MemoryPressure, m.ThermalLevel,
		m.RequestsTotal, m.InputTokensTotal, m.OutputTokensTotal)
	return m
}

// Handler serves the /metrics endpoint for this registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// RecordRequest folds one finished gateway request into every series and
// the recent-request ring buffer.
func (m *Metrics) RecordRequest(r RequestRecord) {
	m.RequestsTotal.WithLabelValues(r.Model, http.StatusText(r.Status)).Inc()
	if r.TTFTMs > 0 {
		m.TTFT.Observe(r.TTFTMs / 1000)
	}
	if r.DecodeTPS > 0 {
		m.DecodeTPS.Set(r.DecodeTPS)
	}
	if r.PeakRSS > 0 {
		m.ResidentBytes.Set(float64(r.PeakRSS))
	}
	m.InputTokensTotal.Add(float64(r.TokensIn))
	m.OutputTokensTotal.Add(float64(r.TokensOut))

	m.mu.Lock()
	defer m.mu.Unlock()
	m.recent = append(m.recent, r)
	if len(m.recent) > recentCap {
		m.recent = m.recent[len(m.recent)-recentCap:]
	}
}

// Recent returns the newest-first request records.
func (m *Metrics) Recent() []RequestRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RequestRecord, len(m.recent))
	for i, r := range m.recent {
		out[len(m.recent)-1-i] = r
	}
	return out
}
