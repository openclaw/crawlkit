package tui

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

type model struct {
	title          string
	items          []Item
	refresh        func(context.Context) ([]Item, error)
	refreshEvery   time.Duration
	ctx            context.Context
	filtered       []int
	groups         []itemGroup
	selected       int
	offset         int
	width          int
	height         int
	query          string
	savedQuery     string
	jumpQuery      string
	filterMode     bool
	jumpMode       bool
	focus          paneFocus
	contextOffset  int
	detailView     viewport.Model
	wheelPending   bool
	wheelFocus     paneFocus
	wheelDelta     int
	wheelSeq       int
	sourceKind     string
	sourceLocation string
	layoutPreset   LayoutPreset
	sortMode       sortMode
	memberSortMode sortMode
	groupMode      groupMode
	compactDetail  bool
	showHelp       bool
	refreshing     bool
	status         string
	layoutMode     layoutMode
	menuOpen       bool
	menuTitle      string
	menuContext    paneFocus
	menuItems      []menuItem
	menuIndex      int
	menuOff        int
	menuFloating   bool
	menuRect       rect
	lastClickFocus paneFocus
	lastClickIndex int
	lastClickX     int
	lastClickY     int
	lastClickAt    time.Time
}

type sortMode int

const (
	sortDefault sortMode = iota
	sortCount
	sortNewest
	sortOldest
	sortTitle
	sortKind
	sortScope
	sortContainer
	sortAuthor
)

type layoutMode string

const (
	layoutModeColumns    layoutMode = "columns"
	layoutModeRightStack layoutMode = "right-stack"
)

type groupMode int

const (
	groupByDefault groupMode = iota
	groupByContainer
	groupByAuthor
	groupByThread
	groupByScope
)

func newModel(opts Options) model {
	layout := opts.Layout
	if layout == LayoutAuto {
		layout = inferLayoutFromItems(opts.Items)
	}
	m := model{
		title:          strings.TrimSpace(opts.Title),
		items:          append([]Item(nil), opts.Items...),
		refresh:        opts.Refresh,
		refreshEvery:   opts.RefreshEvery,
		ctx:            context.Background(),
		width:          100,
		height:         30,
		focus:          focusRows,
		sourceKind:     normalizeSourceKind(opts.SourceKind),
		sourceLocation: strings.TrimSpace(opts.SourceLocation),
		layoutPreset:   layout,
		sortMode:       sortNewest,
		memberSortMode: initialMemberSortMode(layout),
		compactDetail:  initialCompactDetail(layout),
		detailView:     viewport.New(1, 1),
	}
	if m.title == "" {
		m.title = "archive"
	}
	if m.refresh != nil && m.refreshEvery <= 0 {
		m.refreshEvery = refreshInterval
	}
	m.applyFilter()
	m.applyInitialGroupMode()
	m.selectGroup(0)
	return m
}

func initialCompactDetail(layout LayoutPreset) bool {
	switch layout {
	case LayoutChat, LayoutDocument:
		return true
	default:
		return false
	}
}

func initialMemberSortMode(layout LayoutPreset) sortMode {
	switch layout {
	case LayoutChat:
		return sortDefault
	case LayoutDocument:
		return sortKind
	default:
		return sortDefault
	}
}

