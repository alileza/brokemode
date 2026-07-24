package metrics

import (
	"github.com/alileza/brokemode/internal/telemetry"
)

// StreamPayload is one frame of the /api/stream SSE feed and the
// /api/snapshot response; the web dashboard and the TUI both consume it.
type StreamPayload struct {
	Telemetry telemetry.Snapshot `json:"telemetry"`
	BudgetGB  float64            `json:"budget_gb"`
	DecodeTPS float64            `json:"decode_tps"`
	TTFTMs    float64            `json:"ttft_ms"`
	Recent    []RequestRecord    `json:"recent"`
}

// LastRequest returns the newest record, or a zero record.
func (m *Metrics) LastRequest() RequestRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.recent) == 0 {
		return RequestRecord{}
	}
	return m.recent[len(m.recent)-1]
}

// UpdateHostGauges folds a telemetry snapshot into the mac_* gauges.
func (m *Metrics) UpdateHostGauges(s telemetry.Snapshot) {
	m.GPUActiveRatio.Set(s.GPUActiveRatio)
	m.MemoryPressure.Set(s.MemoryPressurePct)
	m.ThermalLevel.Set(float64(s.ThermalLevel))
	if s.OllamaRSSBytes > 0 {
		m.ResidentBytes.Set(float64(s.OllamaRSSBytes))
	}
}
