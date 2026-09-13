package tui

import (
	"fmt"
	"strings"
)

func (m *model) startFilter() {
	m.closeMenu()
	m.showHelp = false
	m.savedQuery = m.query
	m.jumpMode = false
	m.jumpQuery = ""
	m.filterMode = true
}

func (m *model) startJump() {
	m.closeMenu()
	m.showHelp = false
	m.filterMode = false
	m.jumpMode = true
	m.jumpQuery = ""
}

func (m *model) finishJump() {
	target, err := parsePositiveInt(m.jumpQuery)
	if err != nil {
		m.status = "Jump expects a row number"
		return
	}
	switch m.focus {
	case focusContext:
		members := m.currentGroupMembers()
		if len(members) == 0 {
			m.status = "No messages to jump"
			break
		}
		target = clampInt(target, 1, len(members))
		m.selectMemberOffset(target - 1)
		m.status = fmt.Sprintf("Jumped to message %d", target)
	case focusDetail:
		if len(m.filtered) == 0 {
			m.status = "No rows to jump"
		} else {
			target = clampInt(target, 1, len(m.filtered))
			m.selectItemOffset(target - 1)
			m.status = fmt.Sprintf("Jumped to row %d", target)
		}
	default:
		if len(m.groups) == 0 {
			m.status = "No groups to jump"
			break
		}
		target = clampInt(target, 1, len(m.groups))
		m.selectGroup(target - 1)
		m.status = fmt.Sprintf("Jumped to group %d", target)
	}
	m.jumpMode = false
	m.jumpQuery = ""
}

func (m *model) toggleLayout() {
	if m.layoutMode == layoutModeRightStack {
		m.layoutMode = layoutModeColumns
		return
	}
	m.layoutMode = layoutModeRightStack
}

func (m *model) toggleDetailMode() {
	m.showHelp = false
	m.compactDetail = !m.compactDetail
	m.detailView.GotoTop()
	m.status = "Detail mode: " + detailModeLabel(m.compactDetail)
}

func (m *model) cycleGroupMode() {
	m.showHelp = false
	order := groupModeCycle(m.layoutPreset)
	next := order[0]
	for index, mode := range order {
		if mode == m.groupMode {
			next = order[(index+1)%len(order)]
			break
		}
	}
	m.groupMode = next
	m.contextOffset = 0
	m.applyFilter()
	m.detailView.GotoTop()
	m.status = "Group view: " + groupModeLabel(m.layoutPreset, m.groupMode)
}

func (m model) footerLocation() string {
	location := strings.TrimSpace(m.sourceLocation)
	if location == "" {
		return m.sourceKind
	}
	return m.sourceKind + " " + location
}

func (m *model) moveGroup(delta int) {
	if len(m.groups) == 0 {
		m.selected = 0
		m.offset = 0
		return
	}
	m.selectGroup(clampInt(m.currentGroupIndex()+delta, 0, len(m.groups)-1))
}

func (m *model) moveMember(delta int) {
	members := m.currentGroupMembers()
	if len(members) == 0 {
		return
	}
	current := m.currentMemberOffset()
	m.selectMemberOffset(clampInt(current+delta, 0, len(members)-1))
}

func (m *model) selectMemberOffset(offset int) {
	members := m.currentGroupMembers()
	if len(members) == 0 {
		return
	}
	offset = clampInt(offset, 0, len(members)-1)
	m.selectItemIndex(members[offset])
	m.contextOffset = 0
	m.detailView.GotoTop()
	m.ensureVisible()
}

func (m *model) selectItemOffset(offset int) {
	if len(m.filtered) == 0 {
		return
	}
	m.selected = clampInt(offset, 0, len(m.filtered)-1)
	m.detailView.GotoTop()
	m.ensureVisible()
}

func (m *model) scrollFocused(delta int) {
	switch m.focus {
	case focusContext:
		m.moveMember(delta)
	case focusDetail:
		m.syncDetailViewport()
		if delta > 0 {
			m.detailView.LineDown(delta)
		} else if delta < 0 {
			m.detailView.LineUp(-delta)
		}
	default:
		m.moveGroup(delta)
	}
}

func (m model) focusedPageSize() int {
	layout := m.layout()
	switch m.focus {
	case focusContext:
		return max(1, rowsViewportHeight(layout.context.h))
	case focusDetail:
		if m.detailView.Height > 0 {
			return max(1, m.detailView.Height)
		}
		return max(1, layout.detail.h-2)
	default:
		return m.pageSize()
	}
}

func (m model) maxContextOffset() int {
	return max(0, len(m.currentContextRows())-rowsViewportHeight(m.layout().context.h))
}

func (m *model) applyFilter() {
	current := m.currentItemIndex()
	query := strings.ToLower(strings.TrimSpace(m.query))
	m.filtered = m.filtered[:0]
	for i, item := range m.items {
		if query == "" || strings.Contains(strings.ToLower(item.searchText()), query) {
			m.filtered = append(m.filtered, i)
		}
	}
	m.sortFiltered()
	m.buildGroups()
	if len(m.filtered) == 0 {
		m.selected = 0
		m.offset = 0
		m.contextOffset = 0
		return
	}
	if current >= 0 {
		for i, index := range m.filtered {
			if index == current {
				m.selected = i
				break
			}
		}
	}
	m.selected = clampInt(m.selected, 0, len(m.filtered)-1)
	m.ensureVisible()
}

func (m *model) ensureVisible() {
	page := m.pageSize()
	groupIndex := m.currentGroupIndex()
	if groupIndex < m.offset {
		m.offset = groupIndex
	}
	if groupIndex >= m.offset+page {
		m.offset = groupIndex - page + 1
	}
	m.offset = clampInt(m.offset, 0, max(len(m.groups)-1, 0))
	memberPage := rowsViewportHeight(m.layout().context.h)
	contextRowIndex := m.currentContextRowOffset()
	if contextRowIndex < m.contextOffset {
		m.contextOffset = contextRowIndex
	}
	if contextRowIndex >= m.contextOffset+memberPage {
		m.contextOffset = contextRowIndex - memberPage + 1
	}
	m.contextOffset = clampInt(m.contextOffset, 0, m.maxContextOffset())
}

func (m model) pageSize() int {
	return max(1, rowsViewportHeight(m.layout().rows.h))
}
