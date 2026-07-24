package telemetry

import (
	"context"
	"testing"
)

func fixedRunner(out string) runner {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(out), nil
	}
}

func TestMemoryPressureSampler(t *testing.T) {
	out := `The system has 2147483648 (524288 pages with a page size of 4096).

Stats:
Pages free: 51423
...
System-wide memory free percentage: 61%
`
	s := &MemoryPressureSampler{run: fixedRunner(out)}
	var snap Snapshot
	if err := s.Sample(context.Background(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.MemoryPressurePct != 39 {
		t.Errorf("MemoryPressurePct=%v, want 39", snap.MemoryPressurePct)
	}
}

func TestVMStatSampler(t *testing.T) {
	out := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               31415.
Pages active:                            271828.
Pages wired down:                        100000.
Pages occupied by compressor:             50000.
`
	s := &VMStatSampler{run: fixedRunner(out)}
	var snap Snapshot
	if err := s.Sample(context.Background(), &snap); err != nil {
		t.Fatal(err)
	}
	if want := uint64(100000 * 16384); snap.WiredBytes != want {
		t.Errorf("WiredBytes=%d, want %d", snap.WiredBytes, want)
	}
	if want := uint64(50000 * 16384); snap.CompressedBytes != want {
		t.Errorf("CompressedBytes=%d, want %d", snap.CompressedBytes, want)
	}
}

func TestRSSSampler(t *testing.T) {
	out := `  1024 /sbin/launchd
524288 /opt/homebrew/bin/ollama
3145728 /opt/homebrew/bin/ollama-runner
  2048 /usr/bin/top
`
	s := &RSSSampler{run: fixedRunner(out)}
	var snap Snapshot
	if err := s.Sample(context.Background(), &snap); err != nil {
		t.Fatal(err)
	}
	if want := uint64((524288 + 3145728) * 1024); snap.OllamaRSSBytes != want {
		t.Errorf("OllamaRSSBytes=%d, want %d", snap.OllamaRSSBytes, want)
	}
}

func TestThermalSampler(t *testing.T) {
	tests := []struct {
		out       string
		wantLevel int
		wantLimit int
	}{
		{"CPU_Scheduler_Limit = 100\nCPU_Available_CPUs = 8\nCPU_Speed_Limit = 100\n", 0, 100},
		{"CPU_Speed_Limit \t= 72\n", 28, 72},
		{"No thermal warning level info\n", 0, 100},
	}
	for _, tt := range tests {
		s := &ThermalSampler{run: fixedRunner(tt.out)}
		var snap Snapshot
		if err := s.Sample(context.Background(), &snap); err != nil {
			t.Fatal(err)
		}
		if snap.ThermalLevel != tt.wantLevel || snap.CPUSpeedLimit != tt.wantLimit {
			t.Errorf("out=%q: level=%d limit=%d, want %d/%d",
				tt.out, snap.ThermalLevel, snap.CPUSpeedLimit, tt.wantLevel, tt.wantLimit)
		}
	}
}

func TestPowerMetricsParse(t *testing.T) {
	p := NewPowerMetricsSampler()
	p.parseLine("GPU HW active residency:  42.51% (444 MHz: 12% 612 MHz: 30%)")
	p.parseLine("Combined Power (CPU + GPU + ANE): 5230 mW")
	if p.residency < 0.42 || p.residency > 0.43 {
		t.Errorf("residency=%v, want ~0.425", p.residency)
	}
	if p.powerW != 5.23 {
		t.Errorf("powerW=%v, want 5.23", p.powerW)
	}
}
