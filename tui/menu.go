package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type menuAction int

const (
	actionClose menuAction = iota
	actionSeparator
	actionFocusRows
	actionFocusContext
	actionFocusDetail
	actionSortMenu
	actionHelpMenu
	actionClearFilter
	actionStartFilter
	actionStartJump
	actionToggleLayout
	actionToggleDetail
	actionCycleGroup
	actionOpenURL
	actionCopyURL
	actionCopyMarkdownLink
	actionCopyTitle
	actionCopyDetail
	actionOpenLinkMenu
	actionCopyLinkMenu
	actionOpenPickedLink
	actionCopyPickedLink
	actionOpenFirstLink
	actionCopyFirstLink
	actionCopyAllLinks
	actionBackToActions
	actionRefresh
	actionQuit
	actionSortDefault
	actionSortCount
	actionSortNewest
	actionSortOldest
	actionSortTitle
	actionSortKind
	actionSortScope
	actionSortContainer
	actionSortAuthor
)

type menuItem struct {
	label  string
	action menuAction
	value  string
}

func (item menuItem) selectable() bool {
	return item.action != actionSeparator
}

func menuSection(label string) menuItem {
	return menuItem{label: label, action: actionSeparator}
}

func (m *model) handleMenuMouse(msg tea.MouseMsg) tea.Cmd {
	switch {
	case msg.Type == tea.MouseWheelUp || msg.Button == tea.MouseButtonWheelUp:
		m.menuIndex = m.nextSelectableMenuIndex(-1)
		m.keepMenuVisible()
		return nil
	case msg.Type == tea.MouseWheelDown || msg.Button == tea.MouseButtonWheelDown:
		m.menuIndex = m.nextSelectableMenuIndex(1)
		m.keepMenuVisible()
		return nil
	case msg.Button == tea.MouseButtonRight && msg.Action == tea.MouseActionPress:
		m.closeMenu()
		return nil
	}
	index, ok := m.menuIndexAtMouse(msg.X, msg.Y)
	if msg.Action == tea.MouseActionMotion {
		if !ok || index < 0 || index >= len(m.menuItems) {
			return nil
		}
		m.menuIndex = m.nearestSelectableMenuIndex(index, 1)
		m.keepMenuVisible()
		return nil
	}
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return nil
	}
	if !ok {
		m.closeMenu()
		return nil
	}
	if index < 0 || index >= len(m.menuItems) {
		return nil
	}
	if !m.menuItems[index].selectable() {
		m.menuIndex = m.nearestSelectableMenuIndex(index, 1)
		m.keepMenuVisible()
		return nil
	}
	m.menuIndex = index
	m.keepMenuVisible()
	return m.runMenuItem(m.menuItems[m.menuIndex])
}

func (m model) menuIndexAtMouse(x, y int) (int, bool) {
	menuRect := m.layout().detail
	rowOffset := 4
	if m.menuFloating {
		menuRect = m.menuRect
		rowOffset = 3
	}
	if !menuRect.contains(x, y) {
		return 0, false
	}
	return m.menuOff + y - menuRect.y - rowOffset, true
}

