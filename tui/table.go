package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func renderStyledTable(columns []tableColumn, rows []tableRow, offset, height, width int, headerColor string, styleForRow func(index int) lipgloss.Style) string {
	height = max(1, height)
	width = max(1, width)
	lines := make([]string, 0, height+1)
	lines = append(lines, renderTableHeader(columns, width, headerColor))
	for line := 0; line < height; line++ {
		index := offset + line
		if index < 0 || index >= len(rows) {
			lines = append(lines, lipgloss.NewStyle().Width(width).Render(""))
			continue
		}
		lines = append(lines, renderTableRow(columns, rows[index], width, styleForRow(index)))
	}
	return strings.Join(lines, "\n")
}

func renderTableHeader(columns []tableColumn, width int, headerColor string) string {
	values := make(tableRow, 0, len(columns))
	for _, column := range columns {
		values = append(values, column.Title)
	}
	line := truncateCells(renderTableCells(columns, values), width)
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(headerColor)).Width(width).Render(line)
}

func renderTableRow(columns []tableColumn, row tableRow, width int, rowStyle lipgloss.Style) string {
	line := truncateCells(renderTableCells(columns, row), width)
	return rowStyle.Width(width).Render(line)
}

func renderTableCells(columns []tableColumn, row tableRow) string {
	cells := make([]string, 0, min(len(columns), len(row)))
	for index, value := range row {
		if index >= len(columns) || columns[index].Width <= 0 {
			continue
		}
		column := columns[index]
		cells = append(cells, padCells(truncateCells(value, column.Width), column.Width))
	}
	return strings.Join(cells, " ")
}

func messageTableRow(columns []tableColumn, message string) tableRow {
	row := make(tableRow, len(columns))
	if len(row) > 0 {
		row[len(row)-1] = message
	}
	return row
}

