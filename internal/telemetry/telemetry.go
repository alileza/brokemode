// Package telemetry samples macOS host metrics at 1Hz: GPU residency and
// package power (powermetrics), memory pressure, wired/compressed pages
// (vm_stat), the ollama runner's RSS, and thermal state (pmset).
package telemetry

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// Snapshot is one 1Hz observation. Zero values mean "not sampled".
type Snapshot struct {
	Time time.Time `json:"time"`

	GPUActiveRatio float64 `json:"gpu_active_ratio"` // 0..1
	PackagePowerW  float64 `json:"package_power_w"`

	MemoryPressurePct float64 `json:"memory_pressure_pct"` // 100 - free%
	WiredBytes        uint64  `json:"wired_bytes"`
	CompressedBytes   uint64  `json:"compressed_bytes"`

	OllamaRSSBytes uint64 `json:"ollama_rss_bytes"`

	ThermalLevel  int `json:"thermal_level"`   // 0 = nominal; 100-CPU_Speed_Limit
	CPUSpeedLimit int `json:"cpu_speed_limit"` // percent, 100 = no throttle
}

// Sampler is one telemetry source. Sample is called at 1Hz and merges its
// fields into the shared snapshot.
type Sampler interface {
	Name() string
	Sample(ctx context.Context, s *Snapshot) error
}

// runner executes a command and returns its stdout. Injectable for tests.
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Collector drives a set of samplers at a fixed interval.
type Collector struct {
	Samplers []Sampler
	Interval time.Duration

	mu       sync.Mutex
	latest   Snapshot
	warnings map[string]error
}

// NewCollector returns a 1Hz collector over the default macOS samplers.
// sudoOK enables the powermetrics sampler (it requires root).
func NewCollector(sudoOK bool) *Collector {
	samplers := []Sampler{
		NewMemoryPressureSampler(),
		NewVMStatSampler(),
		NewRSSSampler(),
		NewThermalSampler(),
	}
	if sudoOK {
		samplers = append(samplers, NewPowerMetricsSampler())
	}
	return &Collector{Samplers: samplers, Interval: time.Second, warnings: map[string]error{}}
}

// Run samples until ctx is done, invoking fn (if non-nil) after each tick.
// Individual sampler failures are recorded as warnings, never fatal — a
// bench run without sudo must still produce results, loudly marked partial.
func (c *Collector) Run(ctx context.Context, fn func(Snapshot)) {
	interval := c.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		snap := Snapshot{Time: time.Now()}
		for _, s := range c.Samplers {
			if err := s.Sample(ctx, &snap); err != nil {
				c.mu.Lock()
				c.warnings[s.Name()] = err
				c.mu.Unlock()
			}
		}
		c.mu.Lock()
		c.latest = snap
		c.mu.Unlock()
		if fn != nil {
			fn(snap)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Latest returns the most recent snapshot.
func (c *Collector) Latest() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest
}

// Warnings returns sampler-name -> last error for every source that failed
// at least once during the run.
func (c *Collector) Warnings() map[string]error {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]error, len(c.warnings))
	for k, v := range c.warnings {
		out[k] = v
	}
	return out
}
