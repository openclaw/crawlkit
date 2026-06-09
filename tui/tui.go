package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/mattn/go-isatty"
)

var ErrNotTerminal = errors.New("terminal UI requires an interactive terminal")

const terminalRestoreSequence = "\x1b[0m\x1b[?25h\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1049l"

var (
	markdownHeadingRE = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
	markdownLinkRE    = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	bareLinkRE        = regexp.MustCompile(`(^|[\s(<])(https?://[^\s<>)]+)`)
	markdownListRE    = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.+)$`)
	emojiCodeRE       = regexp.MustCompile(`(^|\s):[A-Za-z0-9_+\-]+:`)
)

var (
	openURL  = defaultOpenURL
	copyText = defaultCopyText
)

const (
	wheelScrollDelay      = 16 * time.Millisecond
	wheelMaxBufferedDelta = 6
	refreshInterval       = 15 * time.Second
	doubleClickWindow     = 450 * time.Millisecond
	rowsPaneAccent        = "#5bc0eb"
	contextPaneAccent     = "#9bc53d"
	detailPaneAccent      = "#fde74c"
	archiveBorderColor    = "#3f4654"
	archiveHeaderBG       = "#0d1321"
	archiveHeaderFG       = "#f7f7ff"
	archiveTextFG         = "#dfe7ef"
	archiveMutedFG        = "#8b95a7"
	archiveSubtleAccentFG = "#8fb8d8"
	archiveSelectedFG     = "#f2c94c"
	archiveSelectedBG     = "#1d1e18"
	archiveBlurSelectedFG = "#c3b66f"
	archiveBlurSelectedBG = "#171711"
	archiveRemoteFooterBG = "#f2c14e"
	archiveLocalFooterBG  = "#5bc0eb"
	archiveFooterFG       = "#05070d"
	archiveActiveRowFG    = "#f2c94c"
	archiveActiveRowBG    = "#14130f"
	archiveInactiveRowFG  = "#8793a3"
	archiveInactiveRowBG  = "#0f141b"
)

type wheelScrollMsg struct {
	seq int
}

type refreshTickMsg struct{}

type refreshResultMsg struct {
	items  []Item
	err    error
	manual bool
}

type contextDoneMsg struct{}

type Item struct {
	Title     string            `json:"title"`
	Subtitle  string            `json:"subtitle,omitempty"`
	Text      string            `json:"text,omitempty"`
	Detail    string            `json:"detail,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	Depth     int               `json:"depth,omitempty"`
	Source    string            `json:"source,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	ID        string            `json:"id,omitempty"`
	ParentID  string            `json:"parent_id,omitempty"`
	Scope     string            `json:"scope,omitempty"`
	Container string            `json:"container,omitempty"`
	Author    string            `json:"author,omitempty"`
	URL       string            `json:"url,omitempty"`
	CreatedAt string            `json:"created_at,omitempty"`
	UpdatedAt string            `json:"updated_at,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
}

type LayoutPreset string

const (
	LayoutAuto     LayoutPreset = ""
	LayoutList     LayoutPreset = "list"
	LayoutChat     LayoutPreset = "chat"
	LayoutDocument LayoutPreset = "document"
)

const (
	SourceLocal  = "local"
	SourceRemote = "remote"
)

