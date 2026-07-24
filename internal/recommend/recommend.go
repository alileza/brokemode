// Package recommend classifies registry models against the actual machine
// (unified memory, free disk, cores, live pressure) and picks what to run.
package recommend

import (
	"fmt"
	"sort"

	"github.com/alileza/brokemode/internal/registry"
)

// ReservedForOSGB is what macOS, WindowServer, and a browser realistically
// hold before Ollama loads anything.
const ReservedForOSGB = 5.0

// ComfortHeadroomGB separates "comfortable" from "tight": room for KV
// cache growth and a spike without waking the compressor.
const ComfortHeadroomGB = 1.5

// DiskHeadroomGB is extra free disk required beyond a model's blob size.
const DiskHeadroomGB = 2.0

// Host describes the machine being advised. Unknown values: FreeDiskGB
// and MemoryPressurePct < 0, CPUCores 0.
type Host struct {
	TotalMemGB        float64
	FreeDiskGB        float64
	CPUCores          int
	MemoryPressurePct float64
	ThermalLevel      int
}

// Verdict is a model's fit classification on this host.
type Verdict string

// Fit classifications, best to worst.
const (
	Comfortable Verdict = "comfortable"
	Tight       Verdict = "tight"
	NoFit       Verdict = "no-fit"
)

// ModelAdvice is one model's verdict on this host.
type ModelAdvice struct {
	Model   registry.Model
	Verdict Verdict
	DiskOK  bool
	Reason  string
}

// Advice is the full recommendation for a host.
type Advice struct {
	EffectiveBudgetGB float64
	Recommended       string // best-quality comfortable pick
	FastPick          string // highest tok/s comfortable pick
	Models            []ModelAdvice
	Warnings          []string
}

// For computes model advice for a host against the registry. The effective
// RSS budget is min(max_rss_gb, total memory - ReservedForOSGB) so a
// smaller machine automatically tightens the registry's tuned budget.
func For(host Host, reg *registry.Registry) Advice {
	budget := host.TotalMemGB - ReservedForOSGB
	if budget < 0 {
		budget = 0
	}
	if reg.MaxRSSGB < budget {
		budget = reg.MaxRSSGB
	}
	a := Advice{EffectiveBudgetGB: budget}

	// 15.5 not 16: the OS reports usable memory slightly under the
	// marketing size; a real 16GB box must not trip its own warning.
	if host.TotalMemGB > 0 && host.TotalMemGB < 15.5 {
		a.Warnings = append(a.Warnings, fmt.Sprintf(
			"%.0fGB unified memory is below the 16GB this registry is tuned for — only the smallest models will run, and macOS will fight them for pages",
			host.TotalMemGB))
	}
	if host.CPUCores > 0 && host.CPUCores < 8 {
		a.Warnings = append(a.Warnings, fmt.Sprintf(
			"%d CPU cores detected — prefill on long prompts will be noticeably slower than the registry's expected rates", host.CPUCores))
	}
	if host.MemoryPressurePct >= 60 {
		a.Warnings = append(a.Warnings, fmt.Sprintf(
			"memory pressure is already at %.0f%% — close other apps before loading a model or decode will crawl", host.MemoryPressurePct))
	}
	if host.ThermalLevel > 0 {
		a.Warnings = append(a.Warnings,
			"the machine is currently thermally throttled — benchmark numbers will be misleading until it cools down")
	}

	var minDiskNeed float64 = -1
	for _, m := range reg.Models {
		ma := ModelAdvice{Model: m, DiskOK: true}
		switch {
		case m.PeakRSSGB <= budget-ComfortHeadroomGB:
			ma.Verdict = Comfortable
			ma.Reason = fmt.Sprintf("%.1fGB RSS leaves %.1fGB headroom", m.PeakRSSGB, budget-m.PeakRSSGB)
		case m.PeakRSSGB <= budget:
			ma.Verdict = Tight
			ma.Reason = fmt.Sprintf("%.1fGB RSS against a %.1fGB budget — close everything else first", m.PeakRSSGB, budget)
		default:
			ma.Verdict = NoFit
			ma.Reason = fmt.Sprintf("needs ~%.0fGB total memory (%.1fGB RSS + %.0fGB for macOS); this machine has %.0fGB",
				m.PeakRSSGB+ReservedForOSGB, m.PeakRSSGB, ReservedForOSGB, host.TotalMemGB)
		}
		if host.FreeDiskGB >= 0 {
			need := m.DiskGB + DiskHeadroomGB
			if host.FreeDiskGB < need {
				ma.DiskOK = false
				short := need - host.FreeDiskGB
				ma.Reason += fmt.Sprintf("; needs %.1fGB free disk, only %.1fGB available — free up at least %.1fGB",
					need, host.FreeDiskGB, short)
				if ma.Verdict != NoFit && (minDiskNeed < 0 || short < minDiskNeed) {
					minDiskNeed = short
				}
			}
		}
		a.Models = append(a.Models, ma)
	}

	// Best-quality pick: the heaviest comfortable model (peak RSS tracks
	// parameter count, our quality proxy). Fast pick: highest expected tok/s.
	pick := func(better func(x, y registry.Model) bool) string {
		var best *registry.Model
		for i := range a.Models {
			ma := &a.Models[i]
			if ma.Verdict != Comfortable || !ma.DiskOK {
				continue
			}
			if best == nil || better(ma.Model, *best) {
				best = &ma.Model
			}
		}
		if best == nil {
			return ""
		}
		return best.Name
	}
	a.Recommended = pick(func(x, y registry.Model) bool { return x.PeakRSSGB > y.PeakRSSGB })
	a.FastPick = pick(func(x, y registry.Model) bool { return x.ExpectedTPS > y.ExpectedTPS })

	if a.Recommended == "" {
		// Fall back to a tight fit before declaring the machine hopeless.
		for _, ma := range a.Models {
			if ma.Verdict == Tight && ma.DiskOK {
				a.Recommended = ma.Model.Name
				a.Warnings = append(a.Warnings, fmt.Sprintf(
					"no model fits comfortably; %s is a tight fit — run it alone", ma.Model.Name))
				break
			}
		}
	}
	if a.Recommended == "" {
		a.Warnings = append(a.Warnings,
			"no registry model fits this machine's memory budget — this setup needs more unified memory, or add a smaller model to models.yaml")
	}
	if minDiskNeed > 0 {
		a.Warnings = append(a.Warnings, fmt.Sprintf(
			"disk space is short: free up at least %.1fGB to pull the smallest model that fits in memory", minDiskNeed))
	}

	sort.SliceStable(a.Models, func(i, j int) bool {
		rank := map[Verdict]int{Comfortable: 0, Tight: 1, NoFit: 2}
		return rank[a.Models[i].Verdict] < rank[a.Models[j].Verdict]
	})
	return a
}