func (m *model) updateMenuKey(key tea.KeyMsg) tea.Cmd {
	page := max(1, m.menuVisibleCount())
	if index, ok := visibleMenuShortcutIndex(key.String(), m.menuItems, m.menuOff, page); ok {
		m.menuIndex = index
		return m.runMenuItem(m.menuItems[m.menuIndex])
	}
	switch key.String() {
	case "ctrl+c":
		return tea.Quit
	case "ctrl+d":
		return tea.Quit
	case "q", "esc":
		m.closeMenu()
	case "up", "k":
		m.menuIndex = m.nextSelectableMenuIndex(-1)
		m.keepMenuVisible()
	case "down", "j":
		m.menuIndex = m.nextSelectableMenuIndex(1)
		m.keepMenuVisible()
	case "pgup", "ctrl+b":
		m.menuIndex = m.nearestSelectableMenuIndex(m.menuIndex-page, -1)
		m.keepMenuVisible()
	case "pgdown", "ctrl+f":
		m.menuIndex = m.nearestSelectableMenuIndex(m.menuIndex+page, 1)
		m.keepMenuVisible()
	case "home", "g":
		m.menuIndex = m.firstSelectableMenuIndex()
		m.keepMenuVisible()
	case "end", "G":
		m.menuIndex = m.lastSelectableMenuIndex()
		m.keepMenuVisible()
	case "enter", " ":
		if len(m.menuItems) > 0 {
			return m.runMenuItem(m.menuItems[m.menuIndex])
		}
	case "b":
		if m.menuTitle == "Open Link" || m.menuTitle == "Copy Link" {
			m.openActionMenuFor(m.menuContext)
		}
	case "s":
		m.openSortMenuFor(m.focus)
	case "?":
		m.openHelpMenu()
	case "/":
		m.startFilter()
	case "#":
		m.startJump()
	case "l":
		m.toggleLayout()
	case "d":
		m.toggleDetailMode()
	case "v":
		m.cycleGroupMode()
	}
	return nil
}

func (m *model) openActionMenu() {
	m.openActionMenuFor(focusNone)
}

func (m *model) openActionMenuFor(context paneFocus) {
	selectedItems := []menuItem{
		{label: "Copy selected detail", action: actionCopyDetail},
		{label: "Copy selected title", action: actionCopyTitle},
	}
	if item, ok := m.selectedItem(); ok && strings.TrimSpace(item.URL) != "" {
		selectedItems = append([]menuItem{
			{label: "Open selected URL", action: actionOpenURL},
			{label: "Copy selected URL", action: actionCopyURL},
			{label: "Copy markdown link", action: actionCopyMarkdownLink},
		}, selectedItems...)
	}
	items := []menuItem{
		menuSection("Selected"),
	}
	items = append(items, selectedItems...)
	if links := m.selectedReferenceLinks(); len(links) > 0 {
		items = append(items,
			menuSection("Links"),
			menuItem{label: "Open first body link", action: actionOpenFirstLink},
			menuItem{label: "Copy first body link", action: actionCopyFirstLink},
		)
	}
	if links := m.selectedReferenceLinks(); len(links) > 1 {
		items = append(items,
			menuItem{label: "Open body link...", action: actionOpenLinkMenu},
			menuItem{label: "Copy body link...", action: actionCopyLinkMenu},
			menuItem{label: "Copy all body links", action: actionCopyAllLinks},
		)
	}
	items = append(items, []menuItem{
		menuSection("Pane"),
		{label: "Focus rows pane", action: actionFocusRows},
		{label: "Focus context pane", action: actionFocusContext},
		{label: "Focus detail pane", action: actionFocusDetail},
		menuSection("View"),
		{label: "Sort focused pane", action: actionSortMenu},
		{label: "Filter rows...", action: actionStartFilter},
		{label: "Jump to row...", action: actionStartJump},
		{label: "Refresh rows", action: actionRefresh},
		{label: "Toggle wide layout", action: actionToggleLayout},
		{label: detailModeToggleLabel(m.compactDetail), action: actionToggleDetail},
		{label: groupModeToggleLabel(m.layoutPreset, m.groupMode), action: actionCycleGroup},
	}...)
	if m.query != "" {
		items = append(items, menuItem{label: "Clear filter", action: actionClearFilter})
	}
	items = append(items,
		menuItem{label: "Help", action: actionHelpMenu},
		menuItem{label: "Close menu", action: actionClose},
	)
	m.menuContext = context
	m.openMenu(actionMenuTitle(context), items)
}

