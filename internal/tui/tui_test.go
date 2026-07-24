package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/alileza/brokemode/internal/metrics"
	"github.com/alileza/brokemode/internal/registry"
	"github.com/alileza/brokemode/internal/telemetry"
)

func TestViewRenders(t *testing.T) {
	reg, err := registry.Parse([]byte("max_rss_gb: 11\nmodels:\n  - name: m\n    num_ctx: 1024\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(reg, "http://127.0.0.1:0", false)
	m.snap = telemetry.Snapshot{
		Time:              time.Now(),
		GPUActiveRatio:    0.42,
		MemoryPressurePct: 39,
		OllamaRSSBytes:    8 << 30,
		ThermalLevel:      28,
		CPUSpeedLimit:     72,
	}
	m.stream = metrics.StreamPayload{DecodeTPS: 19.5, TTFTMs: 480}
	m.fromAPI = true
	m.tpsHist = []float64{10, 15, 19.5}

	out := m.View()
	for _, want := range []string{"decode", "19.5 tok/s", "RSS 8.0/11GB", "GPU active", "mem pressure", "THROTTLED", "72%"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() missing %q\n%s", want, out)
		}
	}
}