type Row struct {
	Source    string            `json:"source,omitempty"`
	Kind      string            `json:"kind"`
	ID        string            `json:"id,omitempty"`
	ParentID  string            `json:"parent_id,omitempty"`
	Depth     int               `json:"depth,omitempty"`
	Scope     string            `json:"scope,omitempty"`
	Container string            `json:"container,omitempty"`
	Author    string            `json:"author,omitempty"`
	Title     string            `json:"title"`
	Text      string            `json:"text,omitempty"`
	Detail    string            `json:"detail,omitempty"`
	URL       string            `json:"url,omitempty"`
	CreatedAt string            `json:"created_at,omitempty"`
	UpdatedAt string            `json:"updated_at,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
}

type Options struct {
	Title          string
	EmptyMessage   string
	Items          []Item
	Refresh        func(context.Context) ([]Item, error)
	RefreshEvery   time.Duration
	Layout         LayoutPreset
	SourceKind     string
	SourceLocation string
	Stdin          io.Reader
	Stdout         io.Writer
}

type BrowseOptions struct {
	AppName        string
	Title          string
	EmptyMessage   string
	Rows           []Row
	Refresh        func(context.Context) ([]Row, error)
	RefreshEvery   time.Duration
	JSON           bool
	Layout         LayoutPreset
	SourceKind     string
	SourceLocation string
	Stdin          io.Reader
	Stdout         io.Writer
}

func ControlsHelp() string {
	return strings.TrimSpace(`Controls:
  Tab/arrow      focus panes
  click          select rows and headers
  right-click    open pane action menu
  a              open action menu
  s              cycle group sort
  m              cycle member sort
  S              sort focused pane
  /              filter rows
  #              jump to row
  v              cycle group view
  d              toggle detail mode
  l              toggle wide layout
  r              refresh rows from the archive
  o              open selected URL
  c              copy selected URL
  wheel or j/k   scroll focused pane
  auto-refresh   pick up archive changes every 15s when available
  ?              in-app help
  q              quit`)
}

func Browse(ctx context.Context, opts BrowseOptions) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.JSON {
		if opts.Rows == nil {
			opts.Rows = []Row{}
		}
		enc := json.NewEncoder(opts.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(opts.Rows)
	}
	layout := opts.Layout
	if layout == LayoutAuto {
		layout = inferLayout(opts.Rows)
	}
	rows := opts.Rows
	if len(rows) == 0 && opts.Refresh != nil {
		refreshed, err := opts.Refresh(ctx)
		if err != nil {
			return err
		}
		rows = refreshed
		if opts.Layout == LayoutAuto {
			layout = inferLayout(rows)
		}
	}
	items := rowItemsForLayout(rows, layout)
	var refreshItems func(context.Context) ([]Item, error)
	if opts.Refresh != nil {
		refreshItems = func(ctx context.Context) ([]Item, error) {
			rows, err := opts.Refresh(ctx)
			if err != nil {
				return nil, err
			}
			nextLayout := layout
			if opts.Layout == LayoutAuto {
				nextLayout = inferLayout(rows)
			}
			return rowItemsForLayout(rows, nextLayout), nil
		}
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = strings.TrimSpace(opts.AppName)
		if title != "" {
			title += " archive"
		}
	}
	if title == "" {
		title = "archive"
	}
	empty := strings.TrimSpace(opts.EmptyMessage)
	if empty == "" && strings.TrimSpace(opts.AppName) != "" {
		empty = opts.AppName + " has no local archive rows yet"
	}
	err := Run(ctx, Options{
		Title:          title,
		EmptyMessage:   empty,
		Items:          items,
		Refresh:        refreshItems,
		RefreshEvery:   opts.RefreshEvery,
		Layout:         layout,
		SourceKind:     opts.SourceKind,
		SourceLocation: opts.SourceLocation,
		Stdin:          opts.Stdin,
		Stdout:         opts.Stdout,
	})
	if err != nil && errors.Is(err, ErrNotTerminal) {
		app := strings.TrimSpace(opts.AppName)
		if app == "" {
			return fmt.Errorf("%w; run tui from a TTY or pass --json", err)
		}
		return fmt.Errorf("%w; run %s tui from a TTY or pass --json", err, app)
	}
	return err
}

func rowItemsForLayout(rows []Row, layout LayoutPreset) []Item {
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.ItemForLayout(layout))
	}
	return items
}

func (r Row) Item() Item {
	return r.ItemForLayout(LayoutAuto)
}

func (r Row) ItemForLayout(layout LayoutPreset) Item {
	if layout == LayoutAuto {
		layout = inferLayout([]Row{r})
	}
	rawTitle := firstNonEmpty(cleanText(r.Title), cleanText(r.Text), cleanText(r.ID), "(untitled)")
	title := compactTitle(rawTitle)
	detail := r.detailForLayout(layout)
	if strings.TrimSpace(detail) == "" && title != cleanText(rawTitle) {
		detail = cleanText(rawTitle)
	}
	tags := cleanStrings(r.Tags)
	if r.Kind != "" {
		tags = append([]string{cleanText(r.Kind)}, tags...)
	}
	if r.Source != "" {
		tags = append([]string{cleanText(r.Source)}, tags...)
	}
	depth := r.Depth
	if depth == 0 && layout == LayoutChat && strings.TrimSpace(r.ParentID) != "" {
		depth = 1
	}
	return Item{
		Title:     title,
		Subtitle:  r.subtitleForLayout(layout),
		Text:      cleanText(r.Text),
		Detail:    detail,
		Tags:      tags,
		Depth:     depth,
		Source:    cleanText(r.Source),
		Kind:      cleanText(r.Kind),
		ID:        cleanText(r.ID),
		ParentID:  cleanText(r.ParentID),
		Scope:     cleanText(r.Scope),
		Container: cleanText(r.Container),
		Author:    cleanText(r.Author),
		URL:       cleanText(r.URL),
		CreatedAt: cleanText(r.CreatedAt),
		UpdatedAt: cleanText(r.UpdatedAt),
		Fields:    copyStringMap(r.Fields),
	}
}

func Run(ctx context.Context, opts Options) error {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if len(opts.Items) == 0 {
		msg := opts.EmptyMessage
		if msg == "" {
			msg = "no rows"
		}
		_, err := fmt.Fprintln(opts.Stdout, msg)
		return err
	}
	input, ok := opts.Stdin.(*os.File)
	if !ok || !isatty.IsTerminal(input.Fd()) {
		return ErrNotTerminal
	}
	output, ok := opts.Stdout.(*os.File)
	if !ok || !isatty.IsTerminal(output.Fd()) {
		return ErrNotTerminal
	}
	defer restoreTerminalOutput(output)
	model := newModel(opts)
	if width, height, ok := terminalSize(input, output); ok {
		model.width = width
		model.height = height
		model.ensureVisible()
	} else {
		model.height = 12
	}
	runCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer stopSignals()
	model.ctx = runCtx
	program := tea.NewProgram(
		model,
		tea.WithContext(runCtx),
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(),
	)
	done := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			program.Kill()
		case <-done:
		}
	}()
	_, err := program.Run()
	close(done)
	if errors.Is(err, tea.ErrProgramKilled) && runCtx.Err() != nil {
		return nil
	}
	return err
}

func restoreTerminalOutput(output io.Writer) {
	if output == nil {
		return
	}
	_, _ = io.WriteString(output, terminalRestoreSequence)
}

func inferLayout(rows []Row) LayoutPreset {
	for _, row := range rows {
		switch strings.ToLower(strings.TrimSpace(row.Kind)) {
		case "message", "thread", "reply":
			return LayoutChat
		case "page", "database", "block", "collection":
			return LayoutDocument
		}
	}
	return LayoutList
}

func inferLayoutFromItems(items []Item) LayoutPreset {
	for _, item := range items {
		switch strings.ToLower(strings.TrimSpace(itemKind(item))) {
		case "message", "thread", "reply":
			return LayoutChat
		case "page", "database", "block", "collection":
			return LayoutDocument
		}
	}
	return LayoutList
}

func (r Row) subtitleForLayout(layout LayoutPreset) string {
	if layout == LayoutChat {
		parts := []string{cleanText(r.Container), cleanText(r.Author), cleanText(r.CreatedAt), cleanText(r.UpdatedAt)}
		return joinNonEmpty(parts, "  ")
	}
	if layout == LayoutDocument {
		parts := []string{cleanText(r.Kind), cleanText(r.Scope), cleanText(r.Container), cleanText(r.UpdatedAt), cleanText(r.CreatedAt)}
		return joinNonEmpty(parts, "  ")
	}
	parts := []string{cleanText(r.Scope), cleanText(r.Container), cleanText(r.Author), cleanText(r.CreatedAt), cleanText(r.UpdatedAt)}
	return joinNonEmpty(parts, "  ")
}

func joinNonEmpty(parts []string, sep string) string {
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, sep)
}

func (r Row) detailForLayout(layout LayoutPreset) string {
	if detail := cleanText(r.Detail); detail != "" {
		return detail
	}
	var lines []string
	if text := cleanText(r.Text); text != "" && text != cleanText(r.Title) {
		lines = append(lines, text)
	}
	return strings.Join(lines, "\n")
}

func terminalSize(input, output *os.File) (int, int, bool) {
	for _, file := range []*os.File{output, input} {
		if file == nil {
			continue
		}
		width, height, err := term.GetSize(file.Fd())
		if err == nil && width > 0 && height > 0 {
			return width, height, true
		}
	}
	return 0, 0, false
}

func fieldLine(key, value string) string {
	key = cleanText(key)
	value = cleanText(value)
	if key == "" || value == "" {
		return ""
	}
	return key + ": " + value
}

func parsePositiveInt(value string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("positive integer required")
	}
	return n, nil
}

func cleanStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = cleanText(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[cleanText(key)] = cleanText(value)
	}
	return out
}

func cleanText(value string) string {
	return strings.TrimSpace(stripTerminalControls(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func compactTitle(value string) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if value == "" {
		return ""
	}
	if looksMachineID(value) {
		return compactMachineID(value)
	}
	for _, sep := range []string{". ", "\n"} {
		if idx := strings.Index(value, sep); idx > 18 {
			value = strings.TrimSpace(value[:idx+1])
			break
		}
	}
	return truncateCells(value, 140)
}

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
		sortMode:       initialGroupSortMode(layout),
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

func initialGroupSortMode(layout LayoutPreset) sortMode {
	switch layout {
	case LayoutChat, LayoutDocument:
		return sortNewest
	default:
		return sortNewest
	}
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

type paneFocus int

const (
	focusNone paneFocus = -1
	focusRows paneFocus = iota
	focusContext
	focusDetail
)

type rect struct {
	x int
	y int
	w int
	h int
}

type tableColumn struct {
	Key   string
	Title string
	Width int
}

type tableRow []string

type contextRow struct {
	ItemIndex  int
	Label      string
	Selectable bool
}

type archiveLayout struct {
	rows    rect
	context rect
	detail  rect
	stacked bool
	mode    string
}

type itemGroup struct {
	Key     string
	Title   string
	Kind    string
	Scope   string
	Count   int
	Latest  string
	Members []int
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.refreshTickCmd(), m.contextDoneCmd())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = maxInt(typed.Width, 40)
		m.height = maxInt(typed.Height, 1)
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
			case "ctrl+c", "ctrl+d", "q":
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
					m.query = m.query[:len(m.query)-1]
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
	page := maxInt(1, m.menuVisibleCount())
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

func (m *model) openSelectedURL() {
	item, ok := m.selectedItem()
	if !ok || strings.TrimSpace(item.URL) == "" {
		m.status = "No URL for selected row"
		return
	}
	if err := openURL(strings.TrimSpace(item.URL)); err != nil {
		m.status = err.Error()
		return
	}
	m.status = "Opened selected URL"
}

func (m *model) copySelectedURL() {
	item, ok := m.selectedItem()
	if !ok || strings.TrimSpace(item.URL) == "" {
		m.status = "No URL for selected row"
		return
	}
	if err := copyText(strings.TrimSpace(item.URL)); err != nil {
		m.status = err.Error()
		return
	}
	m.status = "Copied selected URL"
}

func (m *model) copySelectedMarkdownLink() {
	item, ok := m.selectedItem()
	if !ok || strings.TrimSpace(item.URL) == "" {
		m.status = "No URL for selected row"
		return
	}
	url := strings.TrimSpace(item.URL)
	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = url
	}
	if err := copyText("[" + escapeMarkdownLinkLabel(title) + "](" + url + ")"); err != nil {
		m.status = err.Error()
		return
	}
	m.status = "Copied markdown link"
}

func escapeMarkdownLinkLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `[`, `\[`)
	value = strings.ReplaceAll(value, `]`, `\]`)
	return value
}

func (m *model) copySelectedTitle() {
	item, ok := m.selectedItem()
	if !ok || strings.TrimSpace(item.Title) == "" {
		m.status = "No title for selected row"
		return
	}
	if err := copyText(strings.TrimSpace(item.Title)); err != nil {
		m.status = err.Error()
		return
	}
	m.status = "Copied selected title"
}

func (m *model) copySelectedDetail() {
	item, ok := m.selectedItem()
	if !ok {
		m.status = "No selected row"
		return
	}
	text := strings.TrimSpace(stripTerminalControls(strings.Join(m.detailLinesForWidth(item, 100), "\n")))
	if text == "" {
		text = strings.TrimSpace(item.Title)
	}
	if err := copyText(text); err != nil {
		m.status = err.Error()
		return
	}
	m.status = "Copied selected detail"
}

func (m *model) openFirstReferenceLink() {
	link, ok := m.firstReferenceLink()
	if !ok {
		m.status = "No body link found"
		return
	}
	if err := openURL(link); err != nil {
		m.status = err.Error()
		return
	}
	m.status = "Opened first body link"
}

func (m *model) copyFirstReferenceLink() {
	link, ok := m.firstReferenceLink()
	if !ok {
		m.status = "No body link found"
		return
	}
	if err := copyText(link); err != nil {
		m.status = err.Error()
		return
	}
	m.status = "Copied first body link"
}

func (m *model) copyAllReferenceLinks() {
	links := m.selectedReferenceLinks()
	if len(links) == 0 {
		m.status = "No body links found"
		return
	}
	if err := copyText(strings.Join(links, "\n")); err != nil {
		m.status = err.Error()
		return
	}
	m.status = "Copied body links"
}

func (m model) firstReferenceLink() (string, bool) {
	links := m.selectedReferenceLinks()
	if len(links) == 0 {
		return "", false
	}
	return links[0], true
}

func (m model) selectedReferenceLinks() []string {
	item, ok := m.selectedItem()
	if !ok {
		return nil
	}
	return itemReferenceLinks(item)
}

func formatLinkChoiceLabel(url string, index int) string {
	return fmt.Sprintf("%2d  %s", index+1, url)
}

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

func (m model) menuLines(width int) []string {
	if len(m.menuItems) == 0 {
		return []string{"No actions."}
	}
	palette := actionMenuColors(m.menuContext)
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(palette.accent)).Render(m.displayMenuTitle())
	lines := []string{title, dim(actionMenuSubtitle(m.menuContext)), ""}
	visible := m.menuVisibleCount()
	start := clampInt(m.menuOff, 0, maxInt(0, len(m.menuItems)-visible))
	end := minInt(len(m.menuItems), start+visible)
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
	lines := m.menuLines(maxInt(1, m.menuRect.w-2))
	if len(lines) > maxInt(0, m.menuRect.h-2) {
		lines = lines[:maxInt(0, m.menuRect.h-2)]
	}
	box := floatingMenuStyle(m.menuRect.w, m.menuRect.h, actionMenuColors(m.menuContext)).Render(strings.Join(lines, "\n"))
	return overlayBlock(view, box, m.menuRect.x, m.menuRect.y, m.width)
}

func (m *model) placeFloatingMenu(x, y int) {
	if !m.menuOpen {
		return
	}
	maxWidth := maxInt(24, m.width-2)
	width := clampInt(m.preferredMenuWidth(), 34, minInt(58, maxWidth))
	availableHeight := maxInt(1, m.height-3)
	visibleRows := minInt(maxInt(1, len(m.menuItems)), 12)
	height := minInt(visibleRows+7, availableHeight)
	if height < minInt(8, availableHeight) {
		height = minInt(8, availableHeight)
	}
	maxX := maxInt(0, m.width-width)
	minY := 1
	maxY := maxInt(minY, m.height-2-height)
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
		width = maxInt(width, lipgloss.Width(item.label)+8)
	}
	return width
}

func (m model) menuVisibleCount() int {
	if m.menuFloating && m.menuRect.h > 0 {
		return maxInt(1, m.menuRect.h-7)
	}
	return maxInt(1, m.layout().detail.h-7)
}

func visibleMenuShortcutIndex(key string, items []menuItem, menuOff, visible int) (int, bool) {
	if len(key) != 1 || key[0] < '1' || key[0] > '9' {
		return 0, false
	}
	target := int(key[0] - '0')
	shortcut := 0
	end := minInt(len(items), menuOff+maxInt(1, visible))
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
	return maxInt(0, len(m.menuItems)-1)
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
	m.menuOff = clampInt(m.menuOff, 0, maxInt(0, len(m.menuItems)-visible))
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

func (m model) focusedPageSize() int {
	layout := m.layout()
	switch m.focus {
	case focusContext:
		return maxInt(1, rowsViewportHeight(layout.context.h))
	case focusDetail:
		if m.detailView.Height > 0 {
			return maxInt(1, m.detailView.Height)
		}
		return maxInt(1, layout.detail.h-2)
	default:
		return m.pageSize()
	}
}

func (m model) maxContextOffset() int {
	return maxInt(0, len(m.currentContextRows())-rowsViewportHeight(m.layout().context.h))
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

func (m *model) ensureVisible() {
	page := m.pageSize()
	groupIndex := m.currentGroupIndex()
	if groupIndex < m.offset {
		m.offset = groupIndex
	}
	if groupIndex >= m.offset+page {
		m.offset = groupIndex - page + 1
	}
	m.offset = clampInt(m.offset, 0, maxInt(len(m.groups)-1, 0))
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
	return maxInt(1, rowsViewportHeight(m.layout().rows.h))
}

func (m model) layout() archiveLayout {
	width := maxInt(m.width, 80)
	height := m.height
	if height <= 0 {
		height = 24
	}
	bodyH := maxInt(8, height-3)
	if width >= 140 {
		if m.layoutMode == layoutModeRightStack {
			rowsW := maxInt(56, width*44/100)
			rightW := width - rowsW
			contextH := clampInt(maxInt(8, bodyH*42/100), 1, maxInt(1, bodyH-1))
			return archiveLayout{
				rows:    rect{x: 0, y: 1, w: rowsW, h: bodyH},
				context: rect{x: rowsW, y: 1, w: rightW, h: contextH},
				detail:  rect{x: rowsW, y: 1 + contextH, w: rightW, h: bodyH - contextH},
				mode:    string(layoutModeRightStack),
			}
		}
		rowsW := maxInt(48, width*34/100)
		contextW := maxInt(40, width*30/100)
		detailW := maxInt(42, width-rowsW-contextW)
		return archiveLayout{
			rows:    rect{x: 0, y: 1, w: rowsW, h: bodyH},
			context: rect{x: rowsW, y: 1, w: contextW, h: bodyH},
			detail:  rect{x: rowsW + contextW, y: 1, w: detailW, h: bodyH},
			mode:    string(layoutModeColumns),
		}
	}
	if width >= 100 {
		topH := clampInt(maxInt(8, bodyH/2), 1, maxInt(1, bodyH-1))
		rowsW := width / 2
		return archiveLayout{
			rows:    rect{x: 0, y: 1, w: rowsW, h: topH},
			context: rect{x: rowsW, y: 1, w: width - rowsW, h: topH},
			detail:  rect{x: 0, y: 1 + topH, w: width, h: bodyH - topH},
			stacked: true,
			mode:    "split",
		}
	}
	rowsH := clampInt(maxInt(7, bodyH*36/100), 1, maxInt(1, bodyH-2))
	contextH := clampInt(maxInt(6, bodyH*28/100), 1, maxInt(1, bodyH-rowsH-1))
	detailH := maxInt(6, bodyH-rowsH-contextH)
	return archiveLayout{
		rows:    rect{x: 0, y: 1, w: width, h: rowsH},
		context: rect{x: 0, y: 1 + rowsH, w: width, h: contextH},
		detail:  rect{x: 0, y: 1 + rowsH + contextH, w: width, h: detailH},
		stacked: true,
		mode:    "stacked",
	}
}

func (l archiveLayout) footerY() int {
	return maxInt(l.rows.y+l.rows.h, maxInt(l.context.y+l.context.h, l.detail.y+l.detail.h))
}

func (m model) paneAt(x, y int) paneFocus {
	layout := m.layout()
	switch {
	case layout.rows.contains(x, y):
		return focusRows
	case layout.context.contains(x, y):
		return focusContext
	case layout.detail.contains(x, y):
		return focusDetail
	default:
		return m.focus
	}
}

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
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

func paneStyle(pane, focus paneFocus, width, height int, accent string) lipgloss.Style {
	borderColor := accent
	if pane == focus {
		borderColor = "#f7f7ff"
	}
	return lipgloss.NewStyle().
		Width(maxInt(1, width-2)).
		Height(maxInt(1, height-2)).
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(borderColor)).
		Foreground(lipgloss.Color(archiveTextFG)).
		Padding(0, 1)
}

func pane(title, subtitle string, lines []string, rect rect, paneFocus paneFocus, focus paneFocus, accent string) string {
	return paneScrolled(title, subtitle, lines, rect, paneFocus, focus, accent, 0)
}

func paneScrolled(title, subtitle string, lines []string, rect rect, paneFocus paneFocus, focus paneFocus, accent string, scrollOffset int) string {
	width := maxInt(rect.w, 12)
	height := maxInt(rect.h, 3)
	contentW := paneContentWidth(width)
	contentH := paneContentHeight(height)
	body := flattenedPaneLines(lines, contentW)
	if len(body) == 0 {
		body = append(body, "")
	}
	maxOffset := maxInt(0, len(body)-contentH)
	scrollOffset = clampInt(scrollOffset, 0, maxOffset)
	if maxOffset > 0 {
		visibleEnd := minInt(len(body), scrollOffset+contentH)
		scrollLabel := fmt.Sprintf("%d-%d/%d", scrollOffset+1, visibleEnd, len(body))
		if strings.TrimSpace(subtitle) == "" {
			subtitle = scrollLabel
		} else {
			subtitle += "  " + scrollLabel
		}
	}
	titleLine := title
	if strings.TrimSpace(subtitle) != "" {
		titleLine += "  " + subtitle
	}
	header := paneTitleForWidth(paneFocus, focus, titleLine, contentW)
	body = append([]string(nil), body[scrollOffset:minInt(len(body), scrollOffset+contentH)]...)
	for len(body) < contentH {
		body = append(body, "")
	}
	out := append([]string{header}, body[:contentH]...)
	return paneStyle(paneFocus, focus, width, height, accent).Render(strings.Join(out, "\n"))
}

func flattenedPaneLines(lines []string, width int) []string {
	var body []string
	for _, line := range lines {
		body = append(body, wrapLines(line, width)...)
	}
	return body
}

func paneContentWidth(width int) int {
	return maxInt(1, width-4)
}

func paneContentHeight(height int) int {
	return maxInt(1, height-3)
}

func rowsViewportHeight(height int) int {
	return maxInt(1, paneContentHeight(height)-1)
}

func tableViewportWidth(rect rect) int {
	return maxInt(1, rect.w-4)
}

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

func compactNonEmpty(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func distinctContainers(items []Item, indexes []int) int {
	seen := map[string]struct{}{}
	for _, index := range indexes {
		if index < 0 || index >= len(items) {
			continue
		}
		value := strings.TrimSpace(items[index].Container)
		if value == "" {
			continue
		}
		seen[strings.ToLower(value)] = struct{}{}
	}
	return len(seen)
}

func distinctAuthors(items []Item, indexes []int) int {
	seen := map[string]struct{}{}
	for _, index := range indexes {
		if index < 0 || index >= len(items) {
			continue
		}
		value := strings.TrimSpace(itemAuthor(items[index]))
		if value == "" {
			continue
		}
		seen[strings.ToLower(value)] = struct{}{}
	}
	return len(seen)
}

func (item Item) searchText() string {
	parts := []string{
		item.Title,
		item.Subtitle,
		item.Text,
		item.Detail,
		item.Source,
		item.Kind,
		item.ID,
		item.ParentID,
		item.Scope,
		item.Container,
		item.Author,
		item.URL,
		item.CreatedAt,
		item.UpdatedAt,
		strings.Join(item.Tags, " "),
	}
	if len(item.Fields) > 0 {
		keys := make([]string, 0, len(item.Fields))
		for key := range item.Fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			parts = append(parts, key, item.Fields[key])
		}
	}
	return strings.Join(parts, " ")
}

func (m model) detailLines(item Item) []string {
	return m.detailLinesForWidth(item, 1000)
}

func (m model) detailLinesForWidth(item Item, width int) []string {
	width = maxInt(20, width)
	switch m.layoutPreset {
	case LayoutChat:
		return m.chatDetailLines(item, width)
	case LayoutDocument:
		return documentDetailLinesForWidth(item, width, m.compactDetail)
	}
	return genericDetailLinesForWidth(item, width)
}

func genericDetailLinesForWidth(item Item, width int) []string {
	detail := strings.TrimSpace(item.Detail)
	var lines []string
	context := detailContextLines(item, true)
	if len(context) > 0 {
		lines = append(lines, bold("Context"))
		lines = append(lines, context...)
	}
	if detail == "" {
		detail = item.Subtitle
	}
	if detail != "" {
		lines = append(lines, "", dim(tuiRule(width)), bold("Content"))
		lines = append(lines, markdownLines(detail, width)...)
	}
	if len(lines) == 0 {
		lines = append(lines, "", "No detail for this row.")
	}
	return lines
}

func (m model) chatDetailLines(item Item, width int) []string {
	var lines []string
	if header := chatHeaderLine(item); header != "" {
		lines = append(lines, bold(header))
	}
	if meta := chatMetaLine(item); meta != "" {
		lines = append(lines, dim(meta))
	}
	if message := chatBodyText(item); message != "" {
		lines = append(lines, "", dim(tuiRule(width)), bold("Selected Message"))
		lines = appendLimitedDetailLines(lines, chatBubbleLines(item, message, true, width), detailBodyLimit(m.compactDetail))
	}
	if title, thread := m.threadSection(item, width); len(thread) > 0 {
		lines = append(lines, "", dim(tuiRule(width)), bold(title))
		lines = appendLimitedDetailLines(lines, thread, detailBodyLimit(m.compactDetail))
	} else if title, conversation := m.conversationSection(item, width); len(conversation) > 0 {
		lines = append(lines, "", dim(tuiRule(width)), bold(title))
		lines = appendLimitedDetailLines(lines, conversation, detailBodyLimit(m.compactDetail))
	}
	if !m.compactDetail {
		if properties := chatPropertyLines(item); len(properties) > 0 {
			lines = append(lines, "", dim(tuiRule(width)), bold("Properties"))
			lines = append(lines, properties...)
		}
		if ids := chatIDLines(item); len(ids) > 0 {
			lines = append(lines, "", dim(tuiRule(width)), bold("IDs"))
			lines = append(lines, ids...)
		}
	}
	if len(lines) == 0 {
		return []string{"No detail for this message."}
	}
	return lines
}

func documentDetailLines(item Item) []string {
	return documentDetailLinesForWidth(item, 1000, false)
}

func documentDetailLinesForWidth(item Item, width int, compact bool) []string {
	var lines []string
	title := firstNonEmpty(item.Title, item.ID, "Untitled")
	lines = append(lines, bold(title))
	if meta := documentMetaLine(item); meta != "" {
		lines = append(lines, dim(meta))
	}
	preview := documentPreview(item)
	if preview != "" {
		lines = append(lines, "", dim(tuiRule(width)), bold("Preview"))
		lines = appendLimitedDetailLines(lines, markdownLines(preview, width), detailBodyLimit(compact))
	}
	if location := documentLocationLines(item); len(location) > 0 {
		lines = append(lines, "", dim(tuiRule(width)), bold("Location"))
		lines = append(lines, location...)
	}
	if metadata := documentPropertyLines(item); !compact && len(metadata) > 0 {
		lines = append(lines, "", dim(tuiRule(width)), bold("Properties"))
		lines = append(lines, metadata...)
	}
	if len(lines) == 0 {
		return []string{"No detail for this document."}
	}
	return lines
}

func chatHeaderLine(item Item) string {
	parts := []string{
		firstNonEmpty(item.Container, item.Scope),
		itemAuthor(item),
		shortTimestamp(firstNonEmpty(item.CreatedAt, item.UpdatedAt)),
	}
	header := joinNonEmpty(parts, "  ")
	if header == "" {
		return firstNonEmpty(item.Title, item.ID)
	}
	return header
}

func chatMetaLine(item Item) string {
	parts := []string{
		itemKind(item),
		chatThreadLabel(item),
		chatReplyCountLabel(item),
		rowAge(item),
	}
	return joinNonEmpty(parts, "  ")
}

func chatReplyCountLabel(item Item) string {
	value := strings.TrimSpace(firstNonEmpty(fieldValue(item, "reply_count"), fieldValue(item, "replies")))
	if value == "" || value == "0" {
		return ""
	}
	return value + " replies"
}

func chatRelationForColumn(item Item, width int) string {
	label := "msg"
	if strings.TrimSpace(item.ParentID) != "" {
		label = "reply"
	} else if chatReplyCountLabel(item) != "" {
		label = "thread"
	} else if thread := strings.TrimSpace(fieldValue(item, "thread", "reply_to")); thread != "" && thread != strings.TrimSpace(item.ID) && thread != strings.TrimSpace(fieldValue(item, "ts")) {
		label = "thread"
	}
	if width <= 3 {
		switch label {
		case "reply":
			return "rep"
		case "thread":
			return "thr"
		default:
			return "msg"
		}
	}
	return label
}

func chatThreadLabel(item Item) string {
	parent := strings.TrimSpace(item.ParentID)
	thread := strings.TrimSpace(fieldValue(item, "thread", "reply_to"))
	ts := strings.TrimSpace(fieldValue(item, "ts"))
	id := strings.TrimSpace(item.ID)
	switch {
	case parent != "":
		return "reply"
	case thread != "" && thread != ts && thread != id:
		return "thread"
	default:
		return ""
	}
}

func documentMetaLine(item Item) string {
	parts := []string{
		itemKind(item),
		firstNonEmpty(item.Container, item.Scope),
		shortTimestamp(firstNonEmpty(item.UpdatedAt, item.CreatedAt)),
	}
	return joinNonEmpty(parts, "  ")
}

func documentPreview(item Item) string {
	if text := strings.TrimSpace(item.Text); text != "" {
		return text
	}
	detail := strings.TrimSpace(item.Detail)
	if detail == "" || looksLikeFieldDump(detail) {
		return ""
	}
	return detail
}

func documentLocationLines(item Item) []string {
	return compactNonEmpty([]string{
		labelLine("Parent", item.ParentID),
		labelLine("Database", item.Container),
		labelLine("Workspace", item.Scope),
		labelLine("URL", item.URL),
	})
}

func documentPropertyLines(item Item) []string {
	lines := compactNonEmpty([]string{
		fieldLine("kind", itemKind(item)),
		fieldLine("provider", item.Source),
		fieldLine("created", shortTimestamp(item.CreatedAt)),
		fieldLine("updated", shortTimestamp(item.UpdatedAt)),
		fieldLine("id", item.ID),
	})
	lines = append(lines, compactFieldLines(item.Fields, "source", "space_id", "collection_id", "parent_table")...)
	return lines
}

func looksLikeFieldDump(value string) bool {
	lines := compactNonEmpty(strings.Split(value, "\n"))
	if len(lines) == 0 {
		return false
	}
	fieldLines := 0
	for _, line := range lines {
		if strings.Contains(line, "=") || strings.HasPrefix(strings.TrimSpace(line), "url:") || strings.HasPrefix(strings.TrimSpace(line), "url=") {
			fieldLines++
		}
	}
	return fieldLines == len(lines)
}

func labelLine(label, value string) string {
	label = cleanText(label)
	value = cleanText(value)
	if label == "" || value == "" {
		return ""
	}
	return label + ": " + value
}

func detailBodyLimit(compact bool) int {
	if compact {
		return 18
	}
	return 240
}

func appendLimitedDetailLines(out, lines []string, limit int) []string {
	if limit <= 0 || len(lines) <= limit {
		return append(out, lines...)
	}
	omitted := len(lines) - limit
	out = append(out, lines[:limit]...)
	return append(out, dim(fmt.Sprintf("... %d more line(s). Press d for full detail.", omitted)))
}

func prefixedMarkdownLines(value, prefix string, width int) []string {
	prefix = strings.TrimRight(prefix, "\t")
	raw := markdownLines(value, maxInt(8, width-lipgloss.Width(prefix)))
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if line == "" {
			out = append(out, strings.TrimRight(prefix, " "))
			continue
		}
		out = append(out, prefix+line)
	}
	return out
}

func detailContextLines(item Item, includeTitle bool) []string {
	var lines []string
	fields := []string{
		fieldLine("container", item.Container),
		fieldLine("author", item.Author),
		fieldLine("kind", itemKind(item)),
		fieldLine("source", item.Source),
		fieldLine("scope", item.Scope),
		fieldLine("created", shortTimestamp(item.CreatedAt)),
		fieldLine("updated", shortTimestamp(item.UpdatedAt)),
		fieldLine("id", item.ID),
		fieldLine("parent", item.ParentID),
		fieldLine("url", item.URL),
	}
	if includeTitle {
		fields = append(fields, fieldLine("title", item.Title))
	}
	for _, line := range fields {
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(item.Tags) > 0 {
		lines = append(lines, "tags="+strings.Join(item.Tags, " "))
	}
	if len(item.Fields) > 0 {
		keys := make([]string, 0, len(item.Fields))
		for key := range item.Fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if line := fieldLine(key, item.Fields[key]); line != "" {
				lines = append(lines, line)
			}
		}
	}
	return lines
}

func (m model) threadSection(selected Item, width int) (string, []string) {
	key := threadKey(selected)
	if key == "" {
		return "", nil
	}
	indexes := m.chatThreadIndexes(selected)
	if len(indexes) <= 1 {
		return "", nil
	}
	var lines []string
	for _, itemIndex := range indexes {
		if itemIndex < 0 || itemIndex >= len(m.items) {
			continue
		}
		item := m.items[itemIndex]
		text := chatBodyText(item)
		lines = append(lines, chatBubbleLines(item, text, item.ID == selected.ID, width)...)
	}
	return rangeSectionTitle("Thread", 0, len(indexes), len(indexes)), lines
}

func (m model) conversationSection(selected Item, width int) (string, []string) {
	members := m.chatConversationIndexes(selected)
	if len(members) <= 1 {
		return "", nil
	}
	selectedIndex := -1
	for index, itemIndex := range members {
		if itemIndex >= 0 && itemIndex < len(m.items) && m.items[itemIndex].ID == selected.ID {
			selectedIndex = index
			break
		}
	}
	if selectedIndex < 0 {
		return "", nil
	}
	radius := 8
	start := maxInt(0, selectedIndex-radius)
	end := minInt(len(members), selectedIndex+radius+1)
	var lines []string
	for _, itemIndex := range members[start:end] {
		if itemIndex < 0 || itemIndex >= len(m.items) {
			continue
		}
		item := m.items[itemIndex]
		lines = append(lines, chatBubbleLines(item, chatBodyText(item), item.ID == selected.ID, width)...)
	}
	if len(lines) <= 1 {
		return "", nil
	}
	return rangeSectionTitle("Conversation", start, end, len(members)), lines
}

func rangeSectionTitle(label string, start, end, total int) string {
	if total <= 0 || end <= start {
		return label
	}
	return fmt.Sprintf("%s %d-%d/%d", label, start+1, end, total)
}

func (m model) chatThreadIndexes(selected Item) []int {
	key := threadKey(selected)
	if key == "" {
		return nil
	}
	indexes := make([]int, 0)
	for itemIndex, item := range m.items {
		if threadKey(item) == key {
			indexes = append(indexes, itemIndex)
		}
	}
	sortChatIndexesByTime(m.items, indexes)
	return indexes
}

func (m model) chatConversationIndexes(selected Item) []int {
	indexes := make([]int, 0)
	selectedContainer := strings.TrimSpace(selected.Container)
	selectedScope := strings.TrimSpace(selected.Scope)
	for itemIndex, item := range m.items {
		if selectedContainer != "" {
			if strings.TrimSpace(item.Container) != selectedContainer {
				continue
			}
		} else if selectedScope != "" && strings.TrimSpace(item.Scope) != selectedScope {
			continue
		}
		indexes = append(indexes, itemIndex)
	}
	sortChatIndexesByTime(m.items, indexes)
	return indexes
}

func sortChatIndexesByTime(items []Item, indexes []int) {
	sort.SliceStable(indexes, func(i, j int) bool {
		left := items[indexes[i]]
		right := items[indexes[j]]
		if less, ok := compareItemTime(left, right, false); ok {
			return less
		}
		return indexes[i] < indexes[j]
	})
}

func chatBubbleLines(item Item, text string, selected bool, width int) []string {
	var lines []string
	prefix := "  "
	bodyPrefix := "    "
	if selected {
		prefix = "> "
		bodyPrefix = ">   "
	}
	header := joinNonEmpty([]string{itemAuthor(item), shortTimestamp(firstNonEmpty(item.CreatedAt, item.UpdatedAt)), rowAge(item)}, "  ")
	if header != "" {
		lines = append(lines, prefix+header)
	}
	body := prefixedMarkdownLines(text, bodyPrefix, width)
	if len(body) == 0 {
		body = []string{bodyPrefix + "(empty)"}
	}
	lines = append(lines, body...)
	return lines
}

func chatBodyText(item Item) string {
	return strings.TrimSpace(firstNonEmpty(item.Detail, item.Text, item.Title))
}

func chatPropertyLines(item Item) []string {
	return compactNonEmpty([]string{
		fieldLine("channel", item.Container),
		fieldLine("scope", item.Scope),
		fieldLine("author", itemAuthor(item)),
		fieldLine("kind", itemKind(item)),
		fieldLine("source", item.Source),
		fieldLine("created", shortTimestamp(item.CreatedAt)),
		fieldLine("updated", shortTimestamp(item.UpdatedAt)),
		fieldLine("url", item.URL),
		fieldLine("attachments", fieldValue(item, "attachments")),
		fieldLine("pinned", fieldValue(item, "pinned")),
		fieldLine("subtype", fieldValue(item, "subtype")),
	})
}

func chatIDLines(item Item) []string {
	lines := compactNonEmpty([]string{
		fieldLine("id", item.ID),
		fieldLine("thread", threadKey(item)),
		fieldLine("parent", item.ParentID),
	})
	lines = append(lines, compactFieldLines(item.Fields, "guild_id", "channel_id", "author_id", "user_id", "ts", "reply_to")...)
	return lines
}

func compactFieldLines(fields map[string]string, keys ...string) []string {
	if len(fields) == 0 {
		return nil
	}
	lines := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		value := fieldValue(Item{Fields: fields}, key)
		if line := fieldLine(key, value); line != "" {
			lines = append(lines, line)
		}
		seen[strings.ToLower(strings.TrimSpace(key))] = struct{}{}
	}
	remaining := make([]string, 0, len(fields))
	for key := range fields {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if _, ok := seen[normalized]; ok {
			continue
		}
		remaining = append(remaining, key)
	}
	sort.SliceStable(remaining, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(remaining[i])) < strings.ToLower(strings.TrimSpace(remaining[j]))
	})
	for _, key := range remaining {
		if line := fieldLine(key, fields[key]); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func threadKey(item Item) string {
	for _, value := range []string{
		fieldValue(item, "thread"),
		fieldValue(item, "reply_to"),
		strings.TrimSpace(item.ParentID),
		fieldValue(item, "ts"),
		strings.TrimSpace(item.ID),
	} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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

func rowKind(item Item) string {
	if kind := itemKind(item); kind != "" {
		return kind
	}
	return "row"
}

func itemKind(item Item) string {
	if strings.TrimSpace(item.Kind) != "" {
		return strings.TrimSpace(item.Kind)
	}
	for _, tag := range item.Tags {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			return tag
		}
	}
	return ""
}

func rowWhen(item Item) string {
	for _, value := range []string{item.UpdatedAt, item.CreatedAt} {
		if short := shortTimestamp(value); short != "" {
			return short
		}
	}
	for _, part := range subtitleParts(item.Subtitle) {
		if short := shortTimestamp(part); short != "" {
			return short
		}
	}
	return ""
}

func rowTimeForColumn(item Item, width int) string {
	if width <= 11 {
		return compactDate(item)
	}
	return rowWhen(item)
}

func groupTimeForColumn(value string, width int) string {
	if width <= 11 {
		return compactDateFromTimestamp(value)
	}
	return shortTimestamp(value)
}

func rowAge(item Item) string {
	if t, ok := itemSortTime(item); ok {
		return compactAge(time.Since(t))
	}
	return ""
}

func compactDate(item Item) string {
	if t, ok := itemSortTime(item); ok {
		return t.UTC().Format("01-02")
	}
	return ""
}

func compactDateFromTimestamp(value string) string {
	t, ok := parseTimestamp(value)
	if !ok {
		return ""
	}
	return t.UTC().Format("01-02")
}

func ageFromTimestamp(value string) string {
	t, ok := parseTimestamp(value)
	if !ok {
		return ""
	}
	return compactAge(time.Since(t))
}

func compactAge(duration time.Duration) string {
	if duration < 0 {
		duration = -duration
	}
	switch {
	case duration < time.Minute:
		return "now"
	case duration < time.Hour:
		return fmt.Sprintf("%dm", int(duration/time.Minute))
	case duration < 48*time.Hour:
		return fmt.Sprintf("%dh", int(duration/time.Hour))
	case duration < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(duration/(24*time.Hour)))
	case duration < 730*24*time.Hour:
		return fmt.Sprintf("%dmo", int(duration/(30*24*time.Hour)))
	default:
		return fmt.Sprintf("%dy", int(duration/(365*24*time.Hour)))
	}
}

func rowWhere(item Item) string {
	for _, value := range []string{item.Container, item.Scope, item.Author} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	kind := strings.ToLower(itemKind(item))
	for _, part := range subtitleParts(item.Subtitle) {
		lower := strings.ToLower(part)
		if lower == kind || shortTimestamp(part) != "" || looksMachineID(part) {
			continue
		}
		return part
	}
	return ""
}

func itemScope(item Item) string {
	if strings.TrimSpace(item.Scope) != "" {
		return strings.TrimSpace(item.Scope)
	}
	return fieldValue(item, "scope")
}

func itemContainer(item Item) string {
	if strings.TrimSpace(item.Container) != "" {
		return strings.TrimSpace(item.Container)
	}
	return firstNonEmpty(fieldValue(item, "container"), rowWhere(item))
}

func itemAuthor(item Item) string {
	if strings.TrimSpace(item.Author) != "" {
		return strings.TrimSpace(item.Author)
	}
	return firstNonEmpty(fieldValue(item, "author"), fieldValue(item, "user"), fieldValue(item, "sender"))
}

func fieldValue(item Item, keys ...string) string {
	if len(item.Fields) == 0 {
		return ""
	}
	for _, key := range keys {
		for actual, value := range item.Fields {
			if strings.EqualFold(strings.TrimSpace(actual), strings.TrimSpace(key)) {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func itemSortTime(item Item) (time.Time, bool) {
	for _, value := range []string{item.UpdatedAt, item.CreatedAt} {
		if t, ok := parseTimestamp(value); ok {
			return t, true
		}
	}
	for _, part := range subtitleParts(item.Subtitle) {
		if t, ok := parseTimestamp(part); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

func subtitleParts(subtitle string) []string {
	raw := strings.Split(subtitle, "  ")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func shortTimestamp(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if t, ok := parseTimestamp(value); ok {
		return t.UTC().Format("2006-01-02 15:04")
	}
	if len(value) >= len("2006-01-02") && value[4] == '-' && value[7] == '-' {
		return truncateCells(strings.ReplaceAll(value, "T", " "), 16)
	}
	return ""
}

func parseTimestamp(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func looksMachineID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 12 || strings.ContainsAny(value, " \t\n") {
		return false
	}
	digits := 0
	for _, r := range value {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	return digits >= 4
}

func displayLabel(value string) string {
	value = strings.TrimSpace(value)
	if looksMachineID(value) {
		return compactMachineID(value)
	}
	return value
}

func compactMachineID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 14 {
		return value
	}
	prefix := value
	suffix := ""
	if len(value) > 8 {
		prefix = value[:8]
	}
	if len(value) > 4 {
		suffix = value[len(value)-4:]
	}
	if suffix == "" {
		return prefix
	}
	return prefix + "..." + suffix
}

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

func defaultOpenURL(url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return fmt.Errorf("no URL to open")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open URL: %w", err)
	}
	return nil
}

func defaultCopyText(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("nothing to copy")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "windows":
		cmd = exec.Command("clip")
	default:
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd = exec.Command("wl-copy")
		} else {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		}
	}
	cmd.Stdin = strings.NewReader(value)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("copy text: %w", err)
	}
	return nil
}

func tuiRule(width int) string {
	return strings.Repeat("-", minInt(72, maxInt(12, width)))
}

func markdownLines(value string, width int) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	width = maxInt(20, width)
	var lines []string
	inFence := false
	blankRun := 0
	for _, rawLine := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(stripTerminalControls(rawLine), " \t")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			lines = append(lines, dim("--- code ---"))
			blankRun = 0
			continue
		}
		if inFence {
			lines = append(lines, dim(truncateCells(line, width)))
			blankRun = 0
			continue
		}
		if trimmed == "" {
			blankRun++
			if blankRun <= 1 {
				lines = append(lines, "")
			}
			continue
		}
		blankRun = 0
		if match := markdownHeadingRE.FindStringSubmatch(trimmed); match != nil {
			lines = appendWrappedStyled(lines, "", renderInlineMarkdown(match[2]), width, bold)
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			quote := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			lines = appendWrappedStyled(lines, "> ", renderInlineMarkdown(quote), width, dim)
			continue
		}
		if match := markdownListRE.FindStringSubmatch(line); match != nil {
			indent := match[1]
			if lipgloss.Width(indent) > 4 {
				indent = strings.Repeat(" ", 4)
			}
			lines = appendWrappedStyled(lines, indent+"- ", renderInlineMarkdown(match[3]), width, nil)
			continue
		}
		lines = appendWrappedStyled(lines, "", renderInlineMarkdown(line), width, nil)
	}
	return trimTrailingBlankLines(lines)
}

func appendWrappedStyled(lines []string, prefix, value string, width int, styler func(string) string) []string {
	contentWidth := maxInt(8, width-lipgloss.Width(prefix))
	wrapped := wrapPlain(value, contentWidth)
	if len(wrapped) == 0 {
		return lines
	}
	continuation := strings.Repeat(" ", lipgloss.Width(prefix))
	for index, line := range wrapped {
		prefixForLine := prefix
		if index > 0 {
			prefixForLine = continuation
		}
		if styler != nil {
			line = styler(line)
		}
		lines = append(lines, prefixForLine+line)
	}
	return lines
}

func renderInlineMarkdown(value string) string {
	value = markdownLinkRE.ReplaceAllString(value, "$1 <$2>")
	value = emojiCodeRE.ReplaceAllString(value, "$1")
	replacer := strings.NewReplacer(
		"`", "",
		"**", "",
		"*", "",
		"__", "",
		"~~", "",
	)
	return strings.TrimSpace(replacer.Replace(value))
}

