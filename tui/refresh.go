package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) refreshTickCmd() tea.Cmd {
	if m.refresh == nil || m.refreshEvery <= 0 {
		return nil
	}
	return tea.Tick(m.refreshEvery, func(time.Time) tea.Msg {
		return refreshTickMsg{}
	})
}

func (m model) contextDoneCmd() tea.Cmd {
	if m.ctx == nil {
		return nil
	}
	return func() tea.Msg {
		<-m.ctx.Done()
		return contextDoneMsg{}
	}
}

func (m *model) startRefresh(manual bool) tea.Cmd {
	if m.refresh == nil {
		if manual {
			m.status = "Refresh unavailable"
		}
		return nil
	}
	if m.refreshing {
		if manual {
			m.status = "Refresh already in progress"
		}
		return nil
	}
	m.closeMenu()
	m.showHelp = false
	m.refreshing = true
	if manual {
		m.status = "Refreshing " + m.refreshSourceLabel()
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	refresh := m.refresh
	return func() tea.Msg {
		items, err := refresh(ctx)
		return refreshResultMsg{items: items, err: err, manual: manual}
	}
}

func (m *model) finishRefresh(msg refreshResultMsg) {
	m.refreshing = false
	if msg.err != nil {
		m.status = "Refresh failed: " + msg.err.Error()
		return
	}
	previousSignature := itemSignature(m.items)
	previousKey := ""
	if item, ok := m.selectedItem(); ok {
		previousKey = itemStableKey(item)
	}
	m.items = append([]Item(nil), msg.items...)
	m.applyFilter()
	if previousKey != "" {
		m.selectItemByStableKey(previousKey)
	}
	m.ensureVisible()
	nextSignature := itemSignature(m.items)
	if previousSignature == nextSignature {
		if msg.manual {
			m.status = titleCase(m.refreshSourceLabel()) + " already current"
		}
		return
	}
	if msg.manual {
		m.status = fmt.Sprintf("Refreshed %s: %d row(s)", m.refreshSourceLabel(), len(m.items))
		return
	}
	m.status = fmt.Sprintf("Auto refreshed %s: %d row(s)", m.refreshSourceLabel(), len(m.items))
}

func (m model) refreshSourceLabel() string {
	switch normalizeSourceKind(m.sourceKind) {
	case SourceRemote:
		return "remote data"
	case SourceLocal:
		return "local data"
	default:
		return "archive rows"
	}
}

func titleCase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
