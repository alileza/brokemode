package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/alileza/brokemode/internal/ollama"
	"github.com/alileza/brokemode/internal/recommend"
	"github.com/alileza/brokemode/internal/registry"
)

// The launcher is what bare `brokemode` runs on a terminal: explain the
// model mapping, then offer to launch Claude Code fully configured.

type menuAction int

const (
	actionLaunchClaude menuAction = iota
	actionPull
	actionDoctor
	actionServe
	actionUpdate
	actionQuit
)

type menuItem struct {
	title  string
	desc   string
	action menuAction
}

var (
	lTitle  = lipgloss.NewStyle().Bold(true)
	lDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	lFaint  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	lAccent = lipgloss.NewStyle().Foreground(lipgloss.Color("33"))
	lOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("35"))
	lWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("172"))
)

type launcherModel struct {
	header string
	items  []menuItem
	cursor int
	choice menuAction
}

func (m *launcherModel) Init() tea.Cmd { return nil }

func (m *launcherModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter":
			m.choice = m.items[m.cursor].action
			return m, tea.Quit
		case "q", "esc", "ctrl+c":
			m.choice = actionQuit
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *launcherModel) View() string {
	var b strings.Builder
	b.WriteString(m.header)
	for i, it := range m.items {
		if i == m.cursor {
			b.WriteString(lAccent.Render("▸ "+it.title) + "\n")
		} else {
			b.WriteString("  " + it.title + "\n")
		}
		b.WriteString(lFaint.Render("    "+it.desc) + "\n\n")
	}
	b.WriteString(lFaint.Render("  ↑/↓ select · enter run · q quit") + "\n")
	return b.String()
}

// primaryAlias is the claude-* name the gateway serves a model under.
func primaryAlias(m *registry.Model) string {
	for _, a := range m.Aliases {
		if strings.HasPrefix(a, "claude-") {
			return a
		}
	}
	if len(m.Aliases) > 0 {
		return m.Aliases[0]
	}
	return m.Name
}