func itemReferenceLinks(item Item) []string {
	seen := map[string]struct{}{}
	var links []string
	add := func(url string) {
		url = normalizeReferenceLink(url)
		if url == "" {
			return
		}
		if _, ok := seen[url]; ok {
			return
		}
		seen[url] = struct{}{}
		links = append(links, url)
	}
	for _, value := range []string{item.Text, item.Detail, item.Subtitle, item.Title} {
		for _, match := range markdownLinkRE.FindAllStringSubmatch(value, -1) {
			if len(match) > 2 {
				add(match[2])
			}
		}
		for _, match := range bareLinkRE.FindAllStringSubmatch(value, -1) {
			if len(match) > 2 {
				add(match[2])
			}
		}
	}
	return links
}

func normalizeReferenceLink(url string) string {
	url = strings.TrimSpace(stripTerminalControls(url))
	url = strings.Trim(url, `"'`)
	url = strings.TrimRight(url, ".,;:!?)]}")
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return ""
	}
	return url
}

func stripTerminalControls(value string) string {
	return ansi.Strip(value)
}

func trimTrailingBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
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

func floatingMenuStyle(width, height int, palette actionMenuPalette) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(maxInt(1, width-2)).
		Height(maxInt(1, height-2)).
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(palette.accent)).
		Background(lipgloss.Color(palette.background)).
		Foreground(lipgloss.Color(palette.foreground))
}

