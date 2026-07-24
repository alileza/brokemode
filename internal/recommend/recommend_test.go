package recommend

import (
	"strings"
	"testing"

	"github.com/alileza/brokemode/internal/registry"
)

func testReg(t *testing.T) *registry.Registry {
	t.Helper()
	r, err := registry.Parse([]byte(`
max_rss_gb: 11
models:
  - name: qwen3.5:9b
    disk_gb: 5.9
    peak_rss_gb: 7.8
    num_ctx: 16384
    expected_tps: 19
  - name: qwen3.5:4b
    disk_gb: 2.6
    peak_rss_gb: 4.1
    num_ctx: 32768
    expected_tps: 42
  - name: gemma4:12b
    disk_gb: 7.6
    peak_rss_gb: 10.2
    num_ctx: 8192
    expected_tps: 13
  - name: gemma4:e4b
    disk_gb: 3.1
    peak_rss_gb: 3.4
    num_ctx: 32768
    expected_tps: 55
`))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func verdictOf(a Advice, name string) ModelAdvice {
	for _, m := range a.Models {
		if m.Model.Name == name {
			return m
		}
	}
	return ModelAdvice{}
}

func TestSixteenGBMachine(t *testing.T) {
	a := For(Host{TotalMemGB: 16, FreeDiskGB: 100, CPUCores: 8, MemoryPressurePct: -1}, testReg(t))
	if a.EffectiveBudgetGB != 11 {
		t.Errorf("budget=%v, want 11 (16-5)", a.EffectiveBudgetGB)
	}
	if a.Recommended != "qwen3.5:9b" {
		t.Errorf("recommended=%q, want qwen3.5:9b (heaviest comfortable)", a.Recommended)
	}
	if a.FastPick != "gemma4:e4b" {
		t.Errorf("fast pick=%q, want gemma4:e4b", a.FastPick)
	}
	if v := verdictOf(a, "gemma4:12b").Verdict; v != Tight {
		t.Errorf("gemma4:12b on 16GB = %s, want tight (10.2 vs 11 budget)", v)
	}
	if len(a.Warnings) != 0 {
		t.Errorf("healthy 16GB machine should have no warnings: %v", a.Warnings)
	}
}

func TestEightGBMachine(t *testing.T) {
	a := For(Host{TotalMemGB: 8, FreeDiskGB: 100, CPUCores: 8, MemoryPressurePct: -1}, testReg(t))
	if a.EffectiveBudgetGB != 3 {
		t.Errorf("budget=%v, want 3 (8-5)", a.EffectiveBudgetGB)
	}
	if v := verdictOf(a, "qwen3.5:9b").Verdict; v != NoFit {
		t.Errorf("9b on 8GB = %s, want no-fit", v)
	}
	// e4b (3.4GB) is over the 3GB budget too -> nothing fits; warnings say so.
	joined := strings.Join(a.Warnings, " | ")
	if !strings.Contains(joined, "below the 16GB") {
		t.Errorf("missing low-memory warning: %v", a.Warnings)
	}
	if a.Recommended != "" {
		t.Errorf("nothing should be recommended on 8GB, got %q", a.Recommended)
	}
	if !strings.Contains(joined, "no registry model fits") {
		t.Errorf("missing nothing-fits warning: %v", a.Warnings)
	}
}

func TestTwelveGBTightFallback(t *testing.T) {
	// 12GB: budget 7 — 4b comfortable, 9b over budget, e4b comfortable.
	a := For(Host{TotalMemGB: 12, FreeDiskGB: 100, CPUCores: 8, MemoryPressurePct: -1}, testReg(t))
	if a.Recommended != "qwen3.5:4b" {
		t.Errorf("recommended=%q, want qwen3.5:4b", a.Recommended)
	}
	if v := verdictOf(a, "qwen3.5:9b").Verdict; v != NoFit {
		t.Errorf("9b (7.8) on 7GB budget = %s, want no-fit", v)
	}
}

func TestLowDiskWarnings(t *testing.T) {
	a := For(Host{TotalMemGB: 16, FreeDiskGB: 4, CPUCores: 8, MemoryPressurePct: -1}, testReg(t))
	// 4GB free: even e4b needs 3.1+2=5.1GB.
	nine := verdictOf(a, "qwen3.5:9b")
	if nine.DiskOK {
		t.Error("9b needs 7.9GB free disk, only 4 available")
	}
	if !strings.Contains(nine.Reason, "free up at least") {
		t.Errorf("disk reason must say how much to free: %s", nine.Reason)
	}
	if a.Recommended != "" {
		t.Errorf("no disk-ok model should be recommended, got %q", a.Recommended)
	}
	if !strings.Contains(strings.Join(a.Warnings, " "), "free up at least") {
		t.Errorf("missing global disk warning: %v", a.Warnings)
	}
}

func TestPressureAndThermalWarnings(t *testing.T) {
	a := For(Host{TotalMemGB: 16, FreeDiskGB: 100, CPUCores: 6, MemoryPressurePct: 72, ThermalLevel: 15}, testReg(t))
	joined := strings.Join(a.Warnings, " | ")
	for _, want := range []string{"memory pressure", "thermally throttled", "CPU cores"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q warning: %v", want, a.Warnings)
		}
	}
}

func TestBigMachineKeepsConfiguredCap(t *testing.T) {
	// 32GB machine: budget stays at the registry's max_rss_gb cap.
	a := For(Host{TotalMemGB: 32, FreeDiskGB: 100, CPUCores: 10, MemoryPressurePct: -1}, testReg(t))
	if a.EffectiveBudgetGB != 11 {
		t.Errorf("budget=%v, want 11 (configured cap)", a.EffectiveBudgetGB)
	}
	if a.Recommended != "qwen3.5:9b" {
		t.Errorf("recommended=%q, want qwen3.5:9b (gemma4:12b is only a tight fit at the 11GB cap)", a.Recommended)
	}
	if v := verdictOf(a, "gemma4:12b").Verdict; v != Tight {
		t.Errorf("gemma4:12b under the 11GB cap = %s, want tight", v)
	}
}
