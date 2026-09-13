package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func renderStyledTable(columns []tableColumn, rows []tableRow, offset, height, width int, headerColor string, styleForRow func(index int) lipgloss.Style) string {
	height = maxInt(1, height)
	width = maxInt(1, width)
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
	cells := make([]string, 0, minInt(len(columns), len(row)))
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
			title = strings.Repeat("  ", minInt(item.Depth, 6)) + "-> " + title
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
	width = maxInt(24, width)
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
			titleW := maxInt(1, width-countW-timeW-ageW-3)
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
		titleW := maxInt(1, width-countW-ageW-2)
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
		titleW := maxInt(1, width-countW-timeW-ageW-3)
		return []tableColumn{
			{Key: "count", Title: countLabel, Width: countW},
			{Key: "time", Title: activeTimeLabel("date", active), Width: timeW},
			{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
			{Key: "title", Title: activeLabel(titleLabel, active == sortTitle || active == sortContainer || active == sortAuthor), Width: titleW},
		}
	}
	kindW := minInt(maxInt(6, width/8), 10)
	countW := minInt(maxInt(4, width/12), 7)
	timeW := minInt(maxInt(12, width/5), 18)
	ageW := minInt(maxInt(4, width/16), 7)
	columns := []tableColumn{
		{Key: "kind", Title: activeLabel("kind", active == sortKind), Width: kindW},
		{Key: "count", Title: countLabel, Width: countW},
		{Key: "time", Title: activeTimeLabel("latest", active), Width: timeW},
		{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
	}
	if m.shouldShowGroupScopeColumn() {
		scopeW := minInt(maxInt(8, width/7), 16)
		titleW := maxInt(1, width-kindW-countW-timeW-ageW-scopeW-5)
		columns = append(columns, tableColumn{Key: "scope", Title: activeLabel("scope", active == sortScope), Width: scopeW})
		columns = append(columns, tableColumn{Key: "title", Title: activeLabel(titleLabel, active == sortTitle || active == sortContainer || active == sortAuthor), Width: titleW})
		return columns
	}
	titleW := maxInt(1, width-kindW-countW-timeW-ageW-4)
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
	width = maxInt(24, width)
	active := m.memberSortMode
	if width < 34 {
		whenW := 5
		titleW := maxInt(1, width-whenW-1)
		return []tableColumn{
			{Key: "time", Title: activeTimeLabel(m.memberTimeLabel(), active), Width: whenW},
			{Key: "title", Title: activeLabel("title", active == sortTitle), Width: titleW},
		}
	}
	if width < 54 {
		whenW := 5
		ageW := 4
		if m.layoutPreset == LayoutDocument {
			kindW := minInt(maxInt(5, width/7), 9)
			titleW := maxInt(1, width-whenW-ageW-kindW-3)
			return []tableColumn{
				{Key: "time", Title: activeTimeLabel("date", active), Width: whenW},
				{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
				{Key: "kind", Title: activeLabel("kind", active == sortKind), Width: kindW},
				{Key: "title", Title: activeLabel("title", active == sortTitle), Width: titleW},
			}
		}
		relationW := 3
		authorW := minInt(maxInt(5, width/7), 9)
		titleW := maxInt(1, width-whenW-ageW-relationW-authorW-4)
		return []tableColumn{
			{Key: "time", Title: activeTimeLabel(m.memberTimeLabel(), active), Width: whenW},
			{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
			{Key: "relation", Title: "rel", Width: relationW},
			{Key: "author", Title: activeLabel("who", active == sortAuthor), Width: authorW},
			{Key: "title", Title: activeLabel("title", active == sortTitle), Width: titleW},
		}
	}
	kindW := minInt(maxInt(5, width/10), 10)
	whenW := minInt(maxInt(10, width/6), 16)
	ageW := minInt(maxInt(4, width/16), 7)
	whereW := minInt(maxInt(10, width/5), 22)
	if m.layoutPreset == LayoutDocument {
		titleW := maxInt(1, width-kindW-whenW-ageW-whereW-4)
		return []tableColumn{
			{Key: "kind", Title: activeLabel("kind", active == sortKind), Width: kindW},
			{Key: "time", Title: activeTimeLabel("updated", active), Width: whenW},
			{Key: "age", Title: activeTimeLabel("age", active), Width: ageW},
			{Key: "container", Title: activeLabel("where", active == sortContainer || active == sortScope), Width: whereW},
			{Key: "title", Title: activeLabel("title", active == sortTitle), Width: titleW},
		}
	}
	authorW := minInt(maxInt(8, width/7), 18)
	relationW := 5
	titleW := maxInt(1, width-whenW-ageW-relationW-authorW-4)
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

func rowListLine(item Item, width int) string {
	width = maxInt(width, 1)
	title := displayRowTitle(item.Title)
	if item.Depth > 0 {
		title = strings.Repeat("  ", minInt(item.Depth, 6)) + "-> " + title
	}
	if width >= 24 && width < 68 {
		return compactRowListLine(item, title, width)
	}
	if width < 68 {
		return truncateCells(title, width)
	}
	kind := rowKind(item)
	when := rowWhen(item)
	age := rowAge(item)
	where := rowWhere(item)
	author := itemAuthor(item)
	kindW := minInt(maxInt(5, width/10), 10)
	whenW := minInt(maxInt(10, width/6), 16)
	ageW := minInt(maxInt(4, width/16), 7)
	whereW := minInt(maxInt(10, width/5), 22)
	authorW := minInt(maxInt(8, width/7), 18)
	titleW := maxInt(1, width-kindW-whenW-ageW-whereW-authorW-5)
	return padCells(truncateCells(kind, kindW), kindW) + " " +
		padCells(truncateCells(when, whenW), whenW) + " " +
		padCells(truncateCells(age, ageW), ageW) + " " +
		padCells(truncateCells(where, whereW), whereW) + " " +
		padCells(truncateCells(author, authorW), authorW) + " " +
		truncateCells(title, titleW)
}

func displayRowTitle(title string) string {
	if rendered := renderInlineMarkdown(title); rendered != "" {
		return compactTitle(rendered)
	}
	return compactTitle(title)
}

func compactRowListLine(item Item, title string, width int) string {
	if width < 34 {
		whenW := 5
		titleW := maxInt(1, width-whenW-1)
		return padCells(truncateCells(compactDate(item), whenW), whenW) + " " +
			truncateCells(title, titleW)
	}
	whenW := 5
	ageW := 4
	authorW := minInt(maxInt(5, width/6), 9)
	titleW := maxInt(1, width-whenW-ageW-authorW-3)
	return padCells(truncateCells(compactDate(item), whenW), whenW) + " " +
		padCells(truncateCells(rowAge(item), ageW), ageW) + " " +
		padCells(truncateCells(itemAuthor(item), authorW), authorW) + " " +
		truncateCells(title, titleW)
}

func groupListLine(group itemGroup, width int) string {
	width = maxInt(width, 1)
	if width >= 24 && width < 68 {
		return compactGroupListLine(group, width)
	}
	if width < 68 {
		return truncateCells(group.Title, width)
	}
	kindW := minInt(maxInt(6, width/8), 10)
	countW := minInt(maxInt(4, width/12), 7)
	timeW := minInt(maxInt(12, width/5), 18)
	ageW := minInt(maxInt(4, width/16), 7)
	scopeW := minInt(maxInt(8, width/7), 16)
	titleW := maxInt(1, width-kindW-countW-timeW-ageW-scopeW-5)
	return padCells(truncateCells(group.Kind, kindW), kindW) + " " +
		padCells(fmt.Sprintf("%d", group.Count), countW) + " " +
		padCells(truncateCells(shortTimestamp(group.Latest), timeW), timeW) + " " +
		padCells(truncateCells(ageFromTimestamp(group.Latest), ageW), ageW) + " " +
		padCells(truncateCells(group.Scope, scopeW), scopeW) + " " +
		truncateCells(group.Title, titleW)
}

func compactGroupListLine(group itemGroup, width int) string {
	countW := 3
	ageW := 4
	if width >= 36 && width < 44 {
		timeW := 5
		titleW := maxInt(1, width-countW-timeW-ageW-3)
		return padCells(fmt.Sprintf("%d", group.Count), countW) + " " +
			padCells(truncateCells(compactDateFromTimestamp(group.Latest), timeW), timeW) + " " +
			padCells(truncateCells(ageFromTimestamp(group.Latest), ageW), ageW) + " " +
			truncateCells(group.Title, titleW)
	}
	if width >= 44 {
		if width >= 52 {
			kindW := 8
			timeW := 5
			titleW := maxInt(1, width-kindW-countW-timeW-ageW-4)
			return padCells(truncateCells(group.Kind, kindW), kindW) + " " +
				padCells(fmt.Sprintf("%d", group.Count), countW) + " " +
				padCells(truncateCells(compactDateFromTimestamp(group.Latest), timeW), timeW) + " " +
				padCells(truncateCells(ageFromTimestamp(group.Latest), ageW), ageW) + " " +
				truncateCells(group.Title, titleW)
		}
		timeW := 5
		titleW := maxInt(1, width-countW-timeW-ageW-3)
		return padCells(fmt.Sprintf("%d", group.Count), countW) + " " +
			padCells(truncateCells(compactDateFromTimestamp(group.Latest), timeW), timeW) + " " +
			padCells(truncateCells(ageFromTimestamp(group.Latest), ageW), ageW) + " " +
			truncateCells(group.Title, titleW)
	}
	titleW := maxInt(1, width-countW-ageW-2)
	return padCells(fmt.Sprintf("%d", group.Count), countW) + " " +
		padCells(truncateCells(ageFromTimestamp(group.Latest), ageW), ageW) + " " +
		truncateCells(group.Title, titleW)
}

func groupListHeader(width int, active sortMode) string {
	width = maxInt(width, 1)
	if width >= 24 && width < 68 {
		return tagStyle(width).Bold(true).Render(compactGroupListHeader(width, active))
	}
	if width < 68 {
		return tagStyle(width).Render(padCells("GROUP", width))
	}
	kindW := minInt(maxInt(6, width/8), 10)
	countW := minInt(maxInt(4, width/12), 7)
	timeW := minInt(maxInt(12, width/5), 18)
	ageW := minInt(maxInt(4, width/16), 7)
	scopeW := minInt(maxInt(8, width/7), 16)
	titleW := maxInt(1, width-kindW-countW-timeW-ageW-scopeW-5)
	kind := "TYPE"
	count := "COUNT"
	when := "LATEST"
	age := "AGE"
	scope := "SCOPE"
	title := "GROUP"
	switch active {
	case sortKind:
		kind = "TYPE v"
	case sortNewest, sortOldest:
		when = "LATEST v"
	case sortScope:
		scope = "SCOPE v"
	case sortTitle, sortContainer, sortAuthor:
		title = "GROUP v"
	}
	line := padCells(truncateCells(kind, kindW), kindW) + " " +
		padCells(truncateCells(count, countW), countW) + " " +
		padCells(truncateCells(when, timeW), timeW) + " " +
		padCells(truncateCells(age, ageW), ageW) + " " +
		padCells(truncateCells(scope, scopeW), scopeW) + " " +
		truncateCells(title, titleW)
	return tagStyle(width).Bold(true).Render(line)
}

func compactGroupListHeader(width int, active sortMode) string {
	count := "N"
	age := "AGE"
	title := "GROUP"
	if active == sortNewest || active == sortOldest {
		age = "AGE v"
	}
	if active == sortTitle || active == sortContainer || active == sortAuthor {
		title = "GROUP v"
	}
	countW := 3
	ageW := 4
	if width >= 36 && width < 44 {
		timeLabel := "TIME"
		if active == sortNewest || active == sortOldest {
			timeLabel = "TIME v"
		}
		titleW := maxInt(1, width-countW-5-ageW-3)
		return padCells(truncateCells(count, countW), countW) + " " +
			padCells(truncateCells(timeLabel, 5), 5) + " " +
			padCells(truncateCells(age, ageW), ageW) + " " +
			truncateCells(title, titleW)
	}
	if width >= 44 {
		if width >= 52 {
			kindW := 8
			kind := "TYPE"
			if active == sortKind {
				kind = "TYPE v"
			}
			timeLabel := "TIME"
			if active == sortNewest || active == sortOldest {
				timeLabel = "TIME v"
			}
			titleW := maxInt(1, width-kindW-countW-ageW-5-4)
			return padCells(truncateCells(kind, kindW), kindW) + " " +
				padCells(truncateCells(count, countW), countW) + " " +
				padCells(truncateCells(timeLabel, 5), 5) + " " +
				padCells(truncateCells(age, ageW), ageW) + " " +
				truncateCells(title, titleW)
		}
		timeLabel := "TIME"
		if active == sortNewest || active == sortOldest {
			timeLabel = "TIME v"
		}
		titleW := maxInt(1, width-countW-5-ageW-3)
		return padCells(truncateCells(count, countW), countW) + " " +
			padCells(truncateCells(timeLabel, 5), 5) + " " +
			padCells(truncateCells(age, ageW), ageW) + " " +
			truncateCells(title, titleW)
	}
	titleW := maxInt(1, width-countW-ageW-2)
	return padCells(truncateCells(count, countW), countW) + " " +
		padCells(truncateCells(age, ageW), ageW) + " " +
		truncateCells(title, titleW)
}

func rowListHeader(width int, active sortMode) string {
	width = maxInt(width, 1)
	if width >= 24 && width < 68 {
		return tagStyle(width).Bold(true).Render(compactRowListHeader(width, active))
	}
	if width < 68 {
		return tagStyle(width).Render(padCells("TITLE", width))
	}
	kindW := minInt(maxInt(5, width/10), 10)
	whenW := minInt(maxInt(10, width/6), 16)
	ageW := minInt(maxInt(4, width/16), 7)
	whereW := minInt(maxInt(10, width/5), 22)
	authorW := minInt(maxInt(8, width/7), 18)
	titleW := maxInt(1, width-kindW-whenW-ageW-whereW-authorW-5)
	kind := "KIND"
	when := "TIME"
	age := "AGE"
	where := "WHERE"
	author := "AUTHOR"
	title := "TITLE"
	switch active {
	case sortKind:
		kind = "KIND v"
	case sortScope, sortContainer, sortAuthor, sortNewest, sortOldest:
		if active == sortAuthor {
			author = "AUTHOR v"
		} else if active == sortScope || active == sortContainer {
			where = "WHERE v"
		} else {
			when = "WHEN v"
		}
	case sortTitle:
		title = "TITLE v"
	}
	line := padCells(truncateCells(kind, kindW), kindW) + " " +
		padCells(truncateCells(when, whenW), whenW) + " " +
		padCells(truncateCells(age, ageW), ageW) + " " +
		padCells(truncateCells(where, whereW), whereW) + " " +
		padCells(truncateCells(author, authorW), authorW) + " " +
		truncateCells(title, titleW)
	return tagStyle(width).Bold(true).Render(line)
}

func compactRowListHeader(width int, active sortMode) string {
	if width < 34 {
		whenW := 5
		titleW := maxInt(1, width-whenW-1)
		return padCells(truncateCells("TIME", whenW), whenW) + " " + truncateCells("TITLE", titleW)
	}
	timeLabel := "TIME"
	age := "AGE"
	author := "WHO"
	title := "TITLE"
	switch active {
	case sortNewest, sortOldest:
		age = "AGE v"
	case sortAuthor:
		author = "WHO v"
	case sortTitle:
		title = "TITLE v"
	}
	whenW := 5
	ageW := 4
	authorW := minInt(maxInt(5, width/6), 9)
	titleW := maxInt(1, width-whenW-ageW-authorW-3)
	return padCells(truncateCells(timeLabel, whenW), whenW) + " " +
		padCells(truncateCells(age, ageW), ageW) + " " +
		padCells(truncateCells(author, authorW), authorW) + " " +
		truncateCells(title, titleW)
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