// launcherHeader renders the version line and the model-mapping table with
// pulled status and this machine's fit verdicts.
func launcherHeader(reg *registry.Registry, advice recommend.Advice, pulled map[string]bool, ollamaUp bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n\n", lTitle.Render("brokemode"), lDim.Render(version))
	b.WriteString("  " + lTitle.Render("Claude Code, but it bills your Mac") + "\n")
	b.WriteString(lDim.Render("    the gateway serves Anthropic model names on local models:") + "\n\n")

	verdicts := map[string]recommend.ModelAdvice{}
	for _, ma := range advice.Models {
		verdicts[ma.Model.Name] = ma
	}
	for i := range reg.Models {
		m := &reg.Models[i]
		var notes []string
		if !ollamaUp {
			// unknown pull state; say nothing rather than lying
		} else if pulled[m.Name] {
			notes = append(notes, lOK.Render("pulled"))
		} else {
			notes = append(notes, lFaint.Render("not pulled"))
		}
		if m.Name == advice.Recommended {
			notes = append(notes, lOK.Render("recommended"))
		} else if v, ok := verdicts[m.Name]; ok && v.Verdict != recommend.Comfortable {
			notes = append(notes, lWarn.Render(string(v.Verdict)))
		}
		fmt.Fprintf(&b, "    %-18s → %-13s %s\n", primaryAlias(m), m.Name, strings.Join(notes, " · "))
	}
	b.WriteString(lFaint.Render("    edit ~/.brokemode/models.yaml to change the mapping") + "\n")
	if !ollamaUp {
		b.WriteString(lWarn.Render("    ollama isn't answering — pull states unknown") + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// isPulled reports whether ollama has the model locally.
func isPulled(ctx context.Context, client *ollama.Client, name string) (bool, error) {
	tags, err := client.Tags(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tags.Models {
		if t.Name == name || strings.TrimSuffix(t.Name, ":latest") == name {
			return true, nil
		}
	}
	return false, nil
}

// ensureModelReady makes sure the model is pulled and resident in memory
// before Claude Code sends its first request: pull if missing, then a
// bare /api/generate call (empty prompt) which loads the model and
// returns. Warming only the main model — loading two at once is how a
// 16GB machine blows its budget.
func ensureModelReady(reg *registry.Registry, name string) error {
	client := ollama.New(flagOllamaHost)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	pulled, err := isPulled(ctx, client, name)
	cancel()
	if err != nil {
		return fmt.Errorf("could not check ollama's local models: %w", err)
	}
	if !pulled {
		fmt.Printf("%s isn't pulled yet — pulling it first\n", name)
		if err := pullModel(reg, name); err != nil {
			return err
		}
	}

	fmt.Printf("loading %s into memory (makes the first response instant)…\n", name)
	loadCtx, loadCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer loadCancel()
	if _, err := client.Generate(loadCtx, ollama.GenerateRequest{Model: name}); err != nil {
		// Not fatal: claude will surface a real failure; a slow first
		// response is the worst case here.
		fmt.Printf("warm-up did not complete (%v) — the first response may be slow\n", err)
	} else {
		fmt.Printf("%s is loaded and ready\n", name)
	}
	return nil
}

func gatewayBase() string {
	if g := os.Getenv("BROKEMODE_GATEWAY"); g != "" {
		return g
	}
	return "http://127.0.0.1:9100"
}

func gatewayHealthy(base string) bool {
	client := http.Client{Timeout: 700 * time.Millisecond}
	resp, err := client.Get(base + "/health")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// launchClaudeCode makes sure a gateway is running (starting `brokemode
// serve` detached if not), then replaces this process with claude, env
// fully configured for the local gateway.
func launchClaudeCode(reg *registry.Registry, advice recommend.Advice) error {
	base := gatewayBase()
	if !gatewayHealthy(base) {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		logPath := filepath.Join(os.Getenv("HOME"), ".brokemode", "serve.log")
		_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		fmt.Printf("starting the gateway + dashboard in the background (logs: %s)\n", logPath)
		cmd := exec.Command(self, "serve")
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start brokemode serve: %w", err)
		}
		_ = logFile.Close()
		for i := 0; i < 30 && !gatewayHealthy(base); i++ {
			time.Sleep(500 * time.Millisecond)
		}
		if !gatewayHealthy(base) {
			return fmt.Errorf("the gateway never became healthy on %s — check %s", base, logPath)
		}
	}

	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude is not installed — get it with: npm install -g @anthropic-ai/claude-code (or see https://claude.com/claude-code), then run brokemode again")
	}

	model := "claude-sonnet-5"
	if advice.Recommended != "" {
		if m, err := reg.Resolve(advice.Recommended); err == nil {
			model = primaryAlias(m)
		}
	}
	fast := "claude-haiku-4-5"
	if advice.FastPick != "" {
		if m, err := reg.Resolve(advice.FastPick); err == nil {
			fast = primaryAlias(m)
		}
	}

	// The main model gets pulled AND warm-loaded; the small/fast model
	// only needs to exist on disk (loading both at once busts the budget).
	if err := ensureModelReady(reg, mustName(reg, model)); err != nil {
		return err
	}
	if fastName := mustName(reg, fast); fastName != mustName(reg, model) {
		client := ollama.New(flagOllamaHost)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pulled, err := isPulled(ctx, client, fastName)
		cancel()
		if err == nil && !pulled {
			fmt.Printf("%s (small/fast model) isn't pulled yet — pulling it too\n", fastName)
			if err := pullModel(reg, fastName); err != nil {
				return err
			}
		}
	}

	env := os.Environ()
	env = append(env,
		"ANTHROPIC_BASE_URL="+base,
		"ANTHROPIC_AUTH_TOKEN=brokemode-local",
		"ANTHROPIC_MODEL="+model,
		"ANTHROPIC_SMALL_FAST_MODEL="+fast,
	)
	fmt.Printf("launching claude (%s → %s)\n", model, mustName(reg, model))
	return syscall.Exec(claudeBin, []string{"claude"}, env)
}

func mustName(reg *registry.Registry, alias string) string {
	if m, err := reg.Resolve(alias); err == nil {
		return m.Name
	}
	return alias
}

// runLauncher is bare `brokemode` on a TTY.
func runLauncher(cmd *cobra.Command) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	advice := recommend.For(gatherHost(context.Background()), reg)

	pulled := map[string]bool{}
	ollamaUp := false
	tctx, tcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer tcancel()
	if tags, err := ollama.New(flagOllamaHost).Tags(tctx); err == nil {
		ollamaUp = true
		for _, t := range tags.Models {
			pulled[t.Name] = true
			pulled[strings.TrimSuffix(t.Name, ":latest")] = true
		}
	}

	recName := advice.Recommended
	if recName == "" {
		recName = recommendedModel(reg)
	}
	items := []menuItem{
		{fmt.Sprintf("Launch Claude Code (%s → %s)", primaryAliasByName(reg, recName), recName),
			"pulls + warm-loads the model if needed, starts the gateway, opens claude", actionLaunchClaude},
		{fmt.Sprintf("Pull recommended model (%s)", recName),
			"download it via ollama — budget-checked first", actionPull},
		{"Doctor", "what can this machine actually run, with warnings", actionDoctor},
		{"Dashboard", "gateway :9100 + web dashboard http://127.0.0.1:9101 (foreground)", actionServe},
		{"Update brokemode", "self-update to the latest release", actionUpdate},
	}

	m := &launcherModel{
		header: launcherHeader(reg, advice, pulled, ollamaUp),
		items:  items,
		choice: actionQuit,
	}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		return err
	}

	switch m.choice {
	case actionLaunchClaude:
		return launchClaudeCode(reg, advice)
	case actionPull:
		return pullModel(reg, recName)
	case actionDoctor:
		return newDoctorCmd().RunE(cmd, nil)
	case actionServe:
		return newServeCmd().RunE(cmd, nil)
	case actionUpdate:
		return runUpdate(false)
	default:
		return nil
	}
}

func primaryAliasByName(reg *registry.Registry, name string) string {
	if m, err := reg.Resolve(name); err == nil {
		return primaryAlias(m)
	}
	return name
}

// stdinIsTTY gates the launcher: piped/scripted invocations get the normal
// cobra help instead of an interactive menu.
func stdinIsTTY() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}