func (m *model) openSortMenuFor(context paneFocus) {
	active := m.sortMode
	title := "Sort Groups"
	if context == focusContext {
		active = m.memberSortMode
		title = "Sort Members"
	}
	m.menuContext = context
	items := []menuItem{
		menuSection("Order"),
		{label: markActiveSort("Default", active == sortDefault), action: actionSortDefault},
		{label: markActiveSort("Newest", active == sortNewest), action: actionSortNewest},
		{label: markActiveSort("Oldest", active == sortOldest), action: actionSortOldest},
		{label: markActiveSort("Title", active == sortTitle), action: actionSortTitle},
		{label: markActiveSort("Kind", active == sortKind), action: actionSortKind},
		{label: markActiveSort("Scope", active == sortScope), action: actionSortScope},
		{label: markActiveSort("Container", active == sortContainer), action: actionSortContainer},
		{label: markActiveSort("Author", active == sortAuthor), action: actionSortAuthor},
	}
	if context != focusContext {
		items = append(items[:2], append([]menuItem{{label: markActiveSort("Count", active == sortCount), action: actionSortCount}}, items[2:]...)...)
	}
	m.openMenu(title, items)
}

func (m *model) openHelpMenu() {
	m.closeMenu()
	m.showHelp = true
	m.focus = focusDetail
	m.detailView.GotoTop()
	m.status = "Help"
}

func (m *model) toggleHelpPane() {
	m.closeMenu()
	m.showHelp = !m.showHelp
	if m.showHelp {
		m.focus = focusDetail
		m.detailView.GotoTop()
		m.status = "Help"
		return
	}
	m.status = "Ready"
}

func (m model) helpLines(width int) []string {
	lines := []string{
		bold("Crawlkit TUI"),
		"",
		"Mouse",
		"  left click: focus/select a pane row",
		"  left click menu row: run that action",
		"  wheel: scroll the pane under the pointer",
		"  wheel in menu: move the highlighted action",
		"  right click: open a stable action menu",
		"  menu actions: copy, links, filter, jump, sort, layout, group view, detail, quit",
		"",
		"Keyboard",
		"  ?: toggle this help",
		"  q: quit",
		"  Tab / Shift-Tab: cycle focus",
		"  arrows or j/k: move selection or scroll detail",
		"  PageUp/PageDown: page the active pane",
		"  Enter: drill into the next pane",
		"  a: open action menu",
		"  /: filter rows",
		"  #: jump to row",
		"  s: cycle group sort",
		"  m: cycle member sort",
		"  S: sort focused pane",
		"  r: refresh rows from the archive",
		"  v: cycle group view",
		"  d: toggle compact/full detail",
		"  l: toggle wide layout",
		"  o: open selected URL",
		"  c: copy selected URL",
		"  auto-refresh: archive changes are picked up every 15s when refresh is available",
		"  Enter in menu: run action or open link picker",
		"  b in submenu: back to actions",
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "  ") {
			out = append(out, line)
			continue
		}
		out = append(out, wrapPlain(line, width)...)
	}
	return out
}

func (m *model) openReferenceLinkMenu(mode string) {
	links := m.selectedReferenceLinks()
	if len(links) == 0 {
		m.status = "No body links found"
		return
	}
	title := "Copy Link"
	action := actionCopyPickedLink
	if mode == "open" {
		title = "Open Link"
		action = actionOpenPickedLink
	}
	items := make([]menuItem, 0, len(links)+1)
	for index, link := range links {
		items = append(items, menuItem{
			label:  formatLinkChoiceLabel(link, index),
			action: action,
			value:  link,
		})
	}
	items = append(items, menuItem{label: "Back to actions", action: actionBackToActions})
	m.openMenu(title, items)
	m.status = title
}

func (m *model) openMenu(title string, items []menuItem) {
	m.showHelp = false
	m.menuOpen = true
	m.menuTitle = title
	m.status = m.displayMenuTitle()
	m.menuItems = append([]menuItem(nil), items...)
	m.menuIndex = m.firstSelectableMenuIndex()
	m.menuOff = 0
	m.filterMode = false
	m.jumpMode = false
	m.jumpQuery = ""
	m.keepMenuVisible()
}

