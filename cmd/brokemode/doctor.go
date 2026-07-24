package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/alileza/brokemode/internal/ollama"
	"github.com/alileza/brokemode/internal/recommend"
	"github.com/alileza/brokemode/internal/telemetry"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine and recommend which models it can actually run",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			host := gatherHost(cmd.Context())
			a := recommend.For(host, reg)

			vctx, vcancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			defer vcancel()
			ollamaVer, verErr := ollama.New(flagOllamaHost).CheckServerVersion(vctx, reg.MinOllamaVersion)
			switch {
			case verErr == nil && ollamaVer != "":
				fmt.Printf("ollama server: v%s (>= v%s required)\n", ollamaVer, reg.MinOllamaVersion)
			case verErr != nil && ollamaVer != "":
				a.Warnings = append(a.Warnings, verErr.Error())
			case verErr != nil:
				a.Warnings = append(a.Warnings, fmt.Sprintf("ollama server unreachable: %v", verErr))
			}

			fmt.Printf("machine: %.0fGB unified memory, %d cores, %.1fGB free disk\n",
				host.TotalMemGB, host.CPUCores, host.FreeDiskGB)
			fmt.Printf("effective RSS budget: %.1fGB (min of max_rss_gb=%.1f and memory-%.0fGB reserved for macOS)\n\n",
				a.EffectiveBudgetGB, reg.MaxRSSGB, recommend.ReservedForOSGB)

			fmt.Printf("%-14s %-12s %s\n", "MODEL", "FIT", "WHY")
			fmt.Printf("%-14s %-12s %s\n", "-----", "---", "---")
			for _, ma := range a.Models {
				marker := ""
				if ma.Model.Name == a.Recommended {
					marker = "  ← recommended"
				} else if ma.Model.Name == a.FastPick && a.FastPick != a.Recommended {
					marker = "  ← fastest"
				}
				fmt.Printf("%-14s %-12s %s%s\n", ma.Model.Name, string(ma.Verdict), ma.Reason, marker)
			}

			fmt.Println()
			if a.Recommended != "" {
				fmt.Printf("recommendation: brokemode bench --model %s\n", a.Recommended)
				if a.FastPick != "" && a.FastPick != a.Recommended {
					fmt.Printf("fast lane:      brokemode bench --model %s\n", a.FastPick)
				}
			}
			for _, w := range a.Warnings {
				fmt.Fprintf(os.Stderr, "\n!!! WARNING: %s\n", w)
			}
			if a.Recommended == "" {
				return fmt.Errorf("no runnable model for this machine (see warnings above)")
			}
			return nil
		},
	}
}

// gatherHost collects what recommend.For needs; every probe degrades to an
// "unknown" value rather than failing.
func gatherHost(ctx context.Context) recommend.Host {
	host := recommend.Host{
		TotalMemGB:        totalMemGB(),
		FreeDiskGB:        freeDiskGB(),
		CPUCores:          runtime.NumCPU(),
		MemoryPressurePct: -1,
	}
	if runtime.GOOS == "darwin" {
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		var snap telemetry.Snapshot
		if err := telemetry.NewMemoryPressureSampler().Sample(sctx, &snap); err == nil {
			host.MemoryPressurePct = snap.MemoryPressurePct
		}
		if err := telemetry.NewThermalSampler().Sample(sctx, &snap); err == nil {
			host.ThermalLevel = snap.ThermalLevel
		}
	}
	return host
}

func totalMemGB() float64 {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err == nil {
			if b, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64); err == nil {
				return float64(b) / (1 << 30)
			}
		}
		return 0
	}
	// Linux fallback (CI, dev containers): MemTotal from /proc/meminfo.
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if kb, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			fields := strings.Fields(kb)
			if len(fields) > 0 {
				if v, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
					return float64(v) / (1 << 20)
				}
			}
		}
	}
	return 0
}

// freeDiskGB reports available space on the volume holding ~ (where both
// Ollama blobs and ~/.brokemode live). Returns -1 when unknown.
func freeDiskGB() float64 {
	home, err := os.UserHomeDir()
	if err != nil {
		return -1
	}
	var st unix.Statfs_t
	if err := unix.Statfs(home, &st); err != nil {
		return -1
	}
	return float64(st.Bavail) * float64(st.Bsize) / (1 << 30)
}
