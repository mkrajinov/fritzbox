package cli

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"

	"github.com/mkrajinovic/fbctl/internal/ui"
)

// printTable renders a borderless table: bold header, rows separated by spacing only.
func (a *app) printTable(headers []string, rows [][]string) {
	t := table.New().
		Border(lipgloss.HiddenBorder()).
		BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).
		BorderColumn(false).BorderHeader(false).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().PaddingRight(2)
			if row == table.HeaderRow {
				return s.Inherit(ui.Muted).Bold(true)
			}
			return s
		})
	fmt.Fprintln(a.out, t.Render())
}

// kv prints aligned "key  value" lines.
func (a *app) kv(pairs ...[2]string) {
	w := 0
	for _, p := range pairs {
		w = max(w, lipgloss.Width(p[0]))
	}
	for _, p := range pairs {
		fmt.Fprintf(a.out, "%s  %s\n", ui.Muted.Render(fmt.Sprintf("%-*s", w, p[0])), p[1])
	}
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}
