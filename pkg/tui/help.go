package tui

import "github.com/charmbracelet/lipgloss"

func helpView(s Style) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(Base01).
		Padding(1, 2)
	body := `Keys
  tab / shift-tab   cycle tabs
  1..5              jump to tab
  g                 toggle Live scope (global / this-tmux)
  enter             drill down (History / Projects)
  esc               back out of drill-down
  ?                 toggle this help
  q                 quit
`
	return box.Render(body)
}