func (m *model) closeMenu() {
	m.menuOpen = false
	m.menuTitle = ""
	m.menuItems = nil
	m.menuIndex = 0
	m.menuOff = 0
	m.menuFloating = false
	m.menuRect = rect{}
}

func (m *model) runMenuItem(item menuItem) tea.Cmd {
	switch item.action {
	case actionClose:
		m.closeMenu()
	case actionFocusRows:
		m.focus = focusRows
		m.closeMenu()
	case actionFocusContext:
		m.focus = focusContext
		m.closeMenu()
	case actionFocusDetail:
		m.focus = focusDetail
		m.closeMenu()
	case actionSortMenu:
		m.openSortMenuFor(m.menuContext)
	case actionHelpMenu:
		m.openHelpMenu()
	case actionOpenLinkMenu:
		m.openReferenceLinkMenu("open")
	case actionCopyLinkMenu:
		m.openReferenceLinkMenu("copy")
	case actionOpenPickedLink:
		if strings.TrimSpace(item.value) == "" {
			m.status = "No body link found"
			m.closeMenu()
			return nil
		}
		if err := openURL(item.value); err != nil {
			m.status = err.Error()
		} else {
			m.status = "Opened " + item.value
		}
		m.closeMenu()
	case actionCopyPickedLink:
		if strings.TrimSpace(item.value) == "" {
			m.status = "No body link found"
			m.closeMenu()
			return nil
		}
		if err := copyText(item.value); err != nil {
			m.status = err.Error()
		} else {
			m.status = "Copied body link"
		}
		m.closeMenu()
	case actionBackToActions:
		m.openActionMenuFor(m.menuContext)
	case actionClearFilter:
		m.query = ""
		m.applyFilter()
		m.closeMenu()
	case actionStartFilter:
		m.startFilter()
	case actionStartJump:
		m.startJump()
	case actionToggleLayout:
		m.toggleLayout()
		m.closeMenu()
	case actionToggleDetail:
		m.toggleDetailMode()
		m.closeMenu()
	case actionCycleGroup:
		m.cycleGroupMode()
		m.closeMenu()
	case actionOpenURL:
		m.openSelectedURL()
		m.closeMenu()
	case actionCopyURL:
		m.copySelectedURL()
		m.closeMenu()
	case actionCopyMarkdownLink:
		m.copySelectedMarkdownLink()
		m.closeMenu()
	case actionCopyTitle:
		m.copySelectedTitle()
		m.closeMenu()
	case actionCopyDetail:
		m.copySelectedDetail()
		m.closeMenu()
	case actionOpenFirstLink:
		m.openFirstReferenceLink()
		m.closeMenu()
	case actionCopyFirstLink:
		m.copyFirstReferenceLink()
		m.closeMenu()
	case actionCopyAllLinks:
		m.copyAllReferenceLinks()
		m.closeMenu()
	case actionRefresh:
		return m.startRefresh(true)
	case actionSortDefault:
		m.setPaneSortMode(sortDefault)
	case actionSortCount:
		m.setPaneSortMode(sortCount)
	case actionSortNewest:
		m.setPaneSortMode(sortNewest)
	case actionSortOldest:
		m.setPaneSortMode(sortOldest)
	case actionSortTitle:
		m.setPaneSortMode(sortTitle)
	case actionSortKind:
		m.setPaneSortMode(sortKind)
	case actionSortScope:
		m.setPaneSortMode(sortScope)
	case actionSortContainer:
		m.setPaneSortMode(sortContainer)
	case actionSortAuthor:
		m.setPaneSortMode(sortAuthor)
	case actionQuit:
		return tea.Quit
	}
	return nil
}