func selectedMenuLineStyle(width int, palette actionMenuPalette) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(maxInt(1, width)).
		Background(lipgloss.Color(palette.selectedBG)).
		Foreground(lipgloss.Color(palette.selectedFG)).
		Bold(true)
}

func overlayBlock(base, block string, x, y, width int) string {
	baseLines := strings.Split(base, "\n")
	blockLines := strings.Split(block, "\n")
	for offset, line := range blockLines {
		row := y + offset
		if row < 0 || row >= len(baseLines) {
			continue
		}
		baseLine := baseLines[row]
		prefix := strings.Repeat(" ", maxInt(0, x))
		if x > 0 && baseLine != "" {
			prefix = padCells(ansi.Cut(baseLine, 0, x), x)
		}
		lineWidth := ansi.StringWidth(line)
		suffixStart := maxInt(0, x+lineWidth)
		suffix := ""
		if suffixStart < ansi.StringWidth(baseLine) {
			suffix = ansi.Cut(baseLine, suffixStart, width)
		}
		rendered := prefix + line + suffix
		if width > 0 {
			rendered = truncateCells(rendered, width)
		}
		baseLines[row] = rendered
	}
	return strings.Join(baseLines, "\n")
}

func truncateCells(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes))+3 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "..."
}

func wrap(value string, width int) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return ""
	}
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	var b strings.Builder
	for lipgloss.Width(value) > width {
		line := ansi.Cut(value, 0, width)
		cut := strings.LastIndex(line, " ")
		if cut > 0 {
			line = strings.TrimRight(line[:cut], " ")
		}
		if line == "" {
			line = ansi.Cut(value, 0, width)
		}
		b.WriteString(line)
		b.WriteByte('\n')
		value = strings.TrimSpace(strings.TrimPrefix(value, line))
	}
	b.WriteString(value)
	return b.String()
}

func wrapPlain(value string, width int) []string {
	width = maxInt(20, width)
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	var line string
	for _, word := range words {
		if lipgloss.Width(word) > width {
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
			lines = append(lines, truncateCells(word, width))
			continue
		}
		if lipgloss.Width(line)+1+lipgloss.Width(word) > width && line != "" {
			lines = append(lines, line)
			line = word
			continue
		}
		if line == "" {
			line = word
		} else {
			line += " " + word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func wrapLines(value string, width int) []string {
	width = maxInt(width, 1)
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(wrap(line, width), "\n")...)
	}
	return out
}

func padCells(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) > width {
		value = truncateCells(value, width)
	}
	for lipgloss.Width(value) < width {
		value += " "
	}
	return value
}

func fitBlock(value string, width, height int) string {
	width = maxInt(width, 1)
	height = maxInt(height, 1)
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i, line := range lines {
		lines[i] = padCells(truncateCells(line, width), width)
	}
	return strings.Join(lines, "\n")
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

func strconvQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
