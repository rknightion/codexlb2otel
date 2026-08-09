package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rknightion/codexlb2otel/internal/live"
)

// The picker is a front-end and nothing more: it produces the same selection that -pick
// expresses, and everything downstream takes thread ids. That is why it carries no tests -
// -pick is the tested path, and a terminal is a poor thing to assert against.

var (
	styleTitle    = lipgloss.NewStyle().Bold(true)
	styleHelp     = lipgloss.NewStyle().Faint(true)
	styleCursor   = lipgloss.NewStyle().Bold(true)
	styleSelected = lipgloss.NewStyle().Bold(true)
	styleDim      = lipgloss.NewStyle().Faint(true)
)

type picker struct {
	rows     []*live.Thread
	chosen   map[int]bool
	cursor   int
	height   int
	quit     bool
	accepted bool
}

func newPicker(rows []*live.Thread) *picker {
	return &picker{rows: rows, chosen: map[int]bool{}, height: 20}
}

func (p *picker) Init() tea.Cmd { return nil }

func (p *picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Leave room for the title, the help line and the blank separators.
		p.height = max(3, msg.Height-6)
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			p.quit = true
			return p, tea.Quit
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
		case "down", "j":
			if p.cursor < len(p.rows)-1 {
				p.cursor++
			}
		case "g", "home":
			p.cursor = 0
		case "G", "end":
			p.cursor = len(p.rows) - 1
		case " ", "x":
			p.chosen[p.cursor] = !p.chosen[p.cursor]
		case "a":
			// Toggle all: select everything, or clear if everything is already selected.
			// Counted rather than measured by map length - the map keeps false entries for
			// rows toggled on and back off, so len() would call five deselected rows a full
			// selection and clear what the user just chose.
			all := count(p.chosen) == len(p.rows)
			for i := range p.rows {
				p.chosen[i] = !all
			}
			if all {
				p.chosen = map[int]bool{}
			}
		case "enter":
			p.accepted = true
			return p, tea.Quit
		}
	}
	return p, nil
}

func (p *picker) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render(fmt.Sprintf("%d sessions — space selects, a toggles all, enter summarises, q cancels", len(p.rows))))
	b.WriteString("\n\n")

	// Scroll so the cursor stays on screen without redrawing the whole list every frame.
	start := 0
	if p.cursor >= p.height {
		start = p.cursor - p.height + 1
	}
	end := min(len(p.rows), start+p.height)

	for i := start; i < end; i++ {
		th := p.rows[i]
		cursor, mark := "  ", "[ ]"
		if i == p.cursor {
			cursor = styleCursor.Render("> ")
		}
		if p.chosen[i] {
			mark = styleSelected.Render("[x]")
		}

		line := fmt.Sprintf("%s%s %s  %s", cursor, mark, th.LastSeen.Local().Format("01-02 15:04"), truncate(th.Name, 52))
		b.WriteString(line)
		b.WriteString(styleDim.Render(fmt.Sprintf("  %s", meta(th))))
		b.WriteString("\n")
	}

	if end < len(p.rows) {
		b.WriteString(styleDim.Render(fmt.Sprintf("  … %d more\n", len(p.rows)-end)))
	}
	b.WriteString("\n")
	b.WriteString(styleHelp.Render(fmt.Sprintf("%d selected", count(p.chosen))))
	b.WriteString("\n")
	return b.String()
}

// selected returns the chosen rows, or nil when the user cancelled.
func (p *picker) selected() []*live.Thread {
	if !p.accepted {
		return nil
	}
	var out []*live.Thread
	for i, th := range p.rows {
		if p.chosen[i] {
			out = append(out, th)
		}
	}
	return out
}

// meta is the dim right-hand column: enough to tell two sessions apart without
// pretending to be the summary.
func meta(th *live.Thread) string {
	parts := []string{fmt.Sprintf("%s, %s", plural(th.Turns, "turn", "turns"), dur(th.LastSeen.Sub(th.FirstSeen)))}
	if n := countKids(th); n > 0 {
		parts = append(parts, plural(n, "sub-agent", "sub-agents"))
	}
	if th.Errors > 0 {
		parts = append(parts, plural(th.Errors, "error", "errors"))
	}
	if th.Orphaned {
		parts = append(parts, "orphaned")
	}
	return strings.Join(parts, " · ")
}

func countKids(th *live.Thread) int {
	n := len(th.Children)
	for _, c := range th.Children {
		n += countKids(c)
	}
	return n
}

func count(m map[int]bool) int {
	n := 0
	for _, v := range m {
		if v {
			n++
		}
	}
	return n
}

func dur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", "")
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