func (m model) menuLines(width int) []string {
	if len(m.menuItems) == 0 {
		return []string{"No actions."}
	}
	palette := actionMenuColors(m.menuContext)
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(palette.accent)).Render(m.displayMenuTitle())
	lines := []string{title, dim(actionMenuSubtitle(m.menuContext)), ""}
	visible := m.menuVisibleCount()
	start := clampInt(m.menuOff, 0, max(0, len(m.menuItems)-visible))
	end := min(len(m.menuItems), start+visible)
	shortcut := 0
	for i := start; i < end; i++ {
		item := m.menuItems[i]
		if !item.selectable() {
			lines = append(lines, truncateCells("  "+dim(item.label), width))
			continue
		}
		shortcut++
		prefix := "  "
		if i == m.menuIndex {
			prefix = "> "
		}
		key := "   "
		if shortcut <= 9 {
			key = fmt.Sprintf("%d. ", shortcut)
		}
		line := truncateCells(prefix+key+item.label, width)
		if i == m.menuIndex {
			line = selectedMenuLineStyle(width, palette).Render(padCells(line, width))
		}
		lines = append(lines, line)
	}
	footer := "Enter/1-9 run  Esc close"
	if len(m.menuItems) > visible {
		footer = fmt.Sprintf("%s  Pg page  %d-%d/%d", footer, start+1, end, len(m.menuItems))
	}
	lines = append(lines, "", dim(footer))
	return lines
}

func (m model) displayMenuTitle() string {
	switch m.menuTitle {
	case "Row Actions":
		return m.groupPaneTitle() + " Actions"
	case "Context Actions":
		return m.memberPaneTitle() + " Actions"
	default:
		return firstNonEmpty(m.menuTitle, "Actions")
	}
}

type actionMenuPalette struct {
	accent     string
	background string
	foreground string
	selectedBG string
	selectedFG string
}

func actionMenuColors(context paneFocus) actionMenuPalette {
	switch context {
	case focusRows:
		return actionMenuPalette{
			accent:     rowsPaneAccent,
			background: "#111827",
			foreground: "#d7dee8",
			selectedBG: "#2f3f56",
			selectedFG: "#f8fafc",
		}
	case focusContext:
		return actionMenuPalette{
			accent:     contextPaneAccent,
			background: "#111a16",
			foreground: "#d7dee8",
			selectedBG: "#344337",
			selectedFG: "#f8fafc",
		}
	default:
		return actionMenuPalette{
			accent:     detailPaneAccent,
			background: "#151922",
			foreground: "#d7dee8",
			selectedBG: "#3f3a31",
			selectedFG: "#f8fafc",
		}
	}
}

func actionMenuSubtitle(context paneFocus) string {
	switch context {
	case focusRows:
		return "group scope"
	case focusContext:
		return "selected item scope"
	case focusDetail:
		return "detail scope"
	default:
		return "current selection"
	}
}

func actionMenuTitle(context paneFocus) string {
	switch context {
	case focusRows:
		return "Row Actions"
	case focusContext:
		return "Context Actions"
	case focusDetail:
		return "Detail Actions"
	default:
		return "Actions"
	}
}

func (m model) renderFloatingMenu(view string) string {
	if m.menuRect.w <= 0 || m.menuRect.h <= 0 {
		return view
	}
	lines := m.menuLines(max(1, m.menuRect.w-2))
	if len(lines) > max(0, m.menuRect.h-2) {
		lines = lines[:max(0, m.menuRect.h-2)]
	}
	box := floatingMenuStyle(m.menuRect.w, m.menuRect.h, actionMenuColors(m.menuContext)).Render(strings.Join(lines, "\n"))
	return overlayBlock(view, box, m.menuRect.x, m.menuRect.y, m.width)
}

