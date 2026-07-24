package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// write persists the JSON report and regenerates results/summary.md from
// every report present in OutDir.
func (r *Runner) write(report *Report) error {
	if err := os.MkdirAll(r.OutDir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%s.json", strings.ReplaceAll(report.Model, ":", "_"), report.Timestamp)
	path := filepath.Join(r.OutDir, name)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	r.logf("wrote %s", path)

	if err := WriteSummary(r.OutDir); err != nil {
		return err
	}
	r.logf("wrote %s", filepath.Join(r.OutDir, "summary.md"))
	return nil
}

// WriteSummary rebuilds summary.md from all *.json reports in dir.
func WriteSummary(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var reports []Report
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var rep Report
		if err := json.Unmarshal(data, &rep); err != nil {
			continue
		}
		if rep.Model != "" {
			reports = append(reports, rep)
		}
	}
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].Model != reports[j].Model {
			return reports[i].Model < reports[j].Model
		}
		return reports[i].Timestamp < reports[j].Timestamp
	})

	var b strings.Builder
	b.WriteString("# brokemode benchmark summary\n\n")
	b.WriteString("Median / p95 over measured runs (never the mean). Regenerated on every `brokemode bench`.\n\n")
	b.WriteString("| model | run | case | TTFT ms (med/p95) | prefill tok/s (med/p95) | decode tok/s (med/p95) | tok out | peak RSS | thermal |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, rep := range reports {
		for _, c := range rep.Cases {
			throttle := ""
			if c.ThermalThrottle {
				throttle = "⚠️ THROTTLED"
			}
			partial := ""
			if rep.Partial {
				partial = " (partial)"
			}
			fmt.Fprintf(&b, "| %s | %s%s | %s | %.0f / %.0f | %.1f / %.1f | %.1f / %.1f | %d | %.1f GB | %s |\n",
				rep.Model, rep.Timestamp, partial, c.Case,
				c.TTFTMs.Median, c.TTFTMs.P95,
				c.PrefillTPS.Median, c.PrefillTPS.P95,
				c.DecodeTPS.Median, c.DecodeTPS.P95,
				c.TokensOutTotal,
				float64(c.Telemetry.PeakOllamaRSSBytes)/(1<<30),
				throttle)
		}
	}
	return os.WriteFile(filepath.Join(dir, "summary.md"), []byte(b.String()), 0o644)
}
