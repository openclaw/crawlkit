package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m model) View() string {
	width := maxInt(m.width, 40)
	height := m.height
	if height <= 0 {
		height = 12
	}
	layout := m.layout()
	header := m.renderHeader(width)
	rows := m.renderRowsPane(layout.rows)
	context := m.renderContextPane(layout.context)
	detail := m.renderDetailPane(layout.detail)
	footer := m.renderFooter(width)
	var body string
	if layout.stacked {
		if layout.context.x == 0 {
			body = lipgloss.JoinVertical(lipgloss.Left, rows, context, detail)
		} else {
			top := lipgloss.JoinHorizontal(lipgloss.Top, rows, context)
			body = lipgloss.JoinVertical(lipgloss.Left, top, detail)
		}
	} else {
		if layout.detail.y > layout.context.y {
			right := lipgloss.JoinVertical(lipgloss.Left, context, detail)
			body = lipgloss.JoinHorizontal(lipgloss.Top, rows, right)
		} else {
			body = lipgloss.JoinHorizontal(lipgloss.Top, rows, context, detail)
		}
	}
	body = fitBlock(body, width, maxInt(1, layout.footerY()-1))
	view := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	if m.menuOpen && m.menuFloating {
		view = m.renderFloatingMenu(view)
	}
	return fitBlock(view, width, height)
}

func (m model) renderHeader(width int) string {
	status := fmt.Sprintf("%d/%d rows", len(m.filtered), len(m.items))
	if m.query != "" {
		status += " filtered by " + strconvQuote(m.query)
	}
	status += "  sort:" + m.sortMode.Label()
	status += "  members:" + m.memberSortMode.Label()
	status += "  group:" + groupModeLabel(m.layoutPreset, m.groupMode)
	status += "  layout:" + m.layout().mode
	status += "  detail:" + detailModeLabel(m.compactDetail)
	line := m.title + "  " + status
	if m.filterMode {
		line += "  filter> " + m.query
	} else if m.jumpMode {
		line += "  jump> " + m.jumpQuery
	} else if m.menuOpen {
		line += "  menu> " + m.displayMenuTitle()
	}
	return titleStyle(width).Render(padCells(" "+truncateCells(line, maxInt(1, width-2)), width))
}

func (m model) renderRowsPane(rect rect) string {
	width := tableViewportWidth(rect)
	height := rowsViewportHeight(rect.h)
	columns := m.groupColumns(width)
	rows := m.groupTableRows(columns)
	if len(m.groups) == 0 {
		rows = []tableRow{messageTableRow(columns, "no rows match")}
	}
	current := m.currentGroupIndex()
	tableView := renderStyledTable(columns, rows, m.offset, height, width, rowsPaneAccent, func(index int) lipgloss.Style {
		if index < 0 || index >= len(m.groups) {
			return lipgloss.NewStyle().Foreground(lipgloss.Color(archiveTextFG))
		}
		return rowStyle(width, index == current, m.focus == focusRows, false)
	})
	content := lipgloss.JoinVertical(lipgloss.Left, paneTitleWithLabelForWidth(m.groupPaneTitle(), focusRows, m.focus, m.groupPositionLabel(), width), tableView)
	return paneStyle(focusRows, m.focus, rect.w, rect.h, rowsPaneAccent).Render(content)
}

func (m model) renderContextPane(rect rect) string {
	width := tableViewportWidth(rect)
	height := rowsViewportHeight(rect.h)
	group, ok := m.currentGroup()
	if !ok {
		return pane(m.memberPaneTitle(), "", []string{"No group selected."}, rect, focusContext, m.focus, contextPaneAccent)
	}
	contextRows := m.currentContextRows()
	columns := m.memberColumns(width)
	rows := m.memberTableRows(columns, contextRows)
	if len(contextRows) == 0 {
		rows = []tableRow{messageTableRow(columns, "no rows in group")}
	}
	selectedItem := m.currentItemIndex()
	tableView := renderStyledTable(columns, rows, m.contextOffset, height, width, contextPaneAccent, func(index int) lipgloss.Style {
		if index < 0 || index >= len(contextRows) {
			return lipgloss.NewStyle().Foreground(lipgloss.Color(archiveTextFG))
		}
		row := contextRows[index]
		if !row.Selectable {
			return sectionRowStyle(width)
		}
		if row.ItemIndex < 0 || row.ItemIndex >= len(m.items) {
			return lipgloss.NewStyle().Foreground(lipgloss.Color(archiveTextFG))
		}
		return rowStyle(width, row.ItemIndex == selectedItem, m.focus == focusContext, itemInactive(m.items[row.ItemIndex]))
	})
	content := lipgloss.JoinVertical(lipgloss.Left, paneTitleWithLabelForWidth(m.memberPaneTitle(), focusContext, m.focus, m.memberPositionLabel()+"  "+group.Title, width), tableView)
	return paneStyle(focusContext, m.focus, rect.w, rect.h, contextPaneAccent).Render(content)
}