func (m *model) placeFloatingMenu(x, y int) {
	if !m.menuOpen {
		return
	}
	maxWidth := max(24, m.width-2)
	width := clampInt(m.preferredMenuWidth(), 34, min(58, maxWidth))
	availableHeight := max(1, m.height-3)
	visibleRows := min(max(1, len(m.menuItems)), 12)
	height := min(visibleRows+7, availableHeight)
	if height < min(8, availableHeight) {
		height = min(8, availableHeight)
	}
	maxX := max(0, m.width-width)
	minY := 1
	maxY := max(minY, m.height-2-height)
	m.menuFloating = true
	m.menuRect = rect{
		x: clampInt(x+1, 0, maxX),
		y: clampInt(y, minY, maxY),
		w: width,
		h: height,
	}
	m.keepMenuVisible()
}

func (m model) preferredMenuWidth() int {
	width := lipgloss.Width(firstNonEmpty(m.menuTitle, "Actions")) + 4
	for _, item := range m.menuItems {
		width = max(width, lipgloss.Width(item.label)+8)
	}
	return width
}

func (m model) menuVisibleCount() int {
	if m.menuFloating && m.menuRect.h > 0 {
		return max(1, m.menuRect.h-7)
	}
	return max(1, m.layout().detail.h-7)
}

func visibleMenuShortcutIndex(key string, items []menuItem, menuOff, visible int) (int, bool) {
	if len(key) != 1 || key[0] < '1' || key[0] > '9' {
		return 0, false
	}
	target := int(key[0] - '0')
	shortcut := 0
	end := min(len(items), menuOff+max(1, visible))
	for index := menuOff; index < end; index++ {
		if !items[index].selectable() {
			continue
		}
		shortcut++
		if shortcut == target {
			return index, true
		}
	}
	return 0, false
}

func (m model) firstSelectableMenuIndex() int {
	for index, item := range m.menuItems {
		if item.selectable() {
			return index
		}
	}
	return 0
}

func (m model) lastSelectableMenuIndex() int {
	for index := len(m.menuItems) - 1; index >= 0; index-- {
		if m.menuItems[index].selectable() {
			return index
		}
	}
	return max(0, len(m.menuItems)-1)
}

func (m model) nextSelectableMenuIndex(delta int) int {
	if delta == 0 || len(m.menuItems) == 0 {
		return m.menuIndex
	}
	for index := m.menuIndex + delta; index >= 0 && index < len(m.menuItems); index += delta {
		if m.menuItems[index].selectable() {
			return index
		}
	}
	return m.menuIndex
}

func (m model) nearestSelectableMenuIndex(index, direction int) int {
	if len(m.menuItems) == 0 {
		return 0
	}
	index = clampInt(index, 0, len(m.menuItems)-1)
	if m.menuItems[index].selectable() {
		return index
	}
	if direction == 0 {
		direction = 1
	}
	for next := index + direction; next >= 0 && next < len(m.menuItems); next += direction {
		if m.menuItems[next].selectable() {
			return next
		}
	}
	return m.firstSelectableMenuIndex()
}

func (m *model) keepMenuVisible() {
	if len(m.menuItems) == 0 {
		m.menuOff = 0
		return
	}
	visible := m.menuVisibleCount()
	m.menuIndex = m.nearestSelectableMenuIndex(m.menuIndex, 1)
	if m.menuIndex < m.menuOff {
		m.menuOff = m.menuIndex
	}
	if m.menuIndex >= m.menuOff+visible {
		m.menuOff = m.menuIndex - visible + 1
	}
	m.menuOff = clampInt(m.menuOff, 0, max(0, len(m.menuItems)-visible))
}

func floatingMenuStyle(width, height int, palette actionMenuPalette) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(max(1, width-2)).
		Height(max(1, height-2)).
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(palette.accent)).
		Background(lipgloss.Color(palette.background)).
		Foreground(lipgloss.Color(palette.foreground))
}

func selectedMenuLineStyle(width int, palette actionMenuPalette) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(max(1, width)).
		Background(lipgloss.Color(palette.selectedBG)).
		Foreground(lipgloss.Color(palette.selectedFG)).
		Bold(true)
}
