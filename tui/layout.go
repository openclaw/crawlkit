package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type paneFocus int

const (
	focusNone paneFocus = -1
	focusRows paneFocus = iota
	focusContext
	focusDetail
)

type rect struct {
	x int
	y int
	w int
	h int
}

type tableColumn struct {
	Key   string
	Title string
	Width int
}

type tableRow []string

type contextRow struct {
	ItemIndex  int
	Label      string
	Selectable bool
}

type archiveLayout struct {
	rows    rect
	context rect
	detail  rect
	stacked bool
	mode    string
}

type itemGroup struct {
	Key     string
	Title   string
	Kind    string
	Scope   string
	Count   int
	Latest  string
	Members []int
}

func (m model) layout() archiveLayout {
	width := maxInt(m.width, 80)
	height := m.height
	if height <= 0 {
		height = 24
	}
	bodyH := maxInt(8, height-3)
	if width >= 140 {
		if m.layoutMode == layoutModeRightStack {
			rowsW := maxInt(56, width*44/100)
			rightW := width - rowsW
			contextH := clampInt(maxInt(8, bodyH*42/100), 1, maxInt(1, bodyH-1))
			return archiveLayout{
				rows:    rect{x: 0, y: 1, w: rowsW, h: bodyH},
				context: rect{x: rowsW, y: 1, w: rightW, h: contextH},
				detail:  rect{x: rowsW, y: 1 + contextH, w: rightW, h: bodyH - contextH},
				mode:    string(layoutModeRightStack),
			}
		}
		rowsW := maxInt(48, width*34/100)
		contextW := maxInt(40, width*30/100)
		detailW := maxInt(42, width-rowsW-contextW)
		return archiveLayout{
			rows:    rect{x: 0, y: 1, w: rowsW, h: bodyH},
			context: rect{x: rowsW, y: 1, w: contextW, h: bodyH},
			detail:  rect{x: rowsW + contextW, y: 1, w: detailW, h: bodyH},
			mode:    string(layoutModeColumns),
		}
	}
	if width >= 100 {
		topH := clampInt(maxInt(8, bodyH/2), 1, maxInt(1, bodyH-1))
		rowsW := width / 2
		return archiveLayout{
			rows:    rect{x: 0, y: 1, w: rowsW, h: topH},
			context: rect{x: rowsW, y: 1, w: width - rowsW, h: topH},
			detail:  rect{x: 0, y: 1 + topH, w: width, h: bodyH - topH},
			stacked: true,
			mode:    "split",
		}
	}
	rowsH := clampInt(maxInt(7, bodyH*36/100), 1, maxInt(1, bodyH-2))
	contextH := clampInt(maxInt(6, bodyH*28/100), 1, maxInt(1, bodyH-rowsH-1))
	detailH := maxInt(6, bodyH-rowsH-contextH)
	return archiveLayout{
		rows:    rect{x: 0, y: 1, w: width, h: rowsH},
		context: rect{x: 0, y: 1 + rowsH, w: width, h: contextH},
		detail:  rect{x: 0, y: 1 + rowsH + contextH, w: width, h: detailH},
		stacked: true,
		mode:    "stacked",
	}
}

func (l archiveLayout) footerY() int {
	return maxInt(l.rows.y+l.rows.h, maxInt(l.context.y+l.context.h, l.detail.y+l.detail.h))
}

func (m model) paneAt(x, y int) paneFocus {
	layout := m.layout()
	switch {
	case layout.rows.contains(x, y):
		return focusRows
	case layout.context.contains(x, y):
		return focusContext
	case layout.detail.contains(x, y):
		return focusDetail
	default:
		return m.focus
	}
}

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

func paneStyle(pane, focus paneFocus, width, height int, accent string) lipgloss.Style {
	borderColor := accent
	if pane == focus {
		borderColor = "#f7f7ff"
	}
	return lipgloss.NewStyle().
		Width(maxInt(1, width-2)).
		Height(maxInt(1, height-2)).
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(borderColor)).
		Foreground(lipgloss.Color(archiveTextFG)).
		Padding(0, 1)
}

func pane(title, subtitle string, lines []string, rect rect, paneFocus paneFocus, focus paneFocus, accent string) string {
	return paneScrolled(title, subtitle, lines, rect, paneFocus, focus, accent, 0)
}

func paneScrolled(title, subtitle string, lines []string, rect rect, paneFocus paneFocus, focus paneFocus, accent string, scrollOffset int) string {
	width := maxInt(rect.w, 12)
	height := maxInt(rect.h, 3)
	contentW := paneContentWidth(width)
	contentH := paneContentHeight(height)
	body := flattenedPaneLines(lines, contentW)
	if len(body) == 0 {
		body = append(body, "")
	}
	maxOffset := maxInt(0, len(body)-contentH)
	scrollOffset = clampInt(scrollOffset, 0, maxOffset)
	if maxOffset > 0 {
		visibleEnd := minInt(len(body), scrollOffset+contentH)
		scrollLabel := fmt.Sprintf("%d-%d/%d", scrollOffset+1, visibleEnd, len(body))
		if strings.TrimSpace(subtitle) == "" {
			subtitle = scrollLabel
		} else {
			subtitle += "  " + scrollLabel
		}
	}
	titleLine := title
	if strings.TrimSpace(subtitle) != "" {
		titleLine += "  " + subtitle
	}
	header := paneTitleForWidth(paneFocus, focus, titleLine, contentW)
	body = append([]string(nil), body[scrollOffset:minInt(len(body), scrollOffset+contentH)]...)
	for len(body) < contentH {
		body = append(body, "")
	}
	out := append([]string{header}, body[:contentH]...)
	return paneStyle(paneFocus, focus, width, height, accent).Render(strings.Join(out, "\n"))
}

func flattenedPaneLines(lines []string, width int) []string {
	var body []string
	for _, line := range lines {
		body = append(body, wrapLines(line, width)...)
	}
	return body
}

func paneContentWidth(width int) int {
	return maxInt(1, width-4)
}

func paneContentHeight(height int) int {
	return maxInt(1, height-3)
}

func rowsViewportHeight(height int) int {
	return maxInt(1, paneContentHeight(height)-1)
}

func tableViewportWidth(rect rect) int {
	return maxInt(1, rect.w-4)
}
