package tui

import (
	"fmt"
	"sort"
	"strings"
)

func (m *model) setSortMode(mode sortMode) {
	m.sortMode = mode
	m.applyFilter()
	m.closeMenu()
}

func (m *model) setMemberSortMode(mode sortMode) {
	m.memberSortMode = mode
	for index := range m.groups {
		m.sortGroupMembers(m.groups[index].Members)
	}
	m.ensureVisible()
	m.closeMenu()
}

func (m *model) setPaneSortMode(mode sortMode) {
	if m.menuContext == focusContext {
		m.setMemberSortMode(mode)
		return
	}
	m.setSortMode(mode)
}

func (m *model) cycleSortMode() {
	order := []sortMode{sortCount, sortNewest}
	m.setSortMode(nextSortMode(m.sortMode, order))
	m.status = "Sort: " + m.sortMode.Label()
}

func (m *model) cycleMemberSortMode() {
	order := []sortMode{sortDefault, sortNewest, sortOldest, sortTitle}
	if m.layoutPreset == LayoutChat {
		order = []sortMode{sortDefault, sortNewest, sortOldest, sortAuthor, sortTitle}
	} else if m.layoutPreset == LayoutDocument {
		order = []sortMode{sortDefault, sortNewest, sortOldest, sortTitle, sortKind, sortContainer}
	}
	m.setMemberSortMode(nextSortMode(m.memberSortMode, order))
	m.status = "Member sort: " + m.memberSortMode.Label()
}

func nextSortMode(current sortMode, order []sortMode) sortMode {
	if len(order) == 0 {
		return sortDefault
	}
	for index, mode := range order {
		if mode == current {
			return order[(index+1)%len(order)]
		}
	}
	return order[0]
}

func (m model) currentItemIndex() int {
	if len(m.filtered) == 0 || m.selected < 0 || m.selected >= len(m.filtered) {
		return -1
	}
	return m.filtered[m.selected]
}

func (m *model) sortFiltered() {
	if m.sortMode == sortDefault {
		return
	}
	sort.SliceStable(m.filtered, func(i, j int) bool {
		left := m.items[m.filtered[i]]
		right := m.items[m.filtered[j]]
		if less, ok := compareItems(left, right, m.sortMode); ok {
			return less
		}
		return m.filtered[i] < m.filtered[j]
	})
}

func (m *model) buildGroups() {
	byKey := make(map[string]int)
	groups := make([]itemGroup, 0)
	for _, itemIndex := range m.filtered {
		if itemIndex < 0 || itemIndex >= len(m.items) {
			continue
		}
		item := m.items[itemIndex]
		key, title, kind, scope := m.groupFields(item)
		groupIndex, ok := byKey[key]
		if !ok {
			groupIndex = len(groups)
			byKey[key] = groupIndex
			groups = append(groups, itemGroup{
				Key:   key,
				Title: title,
				Kind:  kind,
				Scope: scope,
			})
		}
		group := &groups[groupIndex]
		group.Members = append(group.Members, itemIndex)
		group.Count++
		if newerTimestamp(item.UpdatedAt, group.Latest) {
			group.Latest = item.UpdatedAt
		}
		if newerTimestamp(item.CreatedAt, group.Latest) {
			group.Latest = item.CreatedAt
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if less, ok := compareGroups(groups[i], groups[j], m.sortMode); ok {
			return less
		}
		return strings.ToLower(groups[i].Title) < strings.ToLower(groups[j].Title)
	})
	for index := range groups {
		m.sortGroupMembers(groups[index].Members)
	}
	m.groups = groups
}

