package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *model) handleLeftClick(x, y int) {
	layout := m.layout()
	m.closeMenu()
	focus := m.paneAt(x, y)
	m.focus = focus
	now := time.Now()
	if focus == focusRows {
		if index, ok := m.selectGroupAt(layout.rows, x, y); ok {
			m.finishRowClick(focusRows, index, x, y, now)
		}
	} else if focus == focusContext {
		if index, ok := m.selectMemberAt(layout.context, x, y); ok {
			m.finishRowClick(focusContext, index, x, y, now)
		}
	} else {
		m.clearLastClick()
	}
}

func (m *model) handleRightClick(x, y int) {
	focus := m.paneAt(x, y)
	m.focus = focus
	if focus == focusRows {
		m.selectGroupAt(m.layout().rows, x, y)
	} else if focus == focusContext {
		m.selectMemberAt(m.layout().context, x, y)
	}
	m.openActionMenuFor(focus)
	m.placeFloatingMenu(x, y)
}

func (m *model) selectGroupAt(rect rect, x, y int) (int, bool) {
	row := y - rect.y - 3
	if row == -1 {
		m.sortGroupsFromHeader(x-rect.x-2, paneContentWidth(rect.w))
		m.clearLastClick()
		return 0, false
	}
	if row < 0 || row >= rowsViewportHeight(rect.h) {
		return 0, false
	}
	groupIndex := m.offset + row
	if groupIndex < 0 || groupIndex >= len(m.groups) {
		return 0, false
	}
	m.selectGroup(groupIndex)
	return groupIndex, true
}

func (m *model) selectMemberAt(rect rect, x, y int) (int, bool) {
	row := y - rect.y - 3
	if row == -1 {
		m.sortMembersFromHeader(x-rect.x-2, paneContentWidth(rect.w))
		m.clearLastClick()
		return 0, false
	}
	contextRows := m.currentContextRows()
	memberOffset := m.contextOffset + row
	if row < 0 || row >= rowsViewportHeight(rect.h) || memberOffset < 0 || memberOffset >= len(contextRows) {
		return 0, false
	}
	contextRow := contextRows[memberOffset]
	if !contextRow.Selectable {
		m.status = contextRow.Label
		m.clearLastClick()
		return memberOffset, false
	}
	itemIndex := contextRow.ItemIndex
	m.selectItemIndex(itemIndex)
	m.detailView.GotoTop()
	m.ensureVisible()
	return itemIndex, true
}

func (m *model) selectGroup(groupIndex int) {
	if groupIndex < 0 || groupIndex >= len(m.groups) || len(m.groups[groupIndex].Members) == 0 {
		return
	}
	m.selectItemIndex(m.groups[groupIndex].Members[0])
	m.contextOffset = 0
	m.detailView.GotoTop()
	m.ensureVisible()
}

func (m *model) finishRowClick(focus paneFocus, index, x, y int, now time.Time) {
	if m.isDoubleClick(focus, index, x, y, now) {
		m.clearLastClick()
		m.openSelectedURL()
		return
	}
	m.lastClickFocus = focus
	m.lastClickIndex = index
	m.lastClickX = x
	m.lastClickY = y
	m.lastClickAt = now
}

func (m model) isDoubleClick(focus paneFocus, index, x, y int, now time.Time) bool {
	return !m.lastClickAt.IsZero() &&
		m.lastClickFocus == focus &&
		m.lastClickIndex == index &&
		m.lastClickX == x &&
		m.lastClickY == y &&
		now.Sub(m.lastClickAt) <= doubleClickWindow
}

func (m *model) clearLastClick() {
	m.lastClickAt = time.Time{}
}

func (m *model) queueWheelScroll(focus paneFocus, delta int) tea.Cmd {
	if delta == 0 {
		return nil
	}
	if m.wheelPending && m.wheelFocus != focus {
		m.cancelQueuedWheelScroll()
	}
	m.focus = focus
	m.wheelFocus = focus
	m.wheelDelta = clampInt(m.wheelDelta+delta, -wheelMaxBufferedDelta, wheelMaxBufferedDelta)
	if m.wheelPending {
		return nil
	}
	m.wheelPending = true
	m.wheelSeq++
	seq := m.wheelSeq
	return tea.Tick(wheelScrollDelay, func(time.Time) tea.Msg {
		return wheelScrollMsg{seq: seq}
	})
}

func (m *model) cancelQueuedWheelScroll() {
	if !m.wheelPending && m.wheelDelta == 0 {
		return
	}
	m.wheelPending = false
	m.wheelDelta = 0
	m.wheelSeq++
}

func (m *model) applyQueuedWheelScroll() {
	delta := m.wheelDelta
	focus := m.wheelFocus
	m.wheelPending = false
	m.wheelDelta = 0
	if delta == 0 {
		return
	}
	m.focus = focus
	m.scrollFocused(delta)
}
