package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func titleStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(archiveHeaderFG)).
		Background(lipgloss.Color("#0d1321")).
		Width(width)
}

func bold(value string) string {
	return lipgloss.NewStyle().Bold(true).Render(value)
}

func dim(value string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(archiveMutedFG)).Render(value)
}

func tagStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(archiveSubtleAccentFG)).
		Width(width)
}

func rowStyle(width int, selected bool, focused bool, inactive bool) lipgloss.Style {
	style := lipgloss.NewStyle().Width(width)
	if selected {
		if inactive {
			if focused {
				return style.
					Foreground(lipgloss.Color("#d6dde8")).
					Background(lipgloss.Color("#303744"))
			}
			return style.
				Foreground(lipgloss.Color("#aab2bf")).
				Background(lipgloss.Color("#242936"))
		}
		if focused {
			return style.
				Foreground(lipgloss.Color(archiveSelectedFG)).
				Background(lipgloss.Color(archiveSelectedBG))
		}
		return style.
			Foreground(lipgloss.Color(archiveBlurSelectedFG)).
			Background(lipgloss.Color(archiveBlurSelectedBG))
	}
	if inactive {
		return style.
			Foreground(lipgloss.Color(archiveInactiveRowFG)).
			Background(lipgloss.Color(archiveInactiveRowBG))
	}
	return style.
		Foreground(lipgloss.Color(archiveActiveRowFG)).
		Background(lipgloss.Color(archiveActiveRowBG))
}

func sectionRowStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(width).
		Foreground(lipgloss.Color(archiveSubtleAccentFG)).
		Bold(true)
}

func itemInactive(item Item) bool {
	for _, value := range []string{
		fieldValue(item, "status"),
		fieldValue(item, "state"),
		fieldValue(item, "deleted"),
		fieldValue(item, "archived"),
		fieldValue(item, "closed"),
		item.Kind,
	} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "closed", "deleted", "archived", "inactive", "local", "true":
			return true
		}
	}
	return false
}
