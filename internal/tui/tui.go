// Package tui is the bubbletea terminal dashboard: live tok/s, RSS versus
// the memory budget, GPU residency, memory pressure, and thermal state at
// a 1Hz refresh.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alileza/brokemode/internal/metrics"
	"github.com/alileza/brokemode/internal/registry"
	"github.com/alileza/brokemode/internal/telemetry"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Width(18)
	valueStyle  = lipgloss.NewStyle().Bold(true)
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	badStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	borderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 2)
)

const sparkWidth = 40

// Model is the bubbletea model for the dashboard.
type Model struct {
	Registry  *registry.Registry
	ServeURL  string // brokemode serve snapshot endpoint
	collector *telemetry.Collector
	cancel    context.CancelFunc

	snap    telemetry.Snapshot
	stream  metrics.StreamPayload
	fromAPI bool
	tpsHist []float64
	width   int
}

type tickMsg time.Time

// New builds the TUI model; serveURL is the base URL of `brokemode serve`
// (used for gateway stats) and may be unreachable.
func New(reg *registry.Registry, serveURL string, sudoOK bool) *Model {
	return &Model{
		Registry:  reg,
		ServeURL:  serveURL,
		collector: telemetry.NewCollector(sudoOK),
	}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go m.collector.Run(ctx, nil)
	return tick()
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tickMsg:
		m.snap = m.collector.Latest()
		m.fromAPI = false
		if p, err := fetchSnapshot(m.ServeURL); err == nil {
			m.stream = *p
			m.fromAPI = true
			// serve's collector may have sudo when ours doesn't; prefer it.
			if p.Telemetry.Time.After(m.snap.Time.Add(-3 * time.Second)) {
				m.snap = p.Telemetry
			}
		}
		m.tpsHist = append(m.tpsHist, m.stream.DecodeTPS)
		if len(m.tpsHist) > sparkWidth {
			m.tpsHist = m.tpsHist[len(m.tpsHist)-sparkWidth:]
		}
		return m, tick()
	}
	return m, nil
}

func fetchSnapshot(base string) (*metrics.StreamPayload, error) {
	client := http.Client{Timeout: 700 * time.Millisecond}
	resp, err := client.Get(base + "/api/snapshot")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var p metrics.StreamPayload
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// bar renders a labeled ratio bar of the given width.
func bar(ratio float64, width int, style lipgloss.Style) string {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio*float64(width) + 0.5)
	return style.Render(strings.Repeat("█", filled)) + dimStyle.Render(strings.Repeat("░", width-filled))
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

func sparkline(vals []float64, width int) string {
	if len(vals) == 0 {
		return dimStyle.Render(strings.Repeat("▁", width))
	}
	maxV := 1.0
	for _, v := range vals {
		if v > maxV {
			maxV = v
		}
	}
	var b strings.Builder
	for i := len(vals) - min(len(vals), width); i < len(vals); i++ {
		idx := int(vals[i] / maxV * float64(len(sparkRunes)-1))
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}

func styleFor(ratio float64, warnAt, badAt float64) lipgloss.Style {
	switch {
	case ratio >= badAt:
		return badStyle
	case ratio >= warnAt:
		return warnStyle
	default:
		return okStyle
	}
}

// View implements tea.Model.
func (m *Model) View() string {
	budgetBytes := m.Registry.MaxRSSGB * float64(1<<30)
	rss := float64(m.snap.OllamaRSSBytes)
	rssRatio := 0.0
	if budgetBytes > 0 {
		rssRatio = rss / budgetBytes
	}

	var rows []string
	rows = append(rows, titleStyle.Render("brokemode")+dimStyle.Render("  local LLM dashboard — q to quit"))
	rows = append(rows, "")

	tps := fmt.Sprintf("%6.1f tok/s", m.stream.DecodeTPS)
	src := dimStyle.Render(" (waiting for brokemode serve)")
	if m.fromAPI {
		src = dimStyle.Render(fmt.Sprintf("  TTFT %.0fms", m.stream.TTFTMs))
	}
	rows = append(rows, labelStyle.Render("decode")+valueStyle.Render(tps)+"  "+sparkline(m.tpsHist, sparkWidth)+src)

	rows = append(rows, labelStyle.Render(fmt.Sprintf("RSS %.1f/%.0fGB", rss/(1<<30), m.Registry.MaxRSSGB))+
		bar(rssRatio, sparkWidth, styleFor(rssRatio, 0.75, 0.92))+
		valueStyle.Render(fmt.Sprintf(" %3.0f%%", rssRatio*100)))

	rows = append(rows, labelStyle.Render("GPU active")+
		bar(m.snap.GPUActiveRatio, sparkWidth, styleFor(m.snap.GPUActiveRatio, 0.85, 0.97))+
		valueStyle.Render(fmt.Sprintf(" %3.0f%%", m.snap.GPUActiveRatio*100))+
		dimStyle.Render(fmt.Sprintf("  %.1fW", m.snap.PackagePowerW)))

	pressure := m.snap.MemoryPressurePct / 100
	rows = append(rows, labelStyle.Render("mem pressure")+
		bar(pressure, sparkWidth, styleFor(pressure, 0.6, 0.8))+
		valueStyle.Render(fmt.Sprintf(" %3.0f%%", m.snap.MemoryPressurePct))+
		dimStyle.Render(fmt.Sprintf("  wired %.1fGB compressed %.1fGB",
			float64(m.snap.WiredBytes)/(1<<30), float64(m.snap.CompressedBytes)/(1<<30))))

	thermal := okStyle.Render("nominal")
	if m.snap.ThermalLevel > 0 {
		thermal = badStyle.Render(fmt.Sprintf("THROTTLED — CPU limited to %d%%", m.snap.CPUSpeedLimit))
	}
	rows = append(rows, labelStyle.Render("thermal")+thermal)

	if warnings := m.collector.Warnings(); len(warnings) > 0 && !m.fromAPI {
		var keys []string
		for k := range warnings {
			keys = append(keys, k)
		}
		rows = append(rows, "")
		rows = append(rows, warnStyle.Render("partial telemetry: ")+dimStyle.Render(strings.Join(keys, ", ")+" unavailable"))
	}

	if len(m.stream.Recent) > 0 {
		rows = append(rows, "")
		rows = append(rows, dimStyle.Render(fmt.Sprintf("%-8s %-14s %8s %10s %6s %6s", "when", "model", "TTFT", "decode", "in", "out")))
		for i, r := range m.stream.Recent {
			if i >= 5 {
				break
			}
			rows = append(rows, fmt.Sprintf("%-8s %-14s %7.0fms %6.1ftok/s %6d %6d",
				r.Time.Format("15:04:05"), r.Model, r.TTFTMs, r.DecodeTPS, r.TokensIn, r.TokensOut))
		}
	}

	return borderStyle.Render(strings.Join(rows, "\n")) + "\n"
}
