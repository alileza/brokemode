package telemetry

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ---------------------------------------------------------------- powermetrics

var (
	reGPUResidency = regexp.MustCompile(`GPU (?:HW )?active residency:\s+([\d.]+)%`)
	rePackagePower = regexp.MustCompile(`(?:Combined|Package) Power[^:]*:\s+([\d.]+)\s*mW`)
	reGPUPower     = regexp.MustCompile(`GPU Power:\s+([\d.]+)\s*mW`)
)

// PowerMetricsSampler streams `sudo powermetrics --samplers gpu_power -i 1000`
// and keeps the latest parsed GPU residency and package power. powermetrics
// requires root; construction succeeds but the first Sample returns a clear
// error when sudo -n is unavailable so callers can mark the run partial.
type PowerMetricsSampler struct {
	mu         sync.Mutex
	started    bool
	startErr   error
	residency  float64
	powerW     float64
	sawSample  bool
	cancelProc context.CancelFunc
}

// NewPowerMetricsSampler returns an unstarted powermetrics sampler.
func NewPowerMetricsSampler() *PowerMetricsSampler { return &PowerMetricsSampler{} }

// Name implements Sampler.
func (p *PowerMetricsSampler) Name() string { return "powermetrics" }

func (p *PowerMetricsSampler) start(ctx context.Context) {
	// Refuse to hang on a password prompt: -n fails fast without a cached
	// sudo timestamp or NOPASSWD rule.
	procCtx, cancel := context.WithCancel(context.Background())
	p.cancelProc = cancel
	go func() { <-ctx.Done(); cancel() }()

	cmd := exec.CommandContext(procCtx, "sudo", "-n", "powermetrics", "--samplers", "gpu_power", "-i", "1000")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.startErr = err
		return
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		p.startErr = fmt.Errorf("start sudo powermetrics: %w", err)
		return
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			p.parseLine(sc.Text())
		}
		_ = cmd.Wait()
		p.mu.Lock()
		defer p.mu.Unlock()
		if !p.sawSample {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = "no output (sudo unavailable or powermetrics missing)"
			}
			p.startErr = fmt.Errorf("powermetrics exited: %s", msg)
		}
	}()
}

func (p *PowerMetricsSampler) parseLine(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m := reGPUResidency.FindStringSubmatch(line); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			p.residency = v / 100.0
			p.sawSample = true
		}
	}
	if m := rePackagePower.FindStringSubmatch(line); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			p.powerW = v / 1000.0
			p.sawSample = true
		}
	}
	// gpu_power sampler emits GPU Power but not always package power;
	// fall back so the field is never silently zero while GPU data flows.
	if m := reGPUPower.FindStringSubmatch(line); m != nil && p.powerW == 0 {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			p.powerW = v / 1000.0
			p.sawSample = true
		}
	}
}

// Sample implements Sampler. It starts the powermetrics stream on first call.
func (p *PowerMetricsSampler) Sample(ctx context.Context, s *Snapshot) error {
	p.mu.Lock()
	if !p.started {
		p.started = true
		p.mu.Unlock()
		p.start(ctx)
		p.mu.Lock()
	}
	defer p.mu.Unlock()
	if p.startErr != nil && !p.sawSample {
		return fmt.Errorf("GPU/power telemetry unavailable: %w", p.startErr)
	}
	s.GPUActiveRatio = p.residency
	s.PackagePowerW = p.powerW
	if !p.sawSample {
		return fmt.Errorf("powermetrics has produced no samples yet")
	}
	return nil
}

// ---------------------------------------------------------------- memory_pressure

var reFreePct = regexp.MustCompile(`System-wide memory free percentage:\s+(\d+)%`)

// MemoryPressureSampler runs `memory_pressure` and reports pressure as
// 100 - free%.
type MemoryPressureSampler struct{ run runner }

// NewMemoryPressureSampler returns the default memory_pressure sampler.
func NewMemoryPressureSampler() *MemoryPressureSampler {
	return &MemoryPressureSampler{run: execRunner}
}

// Name implements Sampler.
func (m *MemoryPressureSampler) Name() string { return "memory_pressure" }

