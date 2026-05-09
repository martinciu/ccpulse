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
  j / k             move within table
  enter             drill down
  esc               back out
  g                 toggle Live scope
  r                 refresh
  c                 open config
  ?                 toggle this help
  q                 quit
`
	return box.Render(body)
}