func (m model) groupTableRows(columns []tableColumn) []tableRow {
	if len(m.groups) == 0 {
		return nil
	}
	rows := make([]tableRow, 0, len(m.groups))
	for _, group := range m.groups {
		row := make(tableRow, 0, len(columns))
		for _, column := range columns {
			switch column.Key {
			case "kind":
				row = append(row, group.Kind)
			case "count":
				row = append(row, fmt.Sprintf("%d", group.Count))
			case "time":
				row = append(row, groupTimeForColumn(group.Latest, column.Width))
			case "age":
				row = append(row, ageFromTimestamp(group.Latest))
			case "scope":
				row = append(row, group.Scope)
			default:
				row = append(row, group.Title)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func (m model) memberTableRows(columns []tableColumn, contextRows []contextRow) []tableRow {
	rows := make([]tableRow, 0, len(contextRows))
	for _, contextRow := range contextRows {
		if !contextRow.Selectable {
			rows = append(rows, messageTableRow(columns, contextRow.Label))
			continue
		}
		if contextRow.ItemIndex < 0 || contextRow.ItemIndex >= len(m.items) {
			continue
		}
		item := m.items[contextRow.ItemIndex]
		title := displayRowTitle(item.Title)
		if item.Depth > 0 {
			title = strings.Repeat("  ", min(item.Depth, 6)) + "-> " + title
		}
		row := make(tableRow, 0, len(columns))
		for _, column := range columns {
			switch column.Key {
			case "kind":
				row = append(row, rowKind(item))
			case "time":
				row = append(row, rowTimeForColumn(item, column.Width))
			case "age":
				row = append(row, rowAge(item))
			case "container":
				row = append(row, rowWhere(item))
			case "author":
				row = append(row, itemAuthor(item))
			case "relation":
				row = append(row, chatRelationForColumn(item, column.Width))
			default:
				row = append(row, title)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func (m model) groupColumns(width int) []tableColumn {
	width = max(24, width)
	active := m.sortMode
	countLabel := activeLabel(m.groupCountLabel(width), active == sortCount)
	titleLabel := m.groupTitleLabel()
	if width < 44 {
		if width >= 36 {
			countW := 3
			if active == sortCount {
				countW = 4
			}
			timeW := 5
			ageW := 4
			titleW := max(1, width-countW-timeW-ageW-3)
			return []tableColumn{
				{Key: "count", Title: countLabel, Width: countW},
				{Key: "time", Title: activeTimeLabel("date", active), Width: timeW},
				{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
				{Key: "title", Title: activeLabel(titleLabel, active == sortTitle || active == sortContainer || active == sortAuthor), Width: titleW},
			}
		}
		countW := 3
		if active == sortCount {
			countW = 4
		}
		ageW := 4
		titleW := max(1, width-countW-ageW-2)
		return []tableColumn{
			{Key: "count", Title: countLabel, Width: countW},
			{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
			{Key: "title", Title: activeLabel(titleLabel, active == sortTitle || active == sortContainer || active == sortAuthor), Width: titleW},
		}
	}
	if width < 68 {
		countW := 3
		if active == sortCount {
			countW = 4
		}
		timeW := 5
		ageW := 4
		titleW := max(1, width-countW-timeW-ageW-3)
		return []tableColumn{
			{Key: "count", Title: countLabel, Width: countW},
			{Key: "time", Title: activeTimeLabel("date", active), Width: timeW},
			{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
			{Key: "title", Title: activeLabel(titleLabel, active == sortTitle || active == sortContainer || active == sortAuthor), Width: titleW},
		}
	}
	kindW := min(max(6, width/8), 10)
	countW := min(max(4, width/12), 7)
	timeW := min(max(12, width/5), 18)
	ageW := min(max(4, width/16), 7)
	columns := []tableColumn{
		{Key: "kind", Title: activeLabel("kind", active == sortKind), Width: kindW},
		{Key: "count", Title: countLabel, Width: countW},
		{Key: "time", Title: activeTimeLabel("latest", active), Width: timeW},
		{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
	}
	if m.shouldShowGroupScopeColumn() {
		scopeW := min(max(8, width/7), 16)
		titleW := max(1, width-kindW-countW-timeW-ageW-scopeW-5)
		columns = append(columns, tableColumn{Key: "scope", Title: activeLabel("scope", active == sortScope), Width: scopeW})
		columns = append(columns, tableColumn{Key: "title", Title: activeLabel(titleLabel, active == sortTitle || active == sortContainer || active == sortAuthor), Width: titleW})
		return columns
	}
	titleW := max(1, width-kindW-countW-timeW-ageW-4)
	columns = append(columns, tableColumn{Key: "title", Title: activeLabel(titleLabel, active == sortTitle || active == sortContainer || active == sortAuthor), Width: titleW})
	return columns
}

func (m model) shouldShowGroupScopeColumn() bool {
	seen := map[string]struct{}{}
	for _, group := range m.groups {
		scope := strings.TrimSpace(group.Scope)
		if scope == "" {
			continue
		}
		seen[strings.ToLower(scope)] = struct{}{}
		if m.layoutPreset != LayoutChat || len(seen) > 1 {
			return true
		}
	}
	return false
}

func (m model) groupCountLabel(width int) string {
	switch m.layoutPreset {
	case LayoutChat:
		if width < 68 {
			return "msg"
		}
		return "msgs"
	case LayoutDocument:
		if width < 68 {
			return "doc"
		}
		return "docs"
	default:
		if width < 68 {
			return "row"
		}
		return "rows"
	}
}

func (m model) groupTitleLabel() string {
	return groupModeLabel(m.layoutPreset, m.groupMode)
}

func (m model) memberColumns(width int) []tableColumn {
	width = max(24, width)
	active := m.memberSortMode
	if width < 34 {
		whenW := 5
		titleW := max(1, width-whenW-1)
		return []tableColumn{
			{Key: "time", Title: activeTimeLabel(m.memberTimeLabel(), active), Width: whenW},
			{Key: "title", Title: activeLabel("title", active == sortTitle), Width: titleW},
		}
	}
	if width < 54 {
		whenW := 5
		ageW := 4
		if m.layoutPreset == LayoutDocument {
			kindW := min(max(5, width/7), 9)
			titleW := max(1, width-whenW-ageW-kindW-3)
			return []tableColumn{
				{Key: "time", Title: activeTimeLabel("date", active), Width: whenW},
				{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
				{Key: "kind", Title: activeLabel("kind", active == sortKind), Width: kindW},
				{Key: "title", Title: activeLabel("title", active == sortTitle), Width: titleW},
			}
		}
		relationW := 3
		authorW := min(max(5, width/7), 9)
		titleW := max(1, width-whenW-ageW-relationW-authorW-4)
		return []tableColumn{
			{Key: "time", Title: activeTimeLabel(m.memberTimeLabel(), active), Width: whenW},
			{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
			{Key: "relation", Title: "rel", Width: relationW},
			{Key: "author", Title: activeLabel("who", active == sortAuthor), Width: authorW},
			{Key: "title", Title: activeLabel("title", active == sortTitle), Width: titleW},
		}
	}
	kindW := min(max(5, width/10), 10)
	whenW := min(max(10, width/6), 16)
	ageW := min(max(4, width/16), 7)
	whereW := min(max(10, width/5), 22)
	if m.layoutPreset == LayoutDocument {
		titleW := max(1, width-kindW-whenW-ageW-whereW-4)
		return []tableColumn{
			{Key: "kind", Title: activeLabel("kind", active == sortKind), Width: kindW},
			{Key: "time", Title: activeTimeLabel("updated", active), Width: whenW},
			{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
			{Key: "container", Title: activeLabel("where", active == sortContainer || active == sortScope), Width: whereW},
			{Key: "title", Title: activeLabel("title", active == sortTitle), Width: titleW},
		}
	}
	authorW := min(max(8, width/7), 18)
	relationW := 5
	titleW := max(1, width-whenW-ageW-relationW-authorW-4)
	return []tableColumn{
		{Key: "time", Title: activeTimeLabel("time", active), Width: whenW},
		{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
		{Key: "relation", Title: "type", Width: relationW},
		{Key: "author", Title: activeLabel("who", active == sortAuthor), Width: authorW},
		{Key: "title", Title: activeLabel("title", active == sortTitle), Width: titleW},
	}
}

func (m model) memberTimeLabel() string {
	if m.layoutPreset == LayoutDocument {
		return "date"
	}
	return "time"
}

func activeLabel(label string, active bool) string {
	if active {
		return label + "*"
	}
	return label
}

func activeTimeLabel(label string, active sortMode) string {
	switch active {
	case sortNewest:
		return label + "-"
	case sortOldest:
		return label + "+"
	default:
		return label
	}
}

func columnLeftEdge(columns []tableColumn, index int) int {
	left := 0
	for i := 0; i < index && i < len(columns); i++ {
		left += columns[i].Width + 1
	}
	return left
}

func columnRightEdge(columns []tableColumn, index int) int {
	if index < 0 || index >= len(columns) {
		return 0
	}
	return columnLeftEdge(columns, index) + columns[index].Width
}

func columnAt(columns []tableColumn, x int) tableColumn {
	if len(columns) == 0 {
		return tableColumn{}
	}
	for index, column := range columns {
		if x < columnRightEdge(columns, index) {
			return column
		}
	}
	return columns[len(columns)-1]
}

func displayRowTitle(title string) string {
	if rendered := renderInlineMarkdown(title); rendered != "" {
		return compactTitle(rendered)
	}
	return compactTitle(title)
}

func (m *model) sortGroupsFromHeader(x, width int) {
	column := columnAt(m.groupColumns(width), x)
	switch column.Key {
	case "kind":
		m.setSortMode(sortKind)
	case "count":
		m.setSortMode(sortCount)
	case "time", "age":
		m.toggleTimeSort()
	case "scope":
		m.setSortMode(sortScope)
	default:
		m.setSortMode(sortTitle)
	}
}

func (m *model) sortMembersFromHeader(x, width int) {
	column := columnAt(m.memberColumns(width), x)
	switch column.Key {
	case "kind":
		m.setMemberSortMode(sortKind)
	case "time", "age":
		m.toggleMemberTimeSort()
	case "container":
		m.setMemberSortMode(sortContainer)
	case "author":
		m.setMemberSortMode(sortAuthor)
	default:
		m.setMemberSortMode(sortTitle)
	}
}

func (m *model) toggleTimeSort() {
	if m.sortMode == sortNewest {
		m.setSortMode(sortOldest)
		return
	}
	m.setSortMode(sortNewest)
}

func (m *model) toggleMemberTimeSort() {
	if m.memberSortMode == sortNewest {
		m.setMemberSortMode(sortOldest)
		return
	}
	m.setMemberSortMode(sortNewest)
}