func (m model) sortGroupMembers(members []int) {
	if len(members) < 2 {
		return
	}
	sort.SliceStable(members, func(i, j int) bool {
		left := m.items[members[i]]
		right := m.items[members[j]]
		switch m.memberSortMode {
		case sortNewest:
			if less, ok := compareItemTime(left, right, true); ok {
				return less
			}
		case sortOldest:
			if less, ok := compareItemTime(left, right, false); ok {
				return less
			}
		case sortTitle:
			if less, ok := compareStrings(left.Title, right.Title); ok {
				return less
			}
		case sortKind:
			if less, ok := compareStrings(itemKind(left), itemKind(right)); ok {
				return less
			}
		case sortScope:
			if less, ok := compareStrings(itemScope(left), itemScope(right)); ok {
				return less
			}
		case sortContainer:
			if less, ok := compareStrings(itemContainer(left), itemContainer(right)); ok {
				return less
			}
		case sortAuthor:
			if less, ok := compareStrings(itemAuthor(left), itemAuthor(right)); ok {
				return less
			}
		default:
			if m.layoutPreset == LayoutChat {
				if less, ok := compareItemTime(left, right, false); ok {
					return less
				}
			}
			if m.layoutPreset == LayoutDocument {
				if less, ok := compareItemTime(left, right, true); ok {
					return less
				}
			}
		}
		return members[i] < members[j]
	})
}

func (m model) groupFields(item Item) (key, title, kind, scope string) {
	switch m.layoutPreset {
	case LayoutChat:
		if m.groupMode == groupByAuthor {
			if author := strings.TrimSpace(itemAuthor(item)); author != "" {
				scope := strings.TrimSpace(item.Scope)
				return scopedGroupKey("author", author, scope), displayLabel(author), "person", scope
			}
		}
		if m.groupMode == groupByThread {
			if thread := threadKey(item); thread != "" {
				scope := strings.TrimSpace(item.Scope)
				title := firstNonEmpty(item.Title, thread)
				return scopedGroupKey("thread", thread, scope), displayLabel(title), "thread", scope
			}
		}
		if container := strings.TrimSpace(item.Container); container != "" {
			scope := strings.TrimSpace(item.Scope)
			return scopedGroupKey("container", container, scope), displayLabel(container), "channel", scope
		}
		if author := strings.TrimSpace(itemAuthor(item)); author != "" {
			scope := strings.TrimSpace(item.Scope)
			return scopedGroupKey("author", author, scope), displayLabel(author), "person", scope
		}
	case LayoutDocument:
		if m.groupMode == groupByContainer {
			if container := strings.TrimSpace(item.Container); container != "" {
				scope := strings.TrimSpace(item.Scope)
				return scopedGroupKey("container", container, scope), displayLabel(container), "database", scope
			}
		}
		if m.groupMode == groupByScope {
			if scope := strings.TrimSpace(item.Scope); scope != "" {
				return "scope:" + scope, displayLabel(scope), "workspace", scope
			}
		}
		if parent := strings.TrimSpace(item.ParentID); parent != "" {
			scope := strings.TrimSpace(item.Scope)
			return scopedGroupKey("parent", parent, scope), displayLabel(parent), "parent", scope
		}
		if container := strings.TrimSpace(item.Container); container != "" {
			scope := strings.TrimSpace(item.Scope)
			return scopedGroupKey("container", container, scope), displayLabel(container), "database", scope
		}
	}
	for _, value := range []struct {
		prefix string
		title  string
		kind   string
	}{
		{"container:", strings.TrimSpace(item.Container), "container"},
		{"scope:", strings.TrimSpace(item.Scope), "scope"},
		{"author:", strings.TrimSpace(item.Author), "person"},
	} {
		if value.title != "" {
			return value.prefix + value.title, displayLabel(value.title), value.kind, strings.TrimSpace(item.Scope)
		}
	}
	title = firstNonEmpty(item.Title, item.ID, itemKind(item), "row")
	return "row:" + title, displayLabel(title), firstNonEmpty(itemKind(item), "row"), strings.TrimSpace(item.Scope)
}

func scopedGroupKey(kind, value, scope string) string {
	value = strings.TrimSpace(value)
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return kind + ":" + value
	}
	return kind + ":" + scope + "/" + value
}

func compareGroups(left, right itemGroup, mode sortMode) (bool, bool) {
	switch mode {
	case sortCount:
		if left.Count != right.Count {
			return left.Count > right.Count, true
		}
		return compareGroupTime(left, right, true)
	case sortNewest:
		return compareGroupTime(left, right, true)
	case sortOldest:
		return compareGroupTime(left, right, false)
	case sortTitle, sortContainer:
		return compareStrings(left.Title, right.Title)
	case sortKind:
		return compareStrings(left.Kind, right.Kind)
	case sortScope:
		return compareStrings(left.Scope, right.Scope)
	case sortAuthor:
		return compareStrings(left.Title, right.Title)
	default:
		if less, ok := compareGroupTime(left, right, true); ok {
			return less, true
		}
		return false, false
	}
}