// Sample implements Sampler.
func (m *MemoryPressureSampler) Sample(ctx context.Context, s *Snapshot) error {
	out, err := m.run(ctx, "memory_pressure")
	if err != nil {
		return fmt.Errorf("memory_pressure: %w", err)
	}
	match := reFreePct.FindSubmatch(out)
	if match == nil {
		return fmt.Errorf("memory_pressure: free percentage not found in output")
	}
	free, _ := strconv.Atoi(string(match[1]))
	s.MemoryPressurePct = float64(100 - free)
	return nil
}

// ---------------------------------------------------------------- vm_stat

var (
	rePageSize   = regexp.MustCompile(`page size of (\d+) bytes`)
	reWired      = regexp.MustCompile(`Pages wired down:\s+(\d+)\.`)
	reCompressor = regexp.MustCompile(`Pages occupied by compressor:\s+(\d+)\.`)
)

// VMStatSampler runs `vm_stat` for wired and compressed page counts.
type VMStatSampler struct{ run runner }

// NewVMStatSampler returns the default vm_stat sampler.
func NewVMStatSampler() *VMStatSampler { return &VMStatSampler{run: execRunner} }

// Name implements Sampler.
func (v *VMStatSampler) Name() string { return "vm_stat" }

// Sample implements Sampler.
func (v *VMStatSampler) Sample(ctx context.Context, s *Snapshot) error {
	out, err := v.run(ctx, "vm_stat")
	if err != nil {
		return fmt.Errorf("vm_stat: %w", err)
	}
	pageSize := uint64(16384) // Apple Silicon default
	if m := rePageSize.FindSubmatch(out); m != nil {
		if v, err := strconv.ParseUint(string(m[1]), 10, 64); err == nil {
			pageSize = v
		}
	}
	if m := reWired.FindSubmatch(out); m != nil {
		pages, _ := strconv.ParseUint(string(m[1]), 10, 64)
		s.WiredBytes = pages * pageSize
	}
	if m := reCompressor.FindSubmatch(out); m != nil {
		pages, _ := strconv.ParseUint(string(m[1]), 10, 64)
		s.CompressedBytes = pages * pageSize
	}
	return nil
}

// ---------------------------------------------------------------- ollama RSS

// RSSSampler sums the resident set size of every ollama process (the server
// and its model runner children) via ps.
type RSSSampler struct{ run runner }

// NewRSSSampler returns the default ollama RSS sampler.
func NewRSSSampler() *RSSSampler { return &RSSSampler{run: execRunner} }

// Name implements Sampler.
func (r *RSSSampler) Name() string { return "ollama_rss" }

// Sample implements Sampler.
func (r *RSSSampler) Sample(ctx context.Context, s *Snapshot) error {
	out, err := r.run(ctx, "ps", "axo", "rss=,comm=")
	if err != nil {
		return fmt.Errorf("ps: %w", err)
	}
	var totalKB uint64
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		comm := strings.ToLower(fields[len(fields)-1])
		if !strings.Contains(comm, "ollama") {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		totalKB += kb
	}
	s.OllamaRSSBytes = totalKB * 1024
	return nil
}

// ---------------------------------------------------------------- pmset therm

var reSpeedLimit = regexp.MustCompile(`CPU_Speed_Limit\s*=\s*(\d+)`)

// ThermalSampler runs `pmset -g therm`. ThermalLevel is 100-CPU_Speed_Limit,
// so 0 means nominal and anything above 0 means the SoC is throttling.
type ThermalSampler struct{ run runner }

// NewThermalSampler returns the default pmset thermal sampler.
func NewThermalSampler() *ThermalSampler { return &ThermalSampler{run: execRunner} }

// Name implements Sampler.
func (t *ThermalSampler) Name() string { return "pmset_therm" }

// Sample implements Sampler.
func (t *ThermalSampler) Sample(ctx context.Context, s *Snapshot) error {
	out, err := t.run(ctx, "pmset", "-g", "therm")
	if err != nil {
		return fmt.Errorf("pmset -g therm: %w", err)
	}
	m := reSpeedLimit.FindSubmatch(out)
	if m == nil {
		// Older pmset output without CPU_Speed_Limit: treat as nominal.
		s.CPUSpeedLimit = 100
		s.ThermalLevel = 0
		return nil
	}
	limit, _ := strconv.Atoi(string(m[1]))
	s.CPUSpeedLimit = limit
	s.ThermalLevel = 100 - limit
	return nil
}