func (m model) renderDetailPane(rect rect) string {
	if m.menuOpen && !m.menuFloating {
		return m.renderDetailViewport(rect, m.menuLines(paneContentWidth(rect.w)))
	}
	if m.showHelp {
		return m.renderDetailViewport(rect, m.helpLines(paneContentWidth(rect.w)))
	}
	item, ok := m.selectedItem()
	if !ok {
		return pane("Detail", "", []string{"No row selected."}, rect, focusDetail, m.focus, detailPaneAccent)
	}
	lines := m.detailLinesForWidth(item, paneContentWidth(rect.w))
	return m.renderDetailViewport(rect, lines)
}

func (m model) renderDetailViewport(rect rect, lines []string) string {
	m.configureDetailViewport(rect, lines)
	return paneStyle(focusDetail, m.focus, rect.w, rect.h, detailPaneAccent).Render(m.detailView.View())
}

func (m *model) syncDetailViewport() {
	item, ok := m.selectedItem()
	if !ok {
		return
	}
	rect := m.layout().detail
	m.configureDetailViewport(rect, m.detailLinesForWidth(item, paneContentWidth(rect.w)))
}

func (m *model) configureDetailViewport(rect rect, lines []string) {
	title := detailModeLabel(m.compactDetail)
	if focus := paneFocusLabel(m.focus == focusDetail); focus != "" {
		title += "  " + focus
	}
	content := append([]string{paneTitleWithLabelForWidth(m.detailPaneTitle(), focusDetail, m.focus, title, paneContentWidth(rect.w))}, lines...)
	m.detailView.Width = paneContentWidth(rect.w)
	m.detailView.Height = maxInt(1, rect.h-2)
	m.detailView.MouseWheelEnabled = true
	m.detailView.MouseWheelDelta = 3
	m.detailView.SetContent(strings.Join(content, "\n"))
}

func (m model) renderFooter(width int) string {
	line := firstNonEmpty(m.status, "Ready")
	if m.filterMode {
		line = "Filtering"
	} else if m.jumpMode {
		line = "Jump: " + m.jumpQuery
	}
	if m.refreshing {
		line = "Refreshing " + m.refreshSourceLabel() + "  " + line
	}
	if location := m.footerLocation(); location != "" {
		line += "  " + location
	}
	controls := footerControls(width)
	bg, fg := footerPalette(m.sourceKind)
	statusLine := padCells(" "+truncateCells(line, maxInt(1, width-2)), width)
	controlsLine := padCells(" "+truncateCells(controls, maxInt(1, width-2)), width)
	return lipgloss.NewStyle().Width(width).Height(2).Background(bg).Foreground(fg).Render(statusLine + "\n" + controlsLine)
}

func footerControls(width int) string {
	full := "Tab focus  click select  right-click menu  a actions  header sort  wheel scroll  / filter  # jump  o open  c copy  s sort  m members  v group  d detail  r refresh  l layout  ? help  q quit"
	if lipgloss.Width(full) <= maxInt(1, width-2) {
		return full
	}
	compact := "Tab focus  click select  right-click menu  a actions  wheel scroll  / filter  # jump  r refresh  ? help  q quit"
	if lipgloss.Width(compact) <= maxInt(1, width-2) {
		return compact
	}
	return "Tab focus click right-click menu a actions / filter # jump ? help q quit"
}

func (m model) groupPositionLabel() string {
	if len(m.groups) == 0 {
		return "0/0"
	}
	return fmt.Sprintf("%d/%d groups", m.currentGroupIndex()+1, len(m.groups))
}

func (m model) memberPositionLabel() string {
	members := m.currentGroupMembers()
	if len(members) == 0 {
		return "0/0"
	}
	return fmt.Sprintf("%d/%d rows", m.currentMemberOffset()+1, len(members))
}