func compareGroupTime(left, right itemGroup, newest bool) (bool, bool) {
	leftTime, leftOK := parseTimestamp(left.Latest)
	rightTime, rightOK := parseTimestamp(right.Latest)
	if leftOK != rightOK {
		return leftOK, true
	}
	if !leftOK {
		return false, false
	}
	if leftTime.Equal(rightTime) {
		return false, false
	}
	if newest {
		return leftTime.After(rightTime), true
	}
	return leftTime.Before(rightTime), true
}

func newerTimestamp(candidate, current string) bool {
	candidateTime, candidateOK := parseTimestamp(candidate)
	if !candidateOK {
		return false
	}
	currentTime, currentOK := parseTimestamp(current)
	return !currentOK || candidateTime.After(currentTime)
}

func (m model) selectedItem() (Item, bool) {
	if len(m.filtered) == 0 || m.selected < 0 || m.selected >= len(m.filtered) {
		return Item{}, false
	}
	index := m.filtered[m.selected]
	if index < 0 || index >= len(m.items) {
		return Item{}, false
	}
	return m.items[index], true
}

func (m model) currentGroup() (itemGroup, bool) {
	index := m.currentGroupIndex()
	if index < 0 || index >= len(m.groups) {
		return itemGroup{}, false
	}
	return m.groups[index], true
}

func (m model) currentGroupIndex() int {
	itemIndex := m.currentItemIndex()
	if itemIndex < 0 {
		if len(m.groups) == 0 {
			return 0
		}
		return clampInt(m.offset, 0, len(m.groups)-1)
	}
	for groupIndex, group := range m.groups {
		for _, member := range group.Members {
			if member == itemIndex {
				return groupIndex
			}
		}
	}
	return 0
}

func (m model) currentGroupMembers() []int {
	group, ok := m.currentGroup()
	if !ok {
		return nil
	}
	return group.Members
}

func (m model) currentContextRows() []contextRow {
	return m.contextRowsForMembers(m.currentGroupMembers())
}

func (m model) contextRowsForMembers(members []int) []contextRow {
	if len(members) == 0 {
		return nil
	}
	switch m.layoutPreset {
	case LayoutChat:
		if m.memberSortMode == sortDefault || m.memberSortMode == sortNewest || m.memberSortMode == sortOldest {
			return m.contextRowsWithSections(members, chatDateSectionLabel)
		}
	case LayoutDocument:
		if m.memberSortMode == sortDefault || m.memberSortMode == sortKind {
			return m.contextRowsWithSections(members, documentKindSectionLabel)
		}
	}
	rows := make([]contextRow, 0, len(members))
	for _, itemIndex := range members {
		rows = append(rows, contextRow{ItemIndex: itemIndex, Selectable: true})
	}
	return rows
}

func (m model) contextRowsWithSections(members []int, labelFor func(Item) string) []contextRow {
	labels := make([]string, len(members))
	counts := map[string]int{}
	for index, itemIndex := range members {
		if itemIndex < 0 || itemIndex >= len(m.items) {
			continue
		}
		label := labelFor(m.items[itemIndex])
		if strings.TrimSpace(label) == "" {
			label = "OTHER"
		}
		labels[index] = label
		counts[label]++
	}
	rows := make([]contextRow, 0, len(members)+len(counts))
	previous := ""
	for index, itemIndex := range members {
		label := labels[index]
		if label != "" && label != previous {
			rows = append(rows, contextRow{Label: fmt.Sprintf("%s  (%d)", label, counts[label])})
			previous = label
		}
		rows = append(rows, contextRow{ItemIndex: itemIndex, Selectable: true})
	}
	return rows
}

func chatDateSectionLabel(item Item) string {
	if t, ok := itemSortTime(item); ok {
		return strings.ToUpper(t.UTC().Format("2006-01-02 Mon"))
	}
	return "UNDATED"
}

func documentKindSectionLabel(item Item) string {
	switch strings.ToLower(strings.TrimSpace(itemKind(item))) {
	case "database", "collection":
		return "DATABASES"
	case "page":
		return "PAGES"
	case "block":
		return "BLOCKS"
	default:
		return strings.ToUpper(firstNonEmpty(itemKind(item), "items"))
	}
}

