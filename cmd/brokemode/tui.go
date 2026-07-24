package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/alileza/brokemode/internal/tui"
)

func newTUICmd() *cobra.Command {
	var serveURL string
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Terminal dashboard: tok/s, RSS budget, GPU, memory pressure, thermal",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			p := tea.NewProgram(tui.New(reg, serveURL, sudoAvailable()), tea.WithAltScreen())
			_, err = p.Run()
			return err
		},
	}
	cmd.Flags().StringVar(&serveURL, "serve-url", "http://127.0.0.1:9101", "brokemode serve base URL for gateway stats")
	return cmd
}