func (m model) groupPaneTitle() string {
	switch m.layoutPreset {
	case LayoutChat:
		switch m.groupMode {
		case groupByAuthor:
			return "People"
		case groupByThread:
			return "Threads"
		default:
			return "Channels"
		}
	case LayoutDocument:
		switch m.groupMode {
		case groupByContainer:
			return "Databases"
		case groupByScope:
			return "Workspaces"
		default:
			return "Parents"
		}
	default:
		return "Groups"
	}
}

func (m model) memberPaneTitle() string {
	switch m.layoutPreset {
	case LayoutChat:
		return "Messages"
	case LayoutDocument:
		return "Pages / Databases"
	default:
		return "Items"
	}
}

func (m model) detailPaneTitle() string {
	if m.layoutPreset == LayoutChat {
		return "Thread"
	}
	if m.layoutPreset == LayoutDocument {
		return "Page"
	}
	return "Detail"
}

func nextFocus(focus paneFocus, delta int) paneFocus {
	next := int(focus) + delta
	if next < int(focusRows) {
		return focusDetail
	}
	if next > int(focusDetail) {
		return focusRows
	}
	return paneFocus(next)
}

func paneFocusLabel(focused bool) string {
	if focused {
		return "focused"
	}
	return ""
}

func detailModeToggleLabel(compact bool) string {
	if compact {
		return "Show full detail"
	}
	return "Show compact detail"
}

func detailModeLabel(compact bool) string {
	if compact {
		return "compact"
	}
	return "full"
}

func groupModeCycle(layout LayoutPreset) []groupMode {
	switch layout {
	case LayoutChat:
		return []groupMode{groupByDefault, groupByAuthor, groupByThread}
	case LayoutDocument:
		return []groupMode{groupByDefault, groupByContainer, groupByScope}
	default:
		return []groupMode{groupByDefault, groupByContainer, groupByAuthor, groupByScope}
	}
}

func groupModeLabel(layout LayoutPreset, mode groupMode) string {
	switch mode {
	case groupByContainer:
		if layout == LayoutDocument {
			return "database"
		}
		return "channel"
	case groupByAuthor:
		return "person"
	case groupByThread:
		return "thread"
	case groupByScope:
		if layout == LayoutDocument {
			return "workspace"
		}
		return "scope"
	default:
		if layout == LayoutChat {
			return "channel"
		}
		if layout == LayoutDocument {
			return "parent"
		}
		return "group"
	}
}

func groupModeToggleLabel(layout LayoutPreset, mode groupMode) string {
	order := groupModeCycle(layout)
	next := order[0]
	for index, item := range order {
		if item == mode {
			next = order[(index+1)%len(order)]
			break
		}
	}
	return "Group by " + groupModeLabel(layout, next)
}

func paneTitleForWidth(pane, focus paneFocus, suffix string, width int) string {
	label := map[paneFocus]string{
		focusRows:    "Rows",
		focusContext: "Context",
		focusDetail:  "Detail",
	}[pane]
	if strings.TrimSpace(suffix) != "" && strings.TrimSpace(suffix) != label {
		label += " " + suffix
	}
	prefix := "[ ] "
	if pane == focus {
		prefix = "[*] "
	}
	if width > 0 {
		label = truncateCells(label, maxInt(1, width-lipgloss.Width(prefix)))
	}
	return bold(prefix + label)
}

func paneTitleWithLabelForWidth(label string, pane, focus paneFocus, suffix string, width int) string {
	label = strings.TrimSpace(label)
	if label == "" {
		label = map[paneFocus]string{
			focusRows:    "Rows",
			focusContext: "Context",
			focusDetail:  "Detail",
		}[pane]
	}
	if strings.TrimSpace(suffix) != "" {
		label += " " + strings.TrimSpace(suffix)
	}
	prefix := "[ ] "
	if pane == focus {
		prefix = "[*] "
	}
	if width > 0 {
		label = truncateCells(label, maxInt(1, width-lipgloss.Width(prefix)))
	}
	return bold(prefix + label)
}

func normalizeSourceKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SourceRemote:
		return SourceRemote
	default:
		return SourceLocal
	}
}

func footerPalette(source string) (lipgloss.Color, lipgloss.Color) {
	switch normalizeSourceKind(source) {
	case SourceRemote:
		return lipgloss.Color(archiveRemoteFooterBG), lipgloss.Color(archiveFooterFG)
	default:
		return lipgloss.Color(archiveLocalFooterBG), lipgloss.Color(archiveFooterFG)
	}
}