func (m *model) applyInitialGroupMode() {
	if m.layoutPreset != LayoutChat || m.groupMode != groupByDefault || len(m.groups) > 1 {
		return
	}
	if distinctContainers(m.items, m.filtered) > 1 || distinctAuthors(m.items, m.filtered) <= 1 {
		return
	}
	m.groupMode = groupByAuthor
	m.applyFilter()
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.refreshTickCmd(), m.contextDoneCmd())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(typed.Width, 40)
		m.height = max(typed.Height, 1)
		m.ensureVisible()
	case wheelScrollMsg:
		if typed.seq != m.wheelSeq {
			return m, nil
		}
		m.applyQueuedWheelScroll()
		return m, nil
	case refreshTickMsg:
		return m, tea.Batch(m.startRefresh(false), m.refreshTickCmd())
	case refreshResultMsg:
		m.finishRefresh(typed)
		return m, nil
	case contextDoneMsg:
		return m, tea.Quit
	case tea.MouseMsg:
		if typed.Action == tea.MouseActionMotion && typed.Button == tea.MouseButtonNone {
			if m.menuOpen {
				return m, m.handleMenuMouse(typed)
			}
			return m, nil
		}
		if m.menuOpen {
			return m, m.handleMenuMouse(typed)
		}
		switch {
		case typed.Type == tea.MouseWheelUp || typed.Button == tea.MouseButtonWheelUp:
			return m, m.queueWheelScroll(m.paneAt(typed.X, typed.Y), -3)
		case typed.Type == tea.MouseWheelDown || typed.Button == tea.MouseButtonWheelDown:
			return m, m.queueWheelScroll(m.paneAt(typed.X, typed.Y), 3)
		case typed.Button == tea.MouseButtonLeft && typed.Action == tea.MouseActionPress:
			m.handleLeftClick(typed.X, typed.Y)
		case typed.Button == tea.MouseButtonRight && typed.Action == tea.MouseActionPress:
			m.handleRightClick(typed.X, typed.Y)
		}
	case tea.KeyMsg:
		m.cancelQueuedWheelScroll()
		if m.menuOpen {
			if cmd := m.updateMenuKey(typed); cmd != nil {
				return m, cmd
			}
			return m, nil
		}
		if m.filterMode {
			switch typed.String() {
			case "ctrl+c", "ctrl+d":
				return m, tea.Quit
			case "enter":
				m.filterMode = false
			case "esc":
				if m.query != m.savedQuery {
					m.query = m.savedQuery
					m.applyFilter()
				}
				m.filterMode = false
			case "backspace":
				if len(m.query) > 0 {
					_, size := utf8.DecodeLastRuneInString(m.query)
					m.query = m.query[:len(m.query)-size]
					m.applyFilter()
				}
			default:
				if len(typed.Runes) > 0 {
					m.query += string(typed.Runes)
					m.applyFilter()
				}
			}
			return m, nil
		}
		if m.jumpMode {
			switch typed.String() {
			case "ctrl+c", "ctrl+d", "q":
				return m, tea.Quit
			case "enter":
				m.finishJump()
			case "esc":
				m.jumpMode = false
				m.jumpQuery = ""
				m.status = "Jump canceled"
			case "backspace":
				if len(m.jumpQuery) > 0 {
					m.jumpQuery = m.jumpQuery[:len(m.jumpQuery)-1]
				}
			default:
				for _, r := range typed.Runes {
					if r >= '0' && r <= '9' {
						m.jumpQuery += string(r)
					}
				}
			}
			return m, nil
		}
		switch typed.String() {
		case "ctrl+c", "ctrl+d", "q":
			return m, tea.Quit
		case "tab", "right":
			m.focus = nextFocus(m.focus, 1)
		case "shift+tab", "left":
			m.focus = nextFocus(m.focus, -1)
		case "up", "k":
			if m.focus == focusRows {
				m.moveGroup(-1)
			} else if m.focus == focusContext {
				m.moveMember(-1)
			} else {
				m.scrollFocused(-1)
			}
		case "down", "j":
			if m.focus == focusRows {
				m.moveGroup(1)
			} else if m.focus == focusContext {
				m.moveMember(1)
			} else {
				m.scrollFocused(1)
			}
		case "pgup", "ctrl+b":
			if m.focus == focusRows {
				m.moveGroup(-m.pageSize())
			} else if m.focus == focusContext {
				m.moveMember(-m.focusedPageSize())
			} else {
				m.scrollFocused(-m.focusedPageSize())
			}
		case "pgdown", "ctrl+f":
			if m.focus == focusRows {
				m.moveGroup(m.pageSize())
			} else if m.focus == focusContext {
				m.moveMember(m.focusedPageSize())
			} else {
				m.scrollFocused(m.focusedPageSize())
			}
		case "home", "g":
			if m.focus == focusRows {
				m.selectGroup(0)
				m.ensureVisible()
			} else if m.focus == focusContext {
				m.selectMemberOffset(0)
			}
		case "end", "G":
			if m.focus == focusRows {
				m.selectGroup(len(m.groups) - 1)
				m.ensureVisible()
			} else if m.focus == focusContext {
				m.selectMemberOffset(len(m.currentGroupMembers()) - 1)
			}
		case "/", "f":
			m.startFilter()
		case "#":
			m.startJump()
		case "s":
			m.cycleSortMode()
		case "m":
			m.cycleMemberSortMode()
		case "S":
			m.openSortMenuFor(m.focus)
		case "a":
			m.openActionMenu()
		case "h", "?":
			m.toggleHelpPane()
		case "o":
			m.openSelectedURL()
		case "c":
			m.copySelectedURL()
		case "l":
			m.toggleLayout()
		case "d":
			m.toggleDetailMode()
		case "r":
			return m, m.startRefresh(true)
		case "v":
			m.cycleGroupMode()
		case "esc":
			if m.showHelp {
				m.showHelp = false
				m.status = "Ready"
			} else if m.query != "" {
				m.query = ""
				m.applyFilter()
			}
		case "enter", " ":
			if m.focus == focusRows {
				m.focus = focusContext
			} else if m.focus == focusContext {
				m.focus = focusDetail
			}
		}
	}
	return m, nil
}
