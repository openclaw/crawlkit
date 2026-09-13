package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func overlayBlock(base, block string, x, y, width int) string {
	baseLines := strings.Split(base, "\n")
	blockLines := strings.Split(block, "\n")
	for offset, line := range blockLines {
		row := y + offset
		if row < 0 || row >= len(baseLines) {
			continue
		}
		baseLine := baseLines[row]
		prefix := strings.Repeat(" ", maxInt(0, x))
		if x > 0 && baseLine != "" {
			prefix = padCells(ansi.Cut(baseLine, 0, x), x)
		}
		lineWidth := ansi.StringWidth(line)
		suffixStart := maxInt(0, x+lineWidth)
		suffix := ""
		if suffixStart < ansi.StringWidth(baseLine) {
			suffix = ansi.Cut(baseLine, suffixStart, width)
		}
		rendered := prefix + line + suffix
		if width > 0 {
			rendered = truncateCells(rendered, width)
		}
		baseLines[row] = rendered
	}
	return strings.Join(baseLines, "\n")
}

func truncateCells(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes))+3 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "..."
}

func wrap(value string, width int) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return ""
	}
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	var b strings.Builder
	for lipgloss.Width(value) > width {
		line := ansi.Cut(value, 0, width)
		cut := strings.LastIndex(line, " ")
		if cut > 0 {
			line = strings.TrimRight(line[:cut], " ")
		}
		if line == "" {
			line = ansi.Cut(value, 0, width)
		}
		b.WriteString(line)
		b.WriteByte('\n')
		value = strings.TrimSpace(strings.TrimPrefix(value, line))
	}
	b.WriteString(value)
	return b.String()
}

func wrapPlain(value string, width int) []string {
	width = maxInt(20, width)
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	var line string
	for _, word := range words {
		if lipgloss.Width(word) > width {
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
			lines = append(lines, truncateCells(word, width))
			continue
		}
		if lipgloss.Width(line)+1+lipgloss.Width(word) > width && line != "" {
			lines = append(lines, line)
			line = word
			continue
		}
		if line == "" {
			line = word
		} else {
			line += " " + word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func wrapLines(value string, width int) []string {
	width = maxInt(width, 1)
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(wrap(line, width), "\n")...)
	}
	return out
}

func padCells(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) > width {
		value = truncateCells(value, width)
	}
	for lipgloss.Width(value) < width {
		value += " "
	}
	return value
}

func fitBlock(value string, width, height int) string {
	width = maxInt(width, 1)
	height = maxInt(height, 1)
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i, line := range lines {
		lines[i] = padCells(truncateCells(line, width), width)
	}
	return strings.Join(lines, "\n")
}

func strconvQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
