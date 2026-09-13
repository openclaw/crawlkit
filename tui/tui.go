package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
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