func (m model) currentMemberOffset() int {
	itemIndex := m.currentItemIndex()
	members := m.currentGroupMembers()
	for index, member := range members {
		if member == itemIndex {
			return index
		}
	}
	return 0
}

func (m model) currentContextRowOffset() int {
	itemIndex := m.currentItemIndex()
	if itemIndex < 0 {
		return 0
	}
	rows := m.currentContextRows()
	for index, row := range rows {
		if row.Selectable && row.ItemIndex == itemIndex {
			return index
		}
	}
	return 0
}

func (m *model) selectItemIndex(itemIndex int) {
	for index, filteredIndex := range m.filtered {
		if filteredIndex == itemIndex {
			m.selected = index
			return
		}
	}
}

func (m *model) selectItemByStableKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	for filteredIndex, itemIndex := range m.filtered {
		if itemIndex < 0 || itemIndex >= len(m.items) {
			continue
		}
		if itemStableKey(m.items[itemIndex]) == key {
			m.selected = filteredIndex
			m.contextOffset = 0
			m.detailView.GotoTop()
			return true
		}
	}
	return false
}

func (s sortMode) Label() string {
	switch s {
	case sortCount:
		return "count"
	case sortNewest:
		return "newest"
	case sortOldest:
		return "oldest"
	case sortTitle:
		return "title"
	case sortKind:
		return "kind"
	case sortScope:
		return "scope"
	case sortContainer:
		return "container"
	case sortAuthor:
		return "author"
	default:
		return "default"
	}
}

func markActiveSort(label string, active bool) string {
	if active {
		return label + " *"
	}
	return label
}

func compareItems(left, right Item, mode sortMode) (bool, bool) {
	switch mode {
	case sortNewest:
		return compareItemTime(left, right, true)
	case sortOldest:
		return compareItemTime(left, right, false)
	case sortTitle:
		return compareStrings(left.Title, right.Title)
	case sortKind:
		return compareStrings(itemKind(left), itemKind(right))
	case sortScope:
		return compareStrings(itemScope(left), itemScope(right))
	case sortContainer:
		return compareStrings(itemContainer(left), itemContainer(right))
	case sortAuthor:
		return compareStrings(itemAuthor(left), itemAuthor(right))
	default:
		return false, false
	}
}

func compareItemTime(left, right Item, newest bool) (bool, bool) {
	leftTime, leftOK := itemSortTime(left)
	rightTime, rightOK := itemSortTime(right)
	if leftOK != rightOK {
		return leftOK, true
	}
	if !leftOK {
		return false, false
	}
	if leftTime.Equal(rightTime) {
		return false, false
	}
	if newest {
		return leftTime.After(rightTime), true
	}
	return leftTime.Before(rightTime), true
}

func compareStrings(left, right string) (bool, bool) {
	left = strings.ToLower(strings.TrimSpace(left))
	right = strings.ToLower(strings.TrimSpace(right))
	if left == right {
		return false, false
	}
	if left == "" {
		return false, true
	}
	if right == "" {
		return true, true
	}
	return left < right, true
}

func itemStableKey(item Item) string {
	parts := []string{
		strings.TrimSpace(item.Source),
		strings.TrimSpace(item.Kind),
		strings.TrimSpace(item.ID),
	}
	if strings.Join(parts, "") != "" && strings.TrimSpace(item.ID) != "" {
		return strings.Join(parts, "\x00")
	}
	return strings.Join([]string{
		strings.TrimSpace(item.Source),
		strings.TrimSpace(item.Kind),
		strings.TrimSpace(item.Container),
		strings.TrimSpace(item.Author),
		strings.TrimSpace(item.Title),
		strings.TrimSpace(item.CreatedAt),
		strings.TrimSpace(item.UpdatedAt),
	}, "\x00")
}

func itemSignature(items []Item) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, itemStableKey(item)+"\x00"+strings.TrimSpace(item.Title)+"\x00"+strings.TrimSpace(item.UpdatedAt)+"\x00"+strings.TrimSpace(item.CreatedAt))
	}
	return strings.Join(parts, "\x1f")
}
