package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func stripANSI(value string) string {
	return regexp.MustCompile(`\x1b\[[0-9;:]*[A-Za-z]`).ReplaceAllString(value, "")
}

func menuContainsLabel(items []menuItem, label string) bool {
	for _, item := range items {
		if item.label == label {
			return true
		}
	}
	return false
}

func testDetailLines(count int) string {
	lines := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}
	return strings.Join(lines, "\n")
}

func TestRestoreTerminalOutputDisablesInteractiveModes(t *testing.T) {
	var output bytes.Buffer

	restoreTerminalOutput(&output)

	got := output.String()
	for _, want := range []string{"\x1b[?25h", "\x1b[?1000l", "\x1b[?1002l", "\x1b[?1003l", "\x1b[?1006l", "\x1b[?1049l"} {
		if !strings.Contains(got, want) {
			t.Fatalf("restore sequence missing %q in %q", want, got)
		}
	}
}

func TestBrowseJSONUsesUniversalRows(t *testing.T) {
	var out bytes.Buffer
	rows := []Row{{
		Source:    "slack",
		Kind:      "message",
		ID:        "C1/123",
		Scope:     "T1",
		Container: "general",
		Author:    "vincent",
		Title:     "ship it",
		Text:      "ship crawlkit tui",
		CreatedAt: "2026-05-01T12:00:00Z",
		Fields:    map[string]string{"thread": "123"},
	}}
	if err := Browse(context.Background(), BrowseOptions{AppName: "slacrawl", Rows: rows, JSON: true, Stdout: &out}); err != nil {
		t.Fatalf("Browse json: %v", err)
	}
	var decoded []Row
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out.String())
	}
	if len(decoded) != 1 || decoded[0].Source != "slack" || decoded[0].Kind != "message" || decoded[0].Title != "ship it" {
		t.Fatalf("decoded rows = %#v", decoded)
	}
}

func TestBrowseJSONEncodesNilRowsAsEmptyArray(t *testing.T) {
	var out bytes.Buffer
	if err := Browse(context.Background(), BrowseOptions{AppName: "discrawl", JSON: true, Stdout: &out}); err != nil {
		t.Fatalf("Browse json: %v", err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("json = %q, want []", out.String())
	}
}

func TestControlsHelpDocumentsGitcrawlLikeActions(t *testing.T) {
	help := ControlsHelp()
	for _, want := range []string{"right-click", "a", "m", "s", "S", "/", "#", "v", "d", "l", "r", "auto-refresh", "o", "c", "q"} {
		if !strings.Contains(help, want) {
			t.Fatalf("controls help missing %q:\n%s", want, help)
		}
	}
}

func TestRowItemUsesSharedArchiveShape(t *testing.T) {
	item := Row{
		Source:    "discord",
		Kind:      "message",
		ID:        "m1",
		Scope:     "@me",
		Container: "dm",
		Author:    "sam",
		Title:     "panic locked database",
		Text:      "full message text",
		Fields:    map[string]string{"reply_to": "m0"},
	}.ItemForLayout(LayoutList)
	if item.Title != "panic locked database" {
		t.Fatalf("title = %q", item.Title)
	}
	if !strings.Contains(item.Subtitle, "@me") || !strings.Contains(item.Subtitle, "dm") || !strings.Contains(item.Subtitle, "sam") {
		t.Fatalf("subtitle = %q", item.Subtitle)
	}
	if !strings.Contains(item.Detail, "full message text") || strings.Contains(item.Detail, "reply_to=m0") {
		t.Fatalf("detail = %q", item.Detail)
	}
	if item.Fields["reply_to"] != "m0" {
		t.Fatalf("fields = %#v", item.Fields)
	}
	if len(item.Tags) < 2 || item.Tags[0] != "discord" || item.Tags[1] != "message" {
		t.Fatalf("tags = %#v", item.Tags)
	}
}

func TestRowItemKeepsExplicitDetailAndStripsTerminalControls(t *testing.T) {
	item := Row{
		Source: "slack",
		Kind:   "message",
		ID:     "\x1b[31mm1\x1b[0m",
		Title:  "\x1b]8;;https://evil.test\ahello\x1b]8;;\a",
		Text:   "\x1b[32mgreen body\x1b[0m",
		Detail: "clean readable body",
		Fields: map[string]string{"\x1b[31mraw\x1b[0m": "\x1b[32mvalue\x1b[0m"},
	}.ItemForLayout(LayoutChat)
	if item.ID != "m1" || item.Title != "hello" || item.Text != "green body" || item.Detail != "clean readable body" {
		t.Fatalf("item was not sanitized/readable: %#v", item)
	}
	if item.Fields["raw"] != "value" {
		t.Fatalf("fields were not sanitized: %#v", item.Fields)
	}
}

func TestChatLayoutIndentsReplyRows(t *testing.T) {
	item := Row{
		Source:    "discord",
		Kind:      "message",
		ID:        "m2",
		ParentID:  "m1",
		Container: "general",
		Author:    "sam",
		Title:     "reply",
	}.ItemForLayout(LayoutChat)
	if item.Depth != 1 {
		t.Fatalf("depth = %d", item.Depth)
	}
	if strings.Contains(item.Subtitle, "discord") {
		t.Fatalf("chat subtitle should prioritize chat context, got %q", item.Subtitle)
	}
}

func TestRowsPaneUsesCompactTitlesAndKeepsMetadataInContext(t *testing.T) {
	m := newModel(Options{
		Title: "discrawl archive",
		Items: []Item{Row{
			Source:    "discord",
			Kind:      "message",
			ID:        "m1",
			Container: "general",
			Author:    "vincent",
			Title:     strings.Repeat("long message ", 30),
			Text:      strings.Repeat("long message ", 30),
			CreatedAt: "2026-05-02T12:00:00Z",
		}.ItemForLayout(LayoutChat)},
	})
	m.width = 100
	m.height = 18
	view := m.View()
	if !strings.Contains(view, "Messages") || !strings.Contains(view, "general") {
		t.Fatalf("context pane should render grouped messages:\n%s", view)
	}
	if !strings.Contains(view, "Message") || !strings.Contains(view, "general") || !strings.Contains(view, "vincent") {
		t.Fatalf("detail pane should render chat-style message detail:\n%s", view)
	}
}

func TestMachineIDsStayOutOfPrimaryPaneLabels(t *testing.T) {
	rawID := "00b8cbcf-c520-4790-999a-9c2940263721"
	item := Row{
		Kind:     "page",
		ID:       rawID,
		ParentID: rawID,
		Title:    rawID,
	}.ItemForLayout(LayoutDocument)
	if item.Title != "00b8cbcf...3721" {
		t.Fatalf("title = %q", item.Title)
	}
	m := newModel(Options{Title: "notcrawl archive", Layout: LayoutDocument, Items: []Item{item}})
	if len(m.groups) != 1 || m.groups[0].Title != "00b8cbcf...3721" {
		t.Fatalf("groups = %#v", m.groups)
	}
	if item.ID != rawID || item.ParentID != rawID {
		t.Fatalf("raw ids should remain in detail fields: %#v", item)
	}
}

func TestRowsPaneUsesStableColumns(t *testing.T) {
	line := rowListLine(Item{
		Title:    ":books: Can you *check* again? Hoping this update worked.",
		Subtitle: "general  vincent  2026-05-02T12:00:00Z",
		Tags:     []string{"message", "discord"},
	}, 100)
	for _, want := range []string{"message", "2026-05-02", "general", "Can you check"} {
		if !strings.Contains(line, want) {
			t.Fatalf("row line missing %q: %q", want, line)
		}
	}
	if strings.Contains(line, "*check*") || strings.Contains(line, ":books:") {
		t.Fatalf("row line should render markdown-ish titles: %q", line)
	}
	if strings.Contains(line, "vincent  2026") {
		t.Fatalf("row line should not dump raw subtitle: %q", line)
	}
}

func TestRowsPaneCompactsMultilineTitles(t *testing.T) {
	line := rowListLine(Item{
		Kind:      "message",
		Container: "general",
		Author:    "alice",
		CreatedAt: "2026-05-02T12:00:00Z",
		Title:     ":books: *New Course Started*\n\nCourse: *ChatGPT for Enterprise*\nStudent: Mirna",
	}, 96)
	if strings.Contains(line, "\n") {
		t.Fatalf("row line should be single-line: %q", line)
	}
	for _, want := range []string{"message", "2026-05-02", "alice", "New Course Started Course"} {
		if !strings.Contains(line, want) {
			t.Fatalf("row line missing %q: %q", want, line)
		}
	}
	if strings.Contains(line, "*") || strings.Contains(line, ":books:") {
		t.Fatalf("row line should render compact readable text: %q", line)
	}
}

func TestViewUsesGitcrawlStylePaneTables(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "one", Scope: "T1", Container: "general", Author: "Amy", Title: "first update", CreatedAt: "2026-05-02T09:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "two", Scope: "T1", Container: "random", Author: "Zed", Title: "second update", CreatedAt: "2026-05-02T10:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 300
	m.height = 28
	view := stripANSI(m.View())
	for _, want := range []string{"kind", "msgs", "latest", "channel", "time", "who", "title"} {
		if !strings.Contains(view, want) {
			t.Fatalf("table view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, " where ") || strings.Contains(view, " author ") {
		t.Fatalf("chat message pane should not waste columns on redundant where/author labels:\n%s", view)
	}
	if strings.Contains(view, "> general") || strings.Contains(view, "> first update") {
		t.Fatalf("pane tables should use row styling instead of prompt prefixes:\n%s", view)
	}
}

func TestWideRenderFillsTerminalAndKeepsThreePaneColumns(t *testing.T) {
	m := newModel(Options{
		Title:  "discrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "one", Scope: "guild", Container: "general", Author: "Amy", Title: "first update", CreatedAt: "2026-05-02T09:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "two", Scope: "guild", Container: "general", Author: "Zed", Title: "second update", CreatedAt: "2026-05-02T10:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "three", Scope: "guild", Container: "random", Author: "Cam", Title: "other update", CreatedAt: "2026-05-02T08:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 220
	m.height = 34
	view := stripANSI(m.View())
	lines := strings.Split(view, "\n")
	if len(lines) != 34 {
		t.Fatalf("rendered height = %d, want 34:\n%s", len(lines), view)
	}
	if len(lines[0]) != 220 || len(lines[len(lines)-1]) != 220 {
		t.Fatalf("view did not fill terminal width: first=%d last=%d\n%s", len(lines[0]), len(lines[len(lines)-1]), view)
	}
	for _, want := range []string{"Channels", "Messages", "3/3 rows", "Conversation", "kind", "msgs", "latest", "age", "channel", "time", "who", "title"} {
		if !strings.Contains(view, want) {
			t.Fatalf("wide render missing %q:\n%s", want, view)
		}
	}
}

func TestPaneTitlesStaySingleLineAtNarrowWidths(t *testing.T) {
	title := stripANSI(paneTitleForWidth(focusContext, focusRows, "Messages  1/8 rows  github-secure-session-4", 44))
	if len(title) > 44 {
		t.Fatalf("pane title width = %d, want <= 44: %q", len(title), title)
	}
	if !strings.Contains(title, "...") {
		t.Fatalf("pane title should truncate instead of wrapping: %q", title)
	}
}

func TestViewRespectsShortTerminalHeight(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", Container: "general", Author: "alice", Title: "one", CreatedAt: "2026-05-02T12:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", Container: "general", Author: "bob", Title: "two", CreatedAt: "2026-05-02T12:01:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 180
	m.height = 18

	view := stripANSI(m.View())
	lines := strings.Split(view, "\n")
	if len(lines) != 18 {
		t.Fatalf("view height = %d, want 18:\n%s", len(lines), view)
	}
	if !strings.Contains(lines[0], "slacrawl archive") {
		t.Fatalf("short terminal should keep header visible:\n%s", view)
	}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 180, Height: 18})
	m = updated.(model)
	view = stripANSI(m.View())
	lines = strings.Split(view, "\n")
	if len(lines) != 18 || !strings.Contains(lines[0], "slacrawl archive") {
		t.Fatalf("window resize should preserve short terminal height, lines=%d:\n%s", len(lines), view)
	}
}

func TestCompactWidthKeepsUsefulColumns(t *testing.T) {
	group := itemGroup{Kind: "channel", Count: 18, Latest: "2026-05-02T12:00:00Z", Title: "github-secure-session-4"}
	mediumGroupHeader := groupListHeader(46, sortDefault)
	mediumGroupLine := groupListLine(group, 46)
	groupModel := newModel(Options{Layout: LayoutChat, Items: []Item{
		Row{Kind: "message", Container: "github-secure-session-4", Title: "message", CreatedAt: "2026-05-02T12:00:00Z"}.ItemForLayout(LayoutChat),
	}})
	mediumRows := groupModel.groupTableRows(groupModel.groupColumns(46))
	for _, want := range []string{"N", "TIME", "AGE", "GROUP", "05-02", "github-secure"} {
		if !strings.Contains(mediumGroupHeader+mediumGroupLine, want) {
			t.Fatalf("medium compact group columns missing %q:\n%s\n%s", want, mediumGroupHeader, mediumGroupLine)
		}
	}
	if len(mediumRows) == 0 || !strings.Contains(strings.Join(mediumRows[0], " "), "05-02") {
		t.Fatalf("medium group table row should use compact dates: %#v", mediumRows)
	}

	groupHeader := groupListHeader(56, sortDefault)
	groupLine := groupListLine(group, 56)
	for _, want := range []string{"TYPE", "N", "AGE", "GROUP", "18", "github-secure"} {
		if !strings.Contains(groupHeader+groupLine, want) {
			t.Fatalf("compact group columns missing %q:\n%s\n%s", want, groupHeader, groupLine)
		}
	}
	for _, want := range []string{"TIME", "05-02"} {
		if !strings.Contains(groupHeader+groupLine, want) {
			t.Fatalf("compact group time column missing %q:\n%s\n%s", want, groupHeader, groupLine)
		}
	}

	rowHeader := rowListHeader(42, sortDefault)
	rowLine := rowListLine(Item{
		Title:     "Im working on adding",
		Author:    "Vincent Koc",
		CreatedAt: "2026-05-02T12:00:00Z",
	}, 42)
	for _, want := range []string{"TIME", "AGE", "WHO", "TITLE", "05-02", "Vinc", "Im working"} {
		if !strings.Contains(rowHeader+rowLine, want) {
			t.Fatalf("compact row columns missing %q:\n%s\n%s", want, rowHeader, rowLine)
		}
	}

	tmuxGroupHeader := groupListHeader(38, sortDefault)
	tmuxGroupLine := groupListLine(group, 38)
	for _, want := range []string{"N", "TIME", "AGE", "GROUP", "05-02", "github-secure"} {
		if !strings.Contains(tmuxGroupHeader+tmuxGroupLine, want) {
			t.Fatalf("tmux-width group columns missing %q:\n%s\n%s", want, tmuxGroupHeader, tmuxGroupLine)
		}
	}
}

func TestGroupColumnsOmitEmptyScope(t *testing.T) {
	m := newModel(Options{Layout: LayoutChat, Items: []Item{
		Row{Kind: "message", Container: "general", Title: "message", CreatedAt: "2026-05-02T12:00:00Z"}.ItemForLayout(LayoutChat),
	}})
	columns := m.groupColumns(90)
	for _, column := range columns {
		if column.Key == "scope" {
			t.Fatalf("blank group scopes should not reserve a scope column: %#v", columns)
		}
	}
	view := stripANSI(m.View())
	if strings.Contains(view, "scope") {
		t.Fatalf("view should not show an empty scope column:\n%s", view)
	}
}

func TestChatGroupsDoNotMergeSameChannelAcrossScopes(t *testing.T) {
	m := newModel(Options{Layout: LayoutChat, Items: []Item{
		Row{Kind: "message", Scope: "workspace-a", Container: "general", Title: "one", CreatedAt: "2026-05-02T12:00:00Z"}.ItemForLayout(LayoutChat),
		Row{Kind: "message", Scope: "workspace-b", Container: "general", Title: "two", CreatedAt: "2026-05-02T13:00:00Z"}.ItemForLayout(LayoutChat),
	}})
	if len(m.groups) != 2 {
		t.Fatalf("same channel names in different scopes should stay separate: %#v", m.groups)
	}
	columns := m.groupColumns(90)
	foundScope := false
	for _, column := range columns {
		if column.Key == "scope" {
			foundScope = true
		}
	}
	if !foundScope {
		t.Fatalf("multi-scope chat groups should expose the scope column: %#v", columns)
	}
}

func TestVeryNarrowPanesStillShowCompactColumns(t *testing.T) {
	group := itemGroup{Kind: "channel", Count: 18, Latest: "2026-05-02T12:00:00Z", Title: "github-secure-session-4"}
	groupHeader := groupListHeader(28, sortDefault)
	groupLine := groupListLine(group, 28)
	for _, want := range []string{"N", "AGE", "GROUP", "18", "github-secure"} {
		if !strings.Contains(groupHeader+groupLine, want) {
			t.Fatalf("narrow group columns missing %q:\n%s\n%s", want, groupHeader, groupLine)
		}
	}

	rowHeader := rowListHeader(28, sortDefault)
	rowLine := rowListLine(Item{
		Title:     "Im working on adding",
		Author:    "Vincent Koc",
		CreatedAt: "2026-05-02T12:00:00Z",
	}, 28)
	for _, want := range []string{"TIME", "TITLE", "05-02", "Im working"} {
		if !strings.Contains(rowHeader+rowLine, want) {
			t.Fatalf("narrow row columns missing %q:\n%s\n%s", want, rowHeader, rowLine)
		}
	}
}

func TestQClosesMenuAndTypesInFilterMode(t *testing.T) {
	m := newModel(Options{Title: "archive", Items: []Item{{Title: "alpha"}}})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = updated.(model)
	if !m.menuOpen {
		t.Fatal("menu did not open")
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = updated.(model)
	if cmd != nil || m.menuOpen {
		t.Fatalf("q in menu should close only, menu=%v cmd=%v", m.menuOpen, cmd)
	}

	m = newModel(Options{Title: "archive", Items: []Item{{Title: "alpha"}}})
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(model)
	if !m.filterMode {
		t.Fatal("filter did not start")
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = updated.(model)
	if cmd != nil || !m.filterMode || m.query != "q" {
		t.Fatalf("q in filter should be typed, mode=%v query=%q cmd=%v", m.filterMode, m.query, cmd)
	}
}

func TestFilterModeAcceptsQueryAndControlCQuits(t *testing.T) {
	m := newModel(Options{Title: "archive", Items: []Item{{Title: "query"}}})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(model)
	for _, r := range "query" {
		var cmd tea.Cmd
		updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(model)
		if cmd != nil {
			t.Fatalf("typing %q returned a command", r)
		}
	}
	if !m.filterMode || m.query != "query" {
		t.Fatalf("typed filter = mode %v query %q, want mode true query %q", m.filterMode, m.query, "query")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c in filter should quit")
	}
}

func TestFilterBackspaceRemovesLastRune(t *testing.T) {
	m := newModel(Options{Title: "archive", Items: []Item{{Title: "日本語"}}})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(model)
	for _, r := range "日本語" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(model)
	if m.query != "日本" || !utf8.ValidString(m.query) {
		t.Fatalf("query after backspace = %q, valid UTF-8=%v", m.query, utf8.ValidString(m.query))
	}
}

func TestFilterEscRestoresPreviousQuery(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{
			{Title: "alpha"},
			{Title: "beta"},
			{Title: "alphabet"},
		},
	})
	m.query = "alpha"
	m.applyFilter()
	if len(m.filtered) != 2 {
		t.Fatalf("initial filtered rows = %d, want 2", len(m.filtered))
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	m = updated.(model)
	if m.query != "alphab" || len(m.filtered) != 1 {
		t.Fatalf("draft query/filter = %q/%d", m.query, len(m.filtered))
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.filterMode || m.query != "alpha" || len(m.filtered) != 2 {
		t.Fatalf("esc should restore query/filter, mode=%v query=%q filtered=%d", m.filterMode, m.query, len(m.filtered))
	}
}

func TestInitialTerminalSizeCanUseTallPane(t *testing.T) {
	m := newModel(Options{Title: "archive", Items: []Item{{Title: "alpha"}}})
	m.width = 84
	m.height = 60
	view := m.View()
	if got := strings.Count(view, "\n") + 1; got != 60 {
		t.Fatalf("view height = %d, want 60", got)
	}
}

func TestChatDetailUsesTranscriptShapeBeforeMetadata(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "general", Author: "alice", Title: "root", Text: "root message", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "m2", ParentID: "m1", Container: "general", Author: "bob", Title: "reply", Text: "reply message", URL: "https://example.com/thread", CreatedAt: "2026-05-01T10:01:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.compactDetail = false
	m.selectItemIndex(1)
	item, ok := m.selectedItem()
	if !ok {
		t.Fatal("missing selected item")
	}
	lines := m.detailLines(item)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"general  bob", "Selected Message", "Thread 1-2/2", "alice", "root message", "> bob", ">   reply message", "Properties", "url: https://example.com/thread", "IDs", "parent: m1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("chat detail missing %q:\n%s", want, joined)
		}
	}
	if strings.Index(joined, "Message") > strings.Index(joined, "Thread 1-2/2") {
		t.Fatalf("selected message should appear before thread context:\n%s", joined)
	}
	if strings.Index(joined, "Thread") > strings.Index(joined, "Properties") {
		t.Fatalf("chat detail should put readable content before properties:\n%s", joined)
	}
}

func TestChatDetailShowsReplyCountMetadata(t *testing.T) {
	item := Row{
		Kind:      "message",
		ID:        "m1",
		Container: "general",
		Author:    "alice",
		Text:      "root message",
		Fields:    map[string]string{"reply_count": "3"},
	}.ItemForLayout(LayoutChat)
	joined := stripANSI(strings.Join(newModel(Options{Layout: LayoutChat}).chatDetailLines(item, 60), "\n"))
	if !strings.Contains(joined, "3 replies") {
		t.Fatalf("chat detail missing reply count:\n%s", joined)
	}
}

func TestChatDetailRendersMarkdownTranscriptLikeGitcrawl(t *testing.T) {
	m := newModel(Options{
		Title:  "discrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "general", Author: "alice", Title: "root", Text: "# Plan\n- ship *columns*\n- polish [preview](https://example.com)", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "m2", ParentID: "m1", Container: "general", Author: "bob", Title: "reply", Text: "> agreed\n`done`", CreatedAt: "2026-05-01T10:01:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.compactDetail = false
	m.selectItemIndex(1)
	item, ok := m.selectedItem()
	if !ok {
		t.Fatal("missing selected item")
	}
	joined := stripANSI(strings.Join(m.detailLinesForWidth(item, 52), "\n"))
	for _, want := range []string{"Plan", "- ship columns", "polish preview <https://example.com>", ">   > agreed", ">   done", "Properties", "IDs"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("markdown chat detail missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "# Plan") || strings.Contains(joined, "`done`") || strings.Contains(joined, "*columns*") {
		t.Fatalf("chat detail should render markdown-ish text, not raw markdown:\n%s", joined)
	}
}

func TestChatDetailFallsBackToConversationWindow(t *testing.T) {
	m := newModel(Options{
		Title:  "discrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "general", Author: "amy", Text: "before", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "m2", Container: "general", Author: "bob", Text: "selected", CreatedAt: "2026-05-01T10:01:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "m3", Container: "general", Author: "cam", Text: "after", CreatedAt: "2026-05-01T10:02:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.selectItemIndex(1)
	joined := stripANSI(strings.Join(m.detailLinesForWidth(m.items[1], 60), "\n"))
	for _, want := range []string{"Conversation 1-3/3", "amy", "before", "> bob", "selected", "cam", "after"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("conversation detail missing %q:\n%s", want, joined)
		}
	}
	if strings.Index(joined, "Message") > strings.Index(joined, "Conversation 1-3/3") {
		t.Fatalf("selected message should appear before conversation context:\n%s", joined)
	}
}

func TestChatDetailDoesNotTreatMetadataAsMessageBody(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "general", Author: "alice", Title: "root", Fields: map[string]string{"thread": "m1", "ts": "m1"}}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "m2", ParentID: "m1", Container: "general", Author: "bob", Title: "reply", Text: "actual reply", Fields: map[string]string{"thread": "m1", "ts": "m2"}}.ItemForLayout(LayoutChat),
		},
	})
	m.selectItemIndex(1)
	joined := stripANSI(strings.Join(m.detailLinesForWidth(m.items[1], 60), "\n"))
	if !strings.Contains(joined, "actual reply") {
		t.Fatalf("chat detail missing message body:\n%s", joined)
	}
	transcript := strings.Split(strings.Split(joined, "Properties")[0], "Thread")
	if len(transcript) != 2 {
		t.Fatalf("missing thread transcript:\n%s", joined)
	}
	if strings.Contains(transcript[1], "ts=m1") {
		t.Fatalf("metadata leaked into transcript before properties:\n%s", joined)
	}
}

func TestChatDetailPrefersExplicitReadableDetail(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "general", Author: "alice", Title: "raw", Text: "<@U1> raw body", Detail: "Alice readable body"}.ItemForLayout(LayoutChat),
		},
	})
	joined := stripANSI(strings.Join(m.detailLinesForWidth(m.items[0], 60), "\n"))
	if !strings.Contains(joined, "Alice readable body") || strings.Contains(joined, "<@U1> raw body") {
		t.Fatalf("chat detail should prefer readable explicit detail:\n%s", joined)
	}
}

func TestChatDetailKeepsRawIDsBelowReadableSummary(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{
				Kind:      "message",
				ID:        "C0AQ7TZR9KP/draft:1776788414.770369:C0AQ7TZR9KP-1776788221.127409",
				ParentID:  "1776788221.127409",
				Container: "github-secure-session-4",
				Author:    "Vincent Koc",
				Title:     "raw",
				Text:      "Im working on adding",
				CreatedAt: "2026-04-21T16:20:14Z",
				Fields: map[string]string{
					"thread": "1776788221.127409",
					"ts":     "draft:1776788414.770369",
				},
			}.ItemForLayout(LayoutChat),
		},
	})
	m.compactDetail = false
	joined := stripANSI(strings.Join(m.detailLinesForWidth(m.items[0], 64), "\n"))
	firstSection := strings.Split(joined, "Properties")[0]
	if strings.Contains(firstSection, "C0AQ7TZR9KP") || strings.Contains(firstSection, "1776788221") {
		t.Fatalf("chat summary leaked raw ids before readable content:\n%s", joined)
	}
	if !strings.Contains(firstSection, "message  reply") {
		t.Fatalf("chat summary should show readable message state:\n%s", joined)
	}
	if !strings.Contains(joined, "IDs") || !strings.Contains(joined, "C0AQ7TZR9KP") {
		t.Fatalf("raw ids should remain available in the IDs section:\n%s", joined)
	}
}

func TestDetailModeToggleStartsCompactForReadableArchiveFirstRun(t *testing.T) {
	m := newModel(Options{
		Title:  "discrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "general", Author: "alice", Title: "root", Text: "root message", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	if !m.compactDetail {
		t.Fatal("chat detail should default to compact so the first pane is readable")
	}
	compact := stripANSI(strings.Join(m.detailLinesForWidth(m.items[0], 60), "\n"))
	if !strings.Contains(compact, "root message") || strings.Contains(compact, "Properties") || strings.Contains(compact, "IDs") {
		t.Fatalf("compact detail should keep readable content and hide metadata sections:\n%s", compact)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(model)
	if m.compactDetail {
		t.Fatal("detail mode did not toggle to full")
	}
	full := stripANSI(strings.Join(m.detailLinesForWidth(m.items[0], 60), "\n"))
	if !strings.Contains(full, "root message") || !strings.Contains(full, "Properties") || !strings.Contains(full, "IDs") {
		t.Fatalf("full detail should include readable content and metadata sections:\n%s", full)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(model)
	if !m.compactDetail {
		t.Fatal("detail mode did not toggle back to compact")
	}
	m.width = 190
	if !strings.Contains(stripANSI(m.View()), "d detail") {
		t.Fatalf("footer should expose detail toggle:\n%s", stripANSI(m.View()))
	}
}

func TestDocumentDetailDefaultsCompactForPreviewFirstRun(t *testing.T) {
	m := newModel(Options{
		Title:  "notcrawl archive",
		Layout: LayoutDocument,
		Items: []Item{
			Row{Kind: "page", ID: "page1", ParentID: "Roadmap", Title: "Launch", Text: "Ship the page preview.", UpdatedAt: "2026-05-01T10:00:00Z", Fields: map[string]string{"parent_table": "collection"}}.ItemForLayout(LayoutDocument),
		},
	})
	if !m.compactDetail {
		t.Fatal("document detail should default to compact so page previews lead")
	}
	compact := stripANSI(strings.Join(m.detailLinesForWidth(m.items[0], 60), "\n"))
	if !strings.Contains(compact, "Ship the page preview.") || strings.Contains(compact, "Properties") {
		t.Fatalf("compact document detail should show preview and hide properties:\n%s", compact)
	}
}

func TestFooterControlsPrioritizeGitcrawlMuscleMemory(t *testing.T) {
	full := footerControls(190)
	for _, want := range []string{"right-click menu", "a actions", "header sort", "wheel scroll", "/ filter", "# jump", "r refresh", "d detail"} {
		if !strings.Contains(full, want) {
			t.Fatalf("wide footer missing %q:\n%s", want, full)
		}
	}
	compact := footerControls(122)
	for _, want := range []string{"right-click menu", "a actions", "wheel scroll", "/ filter", "# jump", "r refresh", "? help", "q quit"} {
		if !strings.Contains(compact, want) {
			t.Fatalf("compact footer missing %q:\n%s", want, compact)
		}
	}
	for _, noisy := range []string{"o open", "c copy", "v group", "l layout"} {
		if strings.Contains(compact, noisy) {
			t.Fatalf("compact footer should match gitcrawl priority before app extras %q:\n%s", noisy, compact)
		}
	}
}

func TestChatMembersDefaultToChronologicalSectionsLikeGitcrawl(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "new", Container: "general", Author: "alice", Title: "new", CreatedAt: "2026-05-01T10:02:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "old", Container: "general", Author: "alice", Title: "old", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	members := m.currentGroupMembers()
	if len(members) != 2 {
		t.Fatalf("members = %#v", members)
	}
	if got := m.items[members[0]].ID; got != "old" {
		t.Fatalf("first member = %q, want chronological message first", got)
	}
	contextRows := m.currentContextRows()
	if len(contextRows) != 3 || contextRows[0].Selectable || !strings.Contains(contextRows[0].Label, "2026-05-01") {
		t.Fatalf("chat context rows should start with a date section: %#v", contextRows)
	}
	m.setMemberSortMode(sortNewest)
	members = m.currentGroupMembers()
	if got := m.items[members[0]].ID; got != "new" {
		t.Fatalf("newest sort first member = %q, want newest message first", got)
	}
}

func TestChatMemberColumnsExposeThreadState(t *testing.T) {
	m := newModel(Options{
		Title:  "discrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "root", Container: "general", Author: "alice", Title: "root", CreatedAt: "2026-05-01T10:00:00Z", Fields: map[string]string{"reply_count": "2"}}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "reply", ParentID: "root", Container: "general", Author: "alice", Title: "reply", CreatedAt: "2026-05-01T10:01:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	columns := m.memberColumns(52)
	header := stripANSI(renderTableHeader(columns, 52, "#9bc53d"))
	if !strings.Contains(header, "rel") {
		t.Fatalf("chat member header should expose relation column:\n%s", header)
	}
	m.setMemberSortMode(sortTitle)
	rows := m.memberTableRows(columns, m.currentContextRows())
	rendered := stripANSI(strings.Join([]string{
		renderTableRow(columns, rows[0], 52, lipgloss.NewStyle()),
		renderTableRow(columns, rows[1], 52, lipgloss.NewStyle()),
	}, "\n"))
	if !strings.Contains(rendered, "thr") || !strings.Contains(rendered, "rep") {
		t.Fatalf("chat member rows should show thread/reply state:\n%s", rendered)
	}
}

func TestChatGroupScopeSortUsesScopeNotContainer(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "one", Scope: "z-workspace", Container: "general", Title: "one"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "two", Scope: "a-workspace", Container: "general", Title: "two"}.ItemForLayout(LayoutChat),
		},
	})
	m.setSortMode(sortScope)
	if len(m.groups) != 2 {
		t.Fatalf("groups = %#v", m.groups)
	}
	if got := m.groups[0].Scope; got != "a-workspace" {
		t.Fatalf("scope-sorted first group = %q, want a-workspace", got)
	}
}

func TestFocusedDetailPaneScrollsIndependently(t *testing.T) {
	m := newModel(Options{
		Title: "discrawl archive",
		Items: []Item{{
			Title:  "first",
			Detail: testDetailLines(40),
			Tags:   []string{"message", "discord"},
		}},
	})
	m.width = 80
	m.height = 30
	m.focus = focusDetail
	m.scrollFocused(1)
	if m.selected != 0 {
		t.Fatalf("detail scroll moved row selection to %d", m.selected)
	}
	if m.detailView.YOffset == 0 {
		t.Fatal("detail pane did not scroll")
	}
	view := m.View()
	if !strings.Contains(stripANSI(view), "line 02") {
		t.Fatalf("detail pane did not render scrolled content:\n%s", view)
	}
}

func TestMouseWheelBurstsAreBuffered(t *testing.T) {
	items := make([]Item, 0, 30)
	for i := 0; i < 30; i++ {
		items = append(items, Item{Title: fmt.Sprintf("row %02d", i), Tags: []string{"message"}})
	}
	m := newModel(Options{Title: "archive", Items: items})
	m.width = 100
	m.height = 14
	layout := m.layout()
	initialSelected := m.selected

	queued := 0
	for i := 0; i < 40; i++ {
		updated, cmd := m.Update(tea.MouseMsg{
			X:      layout.rows.x + 2,
			Y:      layout.rows.y + 2,
			Type:   tea.MouseWheelDown,
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
		})
		m = updated.(model)
		if cmd != nil {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("wheel burst queued %d frame ticks, want 1", queued)
	}
	if m.selected != initialSelected {
		t.Fatalf("wheel burst moved immediately to %d, want %d", m.selected, initialSelected)
	}
	if m.wheelDelta != wheelMaxBufferedDelta {
		t.Fatalf("wheel burst delta = %d, want capped %d", m.wheelDelta, wheelMaxBufferedDelta)
	}
	updated, _ := m.Update(wheelScrollMsg{seq: m.wheelSeq})
	m = updated.(model)
	wantSelected := clampInt(initialSelected+wheelMaxBufferedDelta, 0, len(m.filtered)-1)
	if m.selected != wantSelected {
		t.Fatalf("wheel burst selected = %d, want capped movement to %d", m.selected, wantSelected)
	}
}

func TestKeyInputCancelsQueuedWheel(t *testing.T) {
	items := make([]Item, 0, 10)
	for i := 0; i < 10; i++ {
		items = append(items, Item{Title: fmt.Sprintf("row %02d", i), Tags: []string{"message"}})
	}
	m := newModel(Options{Title: "archive", Items: items})
	updated, cmd := m.Update(tea.MouseMsg{Type: tea.MouseWheelDown})
	m = updated.(model)
	if cmd == nil || !m.wheelPending {
		t.Fatal("wheel should queue before key input")
	}
	seq := m.wheelSeq
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(model)
	if m.wheelPending || m.wheelDelta != 0 {
		t.Fatalf("key input did not cancel queued wheel: pending=%v delta=%d", m.wheelPending, m.wheelDelta)
	}
	updated, _ = m.Update(wheelScrollMsg{seq: seq})
	m = updated.(model)
	if m.selected != 1 {
		t.Fatalf("stale wheel changed selection after key input, selected=%d", m.selected)
	}
}

func TestMouseWheelTargetsPaneUnderPointer(t *testing.T) {
	m := newModel(Options{
		Title: "discrawl archive",
		Items: []Item{{
			Title:  "first",
			Detail: testDetailLines(40),
			Tags:   []string{"message", "discord"},
		}},
	})
	m.width = 100
	m.height = 24
	layout := m.layout()
	updated, cmd := m.Update(tea.MouseMsg{
		X:      layout.detail.x + 2,
		Y:      layout.detail.y + 2,
		Type:   tea.MouseWheelDown,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelDown,
	})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("detail wheel should queue a buffered scroll")
	}
	if m.focus != focusDetail {
		t.Fatalf("wheel focus = %v, want detail", m.focus)
	}
	if m.selected != 0 {
		t.Fatalf("detail wheel moved row selection to %d", m.selected)
	}
	updated, _ = m.Update(wheelScrollMsg{seq: m.wheelSeq})
	m = updated.(model)
	if m.detailView.YOffset == 0 {
		t.Fatal("detail pane did not scroll after queued wheel")
	}
}

func TestLeftClickSelectsRowUnderPointer(t *testing.T) {
	items := []Item{
		Row{Kind: "page", Title: "alpha"}.ItemForLayout(LayoutDocument),
		Row{Kind: "page", Title: "bravo"}.ItemForLayout(LayoutDocument),
		Row{Kind: "page", Title: "charlie"}.ItemForLayout(LayoutDocument),
	}
	m := newModel(Options{Title: "archive", Items: items})
	m.width = 100
	m.height = 24
	layout := m.layout()
	updated, _ := m.Update(tea.MouseMsg{
		X:      layout.rows.x + 2,
		Y:      layout.rows.y + 5,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	m = updated.(model)
	if m.focus != focusRows {
		t.Fatalf("focus = %v, want rows", m.focus)
	}
	if m.selected != 2 {
		t.Fatalf("selected = %d, want row under pointer", m.selected)
	}
	item, ok := m.selectedItem()
	if !ok || item.Title != "charlie" {
		t.Fatalf("selected item = %#v ok=%v", item, ok)
	}
}

func TestEnterDrillsThroughPanesLikeGitcrawl(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "general", Author: "Amy", Title: "first"}.ItemForLayout(LayoutChat),
		},
	})
	if m.focus != focusRows {
		t.Fatalf("initial focus = %v, want rows", m.focus)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if m.focus != focusContext {
		t.Fatalf("enter from rows focus = %v, want context", m.focus)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if m.focus != focusDetail {
		t.Fatalf("enter from context focus = %v, want detail", m.focus)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(model)
	if m.focus != focusDetail {
		t.Fatalf("space from detail focus = %v, want detail", m.focus)
	}
}

func TestRightClickOpensSharedActionMenu(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{
			Row{Kind: "message", Title: "alpha", Text: "see https://example.com/body-a", URL: "https://example.com/alpha"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", Title: "bravo", Text: "see [body](https://example.com/body-b)", URL: "https://example.com/bravo"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 100
	m.height = 16
	layout := m.layout()
	updated, _ := m.Update(tea.MouseMsg{
		X:      layout.rows.x + 2,
		Y:      layout.rows.y + 4,
		Button: tea.MouseButtonRight,
		Action: tea.MouseActionPress,
	})
	m = updated.(model)
	if !m.menuOpen || m.menuTitle != "Row Actions" {
		t.Fatalf("menu open=%v title=%q", m.menuOpen, m.menuTitle)
	}
	if m.selected != 1 {
		t.Fatalf("right click selected = %d, want row under pointer", m.selected)
	}
	view := m.View()
	if !strings.Contains(view, "Open selected URL") || !strings.Contains(view, "Copy selected detail") || !strings.Contains(view, "Links") {
		t.Fatalf("action menu missing expected commands:\n%s", view)
	}
	for _, want := range []string{"Copy markdown link", "Open first body link", "Copy first body link", "Focus detail pane", "Sort focused pane", "Jump to row..."} {
		if !menuContainsLabel(m.menuItems, want) {
			t.Fatalf("action menu items missing %q: %#v", want, m.menuItems)
		}
	}
}

func TestActionMenuUsesGitcrawlStyleLinkPicker(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{
			Row{Kind: "message", Title: "alpha", Text: "see https://example.com/a and https://example.com/b"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 100
	m.height = 16
	m.openActionMenuFor(focusRows)

	for _, want := range []string{"Open first body link", "Copy first body link", "Open body link...", "Copy body link...", "Copy all body links"} {
		if !menuContainsLabel(m.menuItems, want) {
			t.Fatalf("action menu missing %q: %#v", want, m.menuItems)
		}
	}
	m.openReferenceLinkMenu("open")
	if m.menuTitle != "Open Link" {
		t.Fatalf("menu title = %q, want Open Link", m.menuTitle)
	}
	if len(m.menuItems) < 3 {
		t.Fatalf("link menu items = %#v", m.menuItems)
	}
	if m.menuItems[0].value != "https://example.com/a" || m.menuItems[1].value != "https://example.com/b" {
		t.Fatalf("link menu values = %#v", m.menuItems)
	}
	if !menuContainsLabel(m.menuItems, "Back to actions") {
		t.Fatalf("link menu missing back action: %#v", m.menuItems)
	}
}

func TestJumpModeSelectsFocusedPaneRows(t *testing.T) {
	m := newModel(Options{
		Title:  "discrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "general", Title: "first"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "m2", Container: "general", Title: "second"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "m3", Container: "random", Title: "third"}.ItemForLayout(LayoutChat),
		},
	})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'#'}})
	m = updated.(model)
	if !m.jumpMode {
		t.Fatal("jump mode did not start")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	item, ok := m.selectedItem()
	if !ok || item.Title != "third" {
		t.Fatalf("group jump selected %#v ok=%v", item, ok)
	}

	m.focus = focusContext
	m.selectGroup(0)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'#'}})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	item, ok = m.selectedItem()
	if !ok || item.Title != "second" {
		t.Fatalf("message jump selected %#v ok=%v", item, ok)
	}
}

func TestKeyboardActionShortcutAliasOpensMenu(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{
			Row{Kind: "message", Title: "alpha", URL: "https://example.com/alpha"}.ItemForLayout(LayoutChat),
		},
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = updated.(model)

	if !m.menuOpen || m.menuTitle != "Actions" {
		t.Fatalf("action shortcut menu open=%v title=%q", m.menuOpen, m.menuTitle)
	}
	if m.menuFloating {
		t.Fatal("keyboard action menu should render in the detail pane")
	}
}

func TestActionMenuUsesGitcrawlDetailChrome(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{
			Row{Kind: "message", Title: "alpha", URL: "https://example.com/alpha"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 160
	m.height = 30
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = updated.(model)
	if m.status != "Actions" {
		t.Fatalf("action menu status = %q, want Actions", m.status)
	}
	view := stripANSI(m.View())
	for _, want := range []string{"Thread compact", "Actions", "current selection", "Open selected URL"} {
		if !strings.Contains(view, want) {
			t.Fatalf("action menu chrome missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Row Actions") || strings.Contains(view, "Channels Actions") {
		t.Fatalf("keyboard action menu should use gitcrawl-style generic title:\n%s", view)
	}
	if strings.Contains(view, "Detail Row Actions") {
		t.Fatalf("action menu should keep gitcrawl-style detail chrome:\n%s", view)
	}
}

func TestActionMenuTitlesFollowFocusedPane(t *testing.T) {
	m := newModel(Options{
		Title:  "archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", Container: "general", Author: "amy", Title: "alpha"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 160
	m.height = 30
	m.focus = focusContext
	m.openActionMenuFor(focusContext)
	if m.status != "Messages Actions" {
		t.Fatalf("context action menu status = %q, want Messages Actions", m.status)
	}
	view := stripANSI(m.View())
	for _, want := range []string{"Messages Actions", "selected item scope"} {
		if !strings.Contains(view, want) {
			t.Fatalf("context action menu missing %q:\n%s", want, view)
		}
	}
}

func TestDetailPaneTitleFollowsLayout(t *testing.T) {
	chat := newModel(Options{
		Title:  "chat archive",
		Layout: LayoutChat,
		Items:  []Item{Row{Kind: "message", Container: "general", Title: "hello"}.ItemForLayout(LayoutChat)},
	})
	chat.width = 160
	chat.height = 24
	if view := stripANSI(chat.View()); !strings.Contains(view, "Thread compact") || strings.Contains(view, "Detail Thread compact") {
		t.Fatalf("chat detail pane should be labeled as a thread:\n%s", view)
	}

	doc := newModel(Options{
		Title:  "doc archive",
		Layout: LayoutDocument,
		Items:  []Item{Row{Kind: "page", ParentID: "workspace", Title: "Roadmap"}.ItemForLayout(LayoutDocument)},
	})
	doc.width = 160
	doc.height = 24
	if view := stripANSI(doc.View()); !strings.Contains(view, "Page compact") || strings.Contains(view, "Detail Page compact") {
		t.Fatalf("document detail pane should be labeled as a page:\n%s", view)
	}
}

func TestMouseDoubleClickOpensSelectedRowURL(t *testing.T) {
	previousOpen := openURL
	var opened []string
	openURL = func(value string) error {
		opened = append(opened, value)
		return nil
	}
	t.Cleanup(func() {
		openURL = previousOpen
	})

	m := newModel(Options{
		Title: "archive",
		Items: []Item{
			Row{Kind: "message", Title: "alpha", URL: "https://example.com/alpha"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", Title: "bravo", URL: "https://example.com/bravo"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 100
	m.height = 16
	layout := m.layout()
	msg := tea.MouseMsg{
		X:      layout.rows.x + 2,
		Y:      layout.rows.y + 4,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}

	updated, _ := m.Update(msg)
	m = updated.(model)
	if len(opened) != 0 {
		t.Fatalf("single click opened URL: %#v", opened)
	}
	updated, _ = m.Update(msg)
	m = updated.(model)

	if len(opened) != 1 || opened[0] != "https://example.com/bravo" || m.status != "Opened selected URL" {
		t.Fatalf("double click opened=%#v status=%q", opened, m.status)
	}
}

func TestActionMenuCopyAndOpenSelectedRow(t *testing.T) {
	previousCopy := copyText
	previousOpen := openURL
	var copied []string
	var opened []string
	copyText = func(value string) error {
		copied = append(copied, value)
		return nil
	}
	openURL = func(value string) error {
		opened = append(opened, value)
		return nil
	}
	t.Cleanup(func() {
		copyText = previousCopy
		openURL = previousOpen
	})

	m := newModel(Options{
		Title:  "notcrawl archive",
		Layout: LayoutDocument,
		Items: []Item{
			Row{Kind: "page", Title: "Launch Plan", Text: "Ship the TUI.", URL: "https://example.com/launch"}.ItemForLayout(LayoutDocument),
		},
	})
	m.openSelectedURL()
	if len(opened) != 1 || opened[0] != "https://example.com/launch" || m.status != "Opened selected URL" {
		t.Fatalf("open action opened=%v status=%q", opened, m.status)
	}
	m.copySelectedURL()
	m.copySelectedMarkdownLink()
	m.copySelectedTitle()
	m.copySelectedDetail()
	if len(copied) != 4 {
		t.Fatalf("copied = %#v", copied)
	}
	if copied[0] != "https://example.com/launch" || copied[1] != "[Launch Plan](https://example.com/launch)" || copied[2] != "Launch Plan" || !strings.Contains(copied[3], "Ship the TUI.") {
		t.Fatalf("copied values = %#v", copied)
	}
}

func TestActionMenuUsesBodyReferenceLinks(t *testing.T) {
	previousCopy := copyText
	previousOpen := openURL
	var copied []string
	var opened []string
	copyText = func(value string) error {
		copied = append(copied, value)
		return nil
	}
	openURL = func(value string) error {
		opened = append(opened, value)
		return nil
	}
	t.Cleanup(func() {
		copyText = previousCopy
		openURL = previousOpen
	})

	m := newModel(Options{
		Title:  "notcrawl archive",
		Layout: LayoutDocument,
		Items: []Item{
			{Kind: "page", Title: "Launch Plan", Text: "Spec [one](https://example.com/one). Related https://example.com/two.", Detail: "Duplicate https://example.com/one"},
		},
	})

	if links := m.selectedReferenceLinks(); len(links) != 2 || links[0] != "https://example.com/one" || links[1] != "https://example.com/two" {
		t.Fatalf("reference links = %#v", links)
	}
	m.openFirstReferenceLink()
	m.copyFirstReferenceLink()
	m.copyAllReferenceLinks()

	if len(opened) != 1 || opened[0] != "https://example.com/one" {
		t.Fatalf("opened links = %#v", opened)
	}
	if len(copied) != 2 || copied[0] != "https://example.com/one" || copied[1] != "https://example.com/one\nhttps://example.com/two" {
		t.Fatalf("copied links = %#v", copied)
	}
}

func TestSortMenuSortsRowsByStructuredTitle(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{
			Row{Kind: "page", Title: "Zulu"}.ItemForLayout(LayoutDocument),
			Row{Kind: "page", Title: "Alpha"}.ItemForLayout(LayoutDocument),
		},
	})
	m.selectItemIndex(0)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	m = updated.(model)
	if !m.menuOpen || m.menuTitle != "Sort Groups" {
		t.Fatalf("sort menu open=%v title=%q", m.menuOpen, m.menuTitle)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	m = updated.(model)
	if m.sortMode != sortTitle {
		t.Fatalf("sort mode = %v, want title", m.sortMode)
	}
	item, ok := m.selectedItem()
	if !ok || item.Title != "Zulu" {
		t.Fatalf("selected item should stay stable after sorting, got %#v ok=%v", item, ok)
	}
	m.selected = 0
	item, ok = m.selectedItem()
	if !ok || item.Title != "Alpha" {
		t.Fatalf("first sorted item = %#v ok=%v", item, ok)
	}
	view := m.View()
	if !strings.Contains(view, "sort:title") {
		t.Fatalf("header missing sort status:\n%s", view)
	}
}

func TestContextSortMenuSortsMembersWithoutResortingGroups(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "z", Scope: "workspace", Container: "general", Author: "Zed", Title: "later", CreatedAt: "2026-05-02T10:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "a", Scope: "workspace", Container: "general", Author: "Amy", Title: "earlier", CreatedAt: "2026-05-02T09:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "x", Scope: "workspace", Container: "random", Author: "Max", Title: "other", CreatedAt: "2026-05-02T11:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 180
	m.height = 28
	m.focus = focusContext
	beforeGroups := make([]string, 0, len(m.groups))
	for _, group := range m.groups {
		beforeGroups = append(beforeGroups, group.Title)
	}
	for i, group := range m.groups {
		if group.Title == "general" {
			m.selectGroup(i)
			break
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	m = updated.(model)
	if !m.menuOpen || m.menuTitle != "Sort Members" {
		t.Fatalf("sort menu title = %q open=%v", m.menuTitle, m.menuOpen)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'8'}})
	m = updated.(model)
	if m.sortMode != sortNewest {
		t.Fatalf("group sort changed to %v", m.sortMode)
	}
	if m.memberSortMode != sortAuthor {
		t.Fatalf("member sort = %v, want author", m.memberSortMode)
	}
	afterGroups := make([]string, 0, len(m.groups))
	for _, group := range m.groups {
		afterGroups = append(afterGroups, group.Title)
	}
	if strings.Join(beforeGroups, "\n") != strings.Join(afterGroups, "\n") {
		t.Fatalf("member sort changed group order: before=%v after=%v", beforeGroups, afterGroups)
	}
	members := m.currentGroupMembers()
	if got := m.items[members[0]].Author; got != "Amy" {
		t.Fatalf("first member author = %q, want Amy", got)
	}
}

func TestNewestSortUsesStructuredRowMetadata(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{
			Row{Kind: "message", Title: "old", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", Title: "new", CreatedAt: "2026-05-02T10:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.setSortMode(sortNewest)
	m.selected = 0
	item, ok := m.selectedItem()
	if !ok || item.Title != "new" {
		t.Fatalf("newest item = %#v ok=%v", item, ok)
	}
}

func TestGitcrawlKeymapCyclesGroupAndMemberSort(t *testing.T) {
	m := newModel(Options{
		Title:  "discrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "a", Container: "general", Author: "Amy", Title: "first", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "b", Container: "general", Author: "Bob", Title: "second", CreatedAt: "2026-05-01T11:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(model)
	if m.menuOpen || m.sortMode != sortCount || !strings.Contains(m.status, "Sort: count") {
		t.Fatalf("s should cycle group sort, menu=%v sort=%v status=%q", m.menuOpen, m.sortMode, m.status)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = updated.(model)
	if m.menuOpen || m.memberSortMode != sortNewest || !strings.Contains(m.status, "Member sort: newest") {
		t.Fatalf("m should cycle member sort, menu=%v member=%v status=%q", m.menuOpen, m.memberSortMode, m.status)
	}
}

func TestChatAndDocumentGroupsDefaultToNewest(t *testing.T) {
	for _, layout := range []LayoutPreset{LayoutChat, LayoutDocument} {
		m := newModel(Options{
			Title:  "archive",
			Layout: layout,
			Items: []Item{
				Row{Kind: "message", Container: "old", ParentID: "old", Title: "old", UpdatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(layout),
				Row{Kind: "message", Container: "new", ParentID: "new", Title: "new", UpdatedAt: "2026-05-02T10:00:00Z"}.ItemForLayout(layout),
			},
		})
		if m.sortMode != sortNewest {
			t.Fatalf("%s group sort = %v, want newest", layout, m.sortMode)
		}
		group, ok := m.currentGroup()
		if !ok || group.Title != "new" {
			t.Fatalf("%s first group = %#v ok=%v, want newest group", layout, group, ok)
		}
	}
}

func TestRefreshKeyReloadsRowsLikeGitcrawl(t *testing.T) {
	m := newModel(Options{
		Title:      "discrawl archive",
		Layout:     LayoutChat,
		SourceKind: SourceRemote,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "general", Author: "Amy", Title: "old", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
		},
		Refresh: func(context.Context) ([]Item, error) {
			return []Item{
				Row{Kind: "message", ID: "m1", Container: "general", Author: "Amy", Title: "old", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
				Row{Kind: "message", ID: "m2", Container: "general", Author: "Bob", Title: "new", CreatedAt: "2026-05-01T11:00:00Z"}.ItemForLayout(LayoutChat),
			}, nil
		},
	})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(model)
	if cmd == nil || !m.refreshing || m.status != "Refreshing remote data" {
		t.Fatalf("refresh did not start, cmd=%v refreshing=%v status=%q", cmd, m.refreshing, m.status)
	}
	updated, _ = m.Update(cmd())
	m = updated.(model)
	if m.refreshing || len(m.items) != 2 || m.status != "Refreshed remote data: 2 row(s)" {
		t.Fatalf("refresh result not applied, refreshing=%v items=%d status=%q", m.refreshing, len(m.items), m.status)
	}
	item, ok := m.selectedItem()
	if !ok || item.ID != "m1" {
		t.Fatalf("refresh should preserve selected row by id, item=%#v ok=%v", item, ok)
	}
}

func TestRefreshCurrentStatusUsesGitcrawlSourceLanguage(t *testing.T) {
	m := newModel(Options{
		Title:      "notcrawl archive",
		Layout:     LayoutDocument,
		SourceKind: SourceLocal,
		Items: []Item{
			Row{Kind: "page", ID: "p1", Title: "Launch Plan", UpdatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutDocument),
		},
		Refresh: func(context.Context) ([]Item, error) {
			return []Item{
				Row{Kind: "page", ID: "p1", Title: "Launch Plan", UpdatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutDocument),
			}, nil
		},
	})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(model)
	if cmd == nil || m.status != "Refreshing local data" {
		t.Fatalf("refresh did not use local source language, cmd=%v status=%q", cmd, m.status)
	}
	updated, _ = m.Update(cmd())
	m = updated.(model)
	if m.status != "Local data already current" {
		t.Fatalf("current refresh status = %q", m.status)
	}
}

func TestStartRefreshDoesNotStack(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{{Title: "alpha"}},
		Refresh: func(context.Context) ([]Item, error) {
			return []Item{{Title: "alpha"}}, nil
		},
	})
	if cmd := m.startRefresh(false); cmd == nil || !m.refreshing {
		t.Fatalf("initial refresh did not start, cmd=%v refreshing=%v", cmd, m.refreshing)
	}
	if cmd := m.startRefresh(false); cmd != nil {
		t.Fatalf("automatic refresh stacked while refreshing: cmd=%v", cmd)
	}
	if cmd := m.startRefresh(true); cmd != nil || m.status != "Refresh already in progress" {
		t.Fatalf("manual refresh while refreshing = cmd %v status %q", cmd, m.status)
	}
}

func TestContextDoneQuitsModelForSignalCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := newModel(Options{Title: "archive", Items: []Item{{Title: "alpha"}}})
	m.ctx = ctx
	cmd := m.contextDoneCmd()
	if cmd == nil {
		t.Fatal("context done command missing")
	}
	cancel()
	updated, quit := m.Update(cmd())
	if _, ok := updated.(model); !ok || quit == nil {
		t.Fatalf("context cancellation should return quit command, updated=%T quit=%v", updated, quit)
	}
}

func TestHelpPaneRendersUniversalControls(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{{Title: "alpha", Tags: []string{"page"}}},
	})
	m.width = 160
	m.height = 40
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = updated.(model)
	if !m.showHelp || m.menuOpen || m.focus != focusDetail {
		t.Fatalf("help should render in detail pane, showHelp=%v menu=%v focus=%v", m.showHelp, m.menuOpen, m.focus)
	}
	view := stripANSI(m.View())
	for _, want := range []string{"Crawlkit TUI", "right click: open a stable action menu", "o: open selected URL", "c: copy selected URL", "r: refresh rows from the archive", "auto-refresh: archive changes are picked up every 15", "s: cycle group sort", "m: cycle member sort", "S: sort focused pane", "v: cycle group view", "#: jump to row", "left click: focus/select a pane row"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help pane missing %q:\n%s", want, view)
		}
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = updated.(model)
	if m.showHelp {
		t.Fatal("second ? should close detail help")
	}
}

func TestWideLayoutUsesThreeColumnsByDefault(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{{Title: "alpha", Tags: []string{"page"}}},
	})
	m.width = 160
	m.height = 30
	layout := m.layout()
	if layout.mode != string(layoutModeColumns) {
		t.Fatalf("layout mode = %q, want columns", layout.mode)
	}
	if layout.rows.y != layout.context.y || layout.context.y != layout.detail.y {
		t.Fatalf("wide panes should share the same row: %#v", layout)
	}
	if layout.rows.x != 0 || layout.context.x <= layout.rows.x || layout.detail.x <= layout.context.x {
		t.Fatalf("wide panes should progress left-to-right: %#v", layout)
	}
}

func TestChatExplorerGroupsChannelsAndListsMessages(t *testing.T) {
	m := newModel(Options{
		Title:  "discrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", Container: "general", Author: "alice", Title: "first", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", Container: "general", Author: "bob", Title: "second", CreatedAt: "2026-05-01T11:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", Container: "random", Author: "alice", Title: "third", CreatedAt: "2026-05-01T12:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 160
	m.height = 24
	view := m.View()
	if !strings.Contains(view, "Channels") || !strings.Contains(view, "Messages") || !strings.Contains(view, "1/1 rows") || !strings.Contains(view, "random") {
		t.Fatalf("chat explorer did not render grouped panes:\n%s", view)
	}
	if len(m.groups) != 2 {
		t.Fatalf("groups = %#v", m.groups)
	}
	for i, group := range m.groups {
		if group.Title == "general" {
			m.selectGroup(i)
			if group.Count != 2 {
				t.Fatalf("general group count = %d", group.Count)
			}
			break
		}
	}
	m.focus = focusContext
	item, ok := m.selectedItem()
	if !ok || item.Title != "first" {
		t.Fatalf("selected member = %#v ok=%v", item, ok)
	}
}

func TestChatExplorerCyclesGroupViews(t *testing.T) {
	m := newModel(Options{
		Title:  "discrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "general", Author: "alice", Title: "first", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "m2", Container: "general", Author: "bob", Title: "second", CreatedAt: "2026-05-01T11:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "m3", ParentID: "m1", Container: "random", Author: "alice", Title: "reply", CreatedAt: "2026-05-01T11:30:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	if groupModeLabel(m.layoutPreset, m.groupMode) != "channel" || len(m.groups) != 2 {
		t.Fatalf("default groups = %s %#v", groupModeLabel(m.layoutPreset, m.groupMode), m.groups)
	}
	m.cycleGroupMode()
	if groupModeLabel(m.layoutPreset, m.groupMode) != "person" || len(m.groups) != 2 || m.groupPaneTitle() != "People" {
		t.Fatalf("people groups = %s title=%s %#v", groupModeLabel(m.layoutPreset, m.groupMode), m.groupPaneTitle(), m.groups)
	}
	m.cycleGroupMode()
	if groupModeLabel(m.layoutPreset, m.groupMode) != "thread" || m.groupPaneTitle() != "Threads" {
		t.Fatalf("thread groups = %s title=%s %#v", groupModeLabel(m.layoutPreset, m.groupMode), m.groupPaneTitle(), m.groups)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "group:thread") || !strings.Contains(view, "Threads") {
		t.Fatalf("view should expose thread group mode:\n%s", view)
	}
}

func TestChatExplorerDefaultsToPeopleWhenChannelPaneWouldBeEmpty(t *testing.T) {
	m := newModel(Options{
		Title:  "discrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "general", Author: "alice", Title: "first", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "m2", Container: "general", Author: "bob", Title: "second", CreatedAt: "2026-05-01T11:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	if groupModeLabel(m.layoutPreset, m.groupMode) != "person" || len(m.groups) != 2 || m.groupPaneTitle() != "People" {
		t.Fatalf("single-channel chat should default to people groups, got mode=%s title=%s groups=%#v", groupModeLabel(m.layoutPreset, m.groupMode), m.groupPaneTitle(), m.groups)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "People") || !strings.Contains(view, "group:person") {
		t.Fatalf("view should expose people grouping for single-channel chat:\n%s", view)
	}
}

func TestDocumentExplorerGroupsParentsAndListsPages(t *testing.T) {
	m := newModel(Options{
		Title:  "notcrawl archive",
		Layout: LayoutDocument,
		Items: []Item{
			Row{Kind: "page", ParentID: "folder-a", Title: "Roadmap", UpdatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutDocument),
			Row{Kind: "database", ParentID: "folder-a", Title: "Leads", UpdatedAt: "2026-05-01T11:00:00Z"}.ItemForLayout(LayoutDocument),
			Row{Kind: "page", ParentID: "folder-b", Title: "Notes", UpdatedAt: "2026-05-01T12:00:00Z"}.ItemForLayout(LayoutDocument),
		},
	})
	m.width = 160
	m.height = 24
	view := m.View()
	if !strings.Contains(view, "Parents") || !strings.Contains(view, "Pages / Databases") || !strings.Contains(view, "folder-a") {
		t.Fatalf("document explorer did not render parent/member panes:\n%s", view)
	}
	if len(m.groups) != 2 || m.groups[0].Kind != "parent" {
		t.Fatalf("groups = %#v", m.groups)
	}
}

func TestDocumentGroupsDoNotMergeSameParentAcrossWorkspaces(t *testing.T) {
	m := newModel(Options{Layout: LayoutDocument, Items: []Item{
		Row{Kind: "page", Scope: "workspace-a", ParentID: "Client Dashboards", Title: "one", UpdatedAt: "2026-05-02T12:00:00Z"}.ItemForLayout(LayoutDocument),
		Row{Kind: "page", Scope: "workspace-b", ParentID: "Client Dashboards", Title: "two", UpdatedAt: "2026-05-02T13:00:00Z"}.ItemForLayout(LayoutDocument),
	}})
	if len(m.groups) != 2 {
		t.Fatalf("same parent names in different workspaces should stay separate: %#v", m.groups)
	}
}

func TestDocumentContextColumnsAvoidEmptyChatAuthorSlot(t *testing.T) {
	m := newModel(Options{
		Title:  "notcrawl archive",
		Layout: LayoutDocument,
		Items: []Item{
			Row{Kind: "page", ParentID: "Marketing", Container: "Comet.com", Title: "Gideon's SF Events", UpdatedAt: "2026-05-01T17:52:33Z"}.ItemForLayout(LayoutDocument),
			Row{Kind: "database", ParentID: "Marketing", Container: "Comet.com", Title: "Launch database", UpdatedAt: "2026-05-01T16:00:00Z"}.ItemForLayout(LayoutDocument),
		},
	})
	m.width = 180
	m.height = 18
	view := stripANSI(m.View())
	if !strings.Contains(view, "kind") || !strings.Contains(view, "date") || !strings.Contains(view, "title") {
		t.Fatalf("document context columns missing useful labels:\n%s", view)
	}
	if strings.Contains(view, " who ") || strings.Contains(view, " author ") {
		t.Fatalf("document context should not reserve a blank chat author column:\n%s", view)
	}
}

func TestDocumentContextStartsWithKindSections(t *testing.T) {
	m := newModel(Options{
		Title:  "notcrawl archive",
		Layout: LayoutDocument,
		Items: []Item{
			Row{Kind: "page", ParentID: "Marketing", Title: "Gideon's SF Events", UpdatedAt: "2026-05-01T17:52:33Z"}.ItemForLayout(LayoutDocument),
			Row{Kind: "database", ParentID: "Marketing", Title: "Launch database", UpdatedAt: "2026-05-01T16:00:00Z"}.ItemForLayout(LayoutDocument),
		},
	})
	if m.memberSortMode != sortKind {
		t.Fatalf("document member sort = %v, want kind", m.memberSortMode)
	}
	rows := m.currentContextRows()
	if len(rows) != 4 || rows[0].Selectable || rows[0].Label != "DATABASES  (1)" || rows[2].Selectable || rows[2].Label != "PAGES  (1)" {
		t.Fatalf("document context rows should section databases and pages: %#v", rows)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "DATABASES") || !strings.Contains(view, "PAGES") {
		t.Fatalf("document view missing section rows:\n%s", view)
	}
}

func TestDocumentExplorerCyclesGroupViews(t *testing.T) {
	m := newModel(Options{
		Title:  "notcrawl archive",
		Layout: LayoutDocument,
		Items: []Item{
			Row{Kind: "page", ParentID: "folder-a", Scope: "workspace-a", Container: "Roadmap", Title: "Roadmap", UpdatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutDocument),
			Row{Kind: "database", ParentID: "folder-b", Scope: "workspace-a", Container: "Leads", Title: "Leads", UpdatedAt: "2026-05-01T11:00:00Z"}.ItemForLayout(LayoutDocument),
		},
	})
	if groupModeLabel(m.layoutPreset, m.groupMode) != "parent" || len(m.groups) != 2 {
		t.Fatalf("parent groups = %s %#v", groupModeLabel(m.layoutPreset, m.groupMode), m.groups)
	}
	m.cycleGroupMode()
	if groupModeLabel(m.layoutPreset, m.groupMode) != "database" || m.groupPaneTitle() != "Databases" || len(m.groups) != 2 {
		t.Fatalf("database groups = %s title=%s %#v", groupModeLabel(m.layoutPreset, m.groupMode), m.groupPaneTitle(), m.groups)
	}
	m.cycleGroupMode()
	if groupModeLabel(m.layoutPreset, m.groupMode) != "workspace" || m.groupPaneTitle() != "Workspaces" || len(m.groups) != 1 {
		t.Fatalf("workspace groups = %s title=%s %#v", groupModeLabel(m.layoutPreset, m.groupMode), m.groupPaneTitle(), m.groups)
	}
}

func TestLayoutToggleUsesRightStackMode(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{{Title: "alpha", Tags: []string{"page"}}},
	})
	m.width = 160
	m.height = 30
	m.toggleLayout()
	layout := m.layout()
	if layout.mode != string(layoutModeRightStack) {
		t.Fatalf("layout mode = %q, want right-stack", layout.mode)
	}
	if layout.context.x != layout.detail.x || layout.detail.y <= layout.context.y {
		t.Fatalf("right stack should place context over detail: %#v", layout)
	}
}

func TestMediumTmuxPanesUseGitcrawlSplitLayout(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{{Title: "alpha", Tags: []string{"page"}}},
	})
	m.width = 122
	m.height = 34
	layout := m.layout()
	if layout.mode != "split" || !layout.stacked {
		t.Fatalf("layout = %#v, want gitcrawl-style split", layout)
	}
	if layout.rows.y != layout.context.y || layout.detail.y <= layout.rows.y {
		t.Fatalf("split panes should put rows/context above detail: %#v", layout)
	}
	if layout.rows.w+layout.context.w != 122 {
		t.Fatalf("split top panes should fill terminal width: %#v", layout)
	}
	if paneContentWidth(layout.context.w) < 52 || paneContentWidth(layout.rows.w) < 52 {
		t.Fatalf("split panes too narrow for useful columns: %#v", layout)
	}
}

func TestNarrowColumnLayoutKeepsDateAgeAndAuthorColumns(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "github-secure-session-4", Author: "Vincent Koc", Title: "Im working on adding", CreatedAt: "2026-05-02T12:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 122
	m.height = 30
	view := stripANSI(m.View())
	for _, want := range []string{"msg", "date", "age", "channel", "time", "who", "title", "05-02", "Vincent"} {
		if !strings.Contains(view, want) {
			t.Fatalf("narrow column layout missing %q:\n%s", want, view)
		}
	}
}

func TestSplitMemberColumnsUseReadableDates(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "m1", Container: "github-secure-session-4", Author: "Vincent Koc", Title: "Im working on adding", CreatedAt: "2026-05-02T12:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "m2", Container: "github-secure-session-4", Author: "Build Club", Title: "Course Completed", CreatedAt: "2026-05-02T13:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 122
	m.height = 34
	view := stripANSI(m.View())
	if !strings.Contains(view, "05-02") {
		t.Fatalf("split member table should keep compact dates:\n%s", view)
	}
	if strings.Contains(view, "2026-05...") {
		t.Fatalf("split member table should not show truncated timestamps:\n%s", view)
	}
}

func TestRightClickPlacesFloatingMenu(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{{Title: "alpha", Tags: []string{"page"}}},
	})
	m.width = 160
	m.height = 24
	layout := m.layout()
	updated, _ := m.Update(tea.MouseMsg{
		X:      layout.rows.x + 4,
		Y:      layout.rows.y + 3,
		Button: tea.MouseButtonRight,
		Action: tea.MouseActionPress,
	})
	m = updated.(model)
	if !m.menuOpen || !m.menuFloating {
		t.Fatalf("menu open=%v floating=%v", m.menuOpen, m.menuFloating)
	}
	if m.menuRect.w <= 0 || m.menuRect.h <= 0 {
		t.Fatalf("menu rect not placed: %#v", m.menuRect)
	}
	view := m.View()
	if !strings.Contains(view, "Pane") || !strings.Contains(view, "Refresh rows") {
		t.Fatalf("floating menu missing expected sections:\n%s", view)
	}
}

func TestMouseClickUsesFloatingMenuOffset(t *testing.T) {
	m := newModel(Options{Title: "archive", Items: []Item{{Title: "alpha"}}})
	m.width = 140
	m.height = 32
	m.menuOpen = true
	m.menuFloating = true
	m.menuRect = rect{x: 5, y: 3, w: 40, h: 12}
	m.menuOff = 5
	m.menuItems = make([]menuItem, 8)
	for index := range m.menuItems {
		m.menuItems[index] = menuItem{label: fmt.Sprintf("Item %d", index), action: actionQuit}
	}

	updated, cmd := m.Update(tea.MouseMsg{
		X:      m.menuRect.x + 2,
		Y:      m.menuRect.y + 3,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	m = updated.(model)

	if m.menuIndex != 5 {
		t.Fatalf("floating menu click selected %d, want offset row 5", m.menuIndex)
	}
	if cmd == nil {
		t.Fatal("floating menu click did not run selected item")
	}
	if !m.menuOpen || !m.menuFloating {
		t.Fatalf("floating menu should stay open for submenu-like actions, open=%v floating=%v", m.menuOpen, m.menuFloating)
	}
}

func TestMouseMotionHoversFloatingMenuItems(t *testing.T) {
	m := newModel(Options{Title: "archive", Items: []Item{{Title: "alpha"}}})
	m.width = 140
	m.height = 32
	m.menuOpen = true
	m.menuFloating = true
	m.menuRect = rect{x: 5, y: 3, w: 40, h: 12}
	m.menuOff = 1
	m.menuItems = make([]menuItem, 6)
	for index := range m.menuItems {
		m.menuItems[index] = menuItem{label: fmt.Sprintf("Item %d", index), action: actionClose}
	}

	updated, _ := m.Update(tea.MouseMsg{
		X:      m.menuRect.x + 2,
		Y:      m.menuRect.y + 5,
		Button: tea.MouseButtonNone,
		Action: tea.MouseActionMotion,
	})
	m = updated.(model)

	if m.menuIndex != 3 {
		t.Fatalf("hover selected %d, want item 3", m.menuIndex)
	}

	updated, _ = m.Update(tea.MouseMsg{
		X:      m.menuRect.x + 2,
		Y:      m.menuRect.y + 6,
		Button: tea.MouseButtonRight,
		Action: tea.MouseActionMotion,
	})
	m = updated.(model)

	if m.menuIndex != 4 {
		t.Fatalf("right-button hover selected %d, want item 4", m.menuIndex)
	}
}

func TestClickingRowsHeaderSorts(t *testing.T) {
	m := newModel(Options{
		Title: "archive",
		Items: []Item{
			Row{Kind: "page", Title: "Zulu"}.ItemForLayout(LayoutDocument),
			Row{Kind: "page", Title: "Alpha"}.ItemForLayout(LayoutDocument),
		},
	})
	m.width = 160
	m.height = 24
	layout := m.layout()
	updated, _ := m.Update(tea.MouseMsg{
		X:      layout.rows.x + layout.rows.w - 8,
		Y:      layout.rows.y + 2,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	m = updated.(model)
	if m.sortMode != sortTitle {
		t.Fatalf("sort mode = %v, want title", m.sortMode)
	}
	m.selected = 0
	item, ok := m.selectedItem()
	if !ok || item.Title != "Alpha" {
		t.Fatalf("first sorted item = %#v ok=%v", item, ok)
	}
}

func TestClickingRowsCountHeaderSortsLikeGitcrawl(t *testing.T) {
	m := newModel(Options{
		Title:  "discrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "old-a", Container: "general", Author: "Amy", Title: "old a", CreatedAt: "2026-05-01T10:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "old-b", Container: "general", Author: "Bob", Title: "old b", CreatedAt: "2026-05-01T11:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "new", Container: "random", Author: "Cam", Title: "new", CreatedAt: "2026-05-02T10:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 160
	m.height = 24
	layout := m.layout()
	if group, ok := m.currentGroup(); !ok || group.Title != "random" || group.Count != 1 {
		t.Fatalf("initial newest group = %#v ok=%v", group, ok)
	}
	updated, _ := m.Update(tea.MouseMsg{
		X:      layout.rows.x + 2,
		Y:      layout.rows.y + 2,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	m = updated.(model)
	if m.sortMode != sortCount {
		t.Fatalf("sort mode = %v, want count", m.sortMode)
	}
	if len(m.groups) == 0 || m.groups[0].Title != "general" || m.groups[0].Count != 2 {
		t.Fatalf("count-sorted groups = %#v", m.groups)
	}
	if !strings.Contains(stripANSI(m.View()), "msg*") {
		t.Fatalf("count sort header missing active count marker:\n%s", stripANSI(m.View()))
	}
}

func TestClickingContextHeaderUsesContextPaneColumns(t *testing.T) {
	m := newModel(Options{
		Title:  "slacrawl archive",
		Layout: LayoutChat,
		Items: []Item{
			Row{Kind: "message", ID: "z", Container: "general", Author: "Zed", Title: "later", CreatedAt: "2026-05-02T10:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "a", Container: "general", Author: "Amy", Title: "earlier", CreatedAt: "2026-05-02T09:00:00Z"}.ItemForLayout(LayoutChat),
			Row{Kind: "message", ID: "x", Container: "random", Author: "Cam", Title: "other", CreatedAt: "2026-05-02T08:00:00Z"}.ItemForLayout(LayoutChat),
		},
	})
	m.width = 300
	m.height = 24
	layout := m.layout()
	contextWidth := paneContentWidth(layout.context.w)
	columns := m.memberColumns(contextWidth)
	authorX := 0
	for index, column := range columns {
		if column.Key == "author" {
			authorX = columnLeftEdge(columns, index)
			break
		}
	}
	updated, _ := m.Update(tea.MouseMsg{
		X:      layout.context.x + 2 + authorX,
		Y:      layout.context.y + 2,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	m = updated.(model)
	if m.memberSortMode != sortAuthor {
		t.Fatalf("member sort mode = %v, want author", m.memberSortMode)
	}
	members := m.currentGroupMembers()
	if got := m.items[members[0]].Author; got != "Amy" {
		t.Fatalf("first author = %q, want Amy", got)
	}
}

func TestRowStyleUsesSubtleSelectedPalette(t *testing.T) {
	selected := rowStyle(80, true, true, false)
	if fmt.Sprint(selected.GetForeground()) != archiveSelectedFG {
		t.Fatalf("selected foreground = %v, want %s", selected.GetForeground(), archiveSelectedFG)
	}
	if fmt.Sprint(selected.GetBackground()) != archiveSelectedBG {
		t.Fatalf("selected background = %v, want %s", selected.GetBackground(), archiveSelectedBG)
	}
	if fmt.Sprint(selected.GetBackground()) == "#2f3f56" {
		t.Fatal("selected row still uses the old high-contrast blue block")
	}
	blurred := rowStyle(80, true, false, false)
	if fmt.Sprint(blurred.GetBackground()) != archiveBlurSelectedBG {
		t.Fatalf("blurred selected background = %v, want %s", blurred.GetBackground(), archiveBlurSelectedBG)
	}
	active := rowStyle(80, false, false, false)
	if fmt.Sprint(active.GetForeground()) != archiveActiveRowFG || fmt.Sprint(active.GetBackground()) != archiveActiveRowBG {
		t.Fatalf("active row style fg/bg = %v/%v", active.GetForeground(), active.GetBackground())
	}
	inactive := rowStyle(80, false, false, true)
	if fmt.Sprint(inactive.GetForeground()) != archiveInactiveRowFG || fmt.Sprint(inactive.GetBackground()) != archiveInactiveRowBG {
		t.Fatalf("inactive row style fg/bg = %v/%v", inactive.GetForeground(), inactive.GetBackground())
	}
	if !itemInactive(Item{Fields: map[string]string{"deleted": "true"}}) || itemInactive(Item{Fields: map[string]string{"deleted": "false"}}) {
		t.Fatal("inactive item detection did not follow status fields")
	}
}

func TestDocumentLayoutPrioritizesURLDetail(t *testing.T) {
	item := Row{
		Source:    "notion",
		Kind:      "page",
		ID:        "page1",
		Title:     "Launch plan",
		URL:       "https://example.com/launch",
		UpdatedAt: "2026-05-01T12:00:00Z",
	}.ItemForLayout(LayoutDocument)
	if item.Detail != "" {
		t.Fatalf("detail = %q", item.Detail)
	}
	if item.URL != "https://example.com/launch" {
		t.Fatalf("url = %q", item.URL)
	}
	if !strings.Contains(item.Subtitle, "page") || !strings.Contains(item.Subtitle, "2026-05-01") {
		t.Fatalf("subtitle = %q", item.Subtitle)
	}
}

func TestDocumentDetailUsesHeaderPreviewLocationProperties(t *testing.T) {
	item := Row{
		Source:    "notion",
		Kind:      "page",
		ID:        "page1",
		ParentID:  "Launch docs",
		Scope:     "Workspace",
		Container: "Roadmap DB",
		Title:     "Launch plan",
		Text:      "Ship the terminal UI cleanup.",
		URL:       "https://example.com/launch",
		UpdatedAt: "2026-05-01T12:00:00Z",
		Fields:    map[string]string{"space_id": "space1", "parent_table": "collection"},
	}.ItemForLayout(LayoutDocument)
	lines := documentDetailLines(item)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"Launch plan", "Location", "Parent: Launch docs", "Database: Roadmap DB", "Preview", "Ship the terminal UI cleanup.", "Properties", "updated: 2026-05-01 12:00"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("document detail missing %q:\n%s", want, joined)
		}
	}
	if strings.Index(joined, "Preview") > strings.Index(joined, "Properties") {
		t.Fatalf("document preview should come before properties:\n%s", joined)
	}
	if strings.Index(joined, "Preview") > strings.Index(joined, "Location") {
		t.Fatalf("document preview should come before location metadata:\n%s", joined)
	}
}

func TestDocumentDetailRendersMarkdownPreviewLikeGitcrawl(t *testing.T) {
	item := Row{
		Source:    "notion",
		Kind:      "page",
		ID:        "page1",
		ParentID:  "Launch docs",
		Scope:     "Workspace",
		Container: "Roadmap DB",
		Title:     "Launch plan",
		Text:      "# Checklist\n- wire panes\n- review [spec](https://example.com/spec)\n> keep it readable",
		UpdatedAt: "2026-05-01T12:00:00Z",
	}.ItemForLayout(LayoutDocument)
	joined := stripANSI(strings.Join(documentDetailLinesForWidth(item, 56, false), "\n"))
	for _, want := range []string{"Launch plan", "Checklist", "- wire panes", "review spec <https://example.com/spec>", "> keep it readable", "Properties"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("document detail missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "# Checklist") {
		t.Fatalf("document detail should render markdown-ish headings:\n%s", joined)
	}
}

func TestCompactDocumentDetailLimitsLongPreview(t *testing.T) {
	text := strings.Repeat("preview line\n", 30)
	item := Row{
		Source: "notion",
		Kind:   "page",
		Title:  "Launch plan",
		Text:   text,
	}.ItemForLayout(LayoutDocument)
	joined := stripANSI(strings.Join(documentDetailLinesForWidth(item, 56, true), "\n"))
	if !strings.Contains(joined, "Press d for full detail") {
		t.Fatalf("compact document detail should advertise full mode:\n%s", joined)
	}
	if strings.Count(joined, "preview line") > detailBodyLimit(true) {
		t.Fatalf("compact document detail did not limit preview:\n%s", joined)
	}
}

func TestDocumentDetailSeparatesProviderAndSource(t *testing.T) {
	item := Row{
		Source: "notion",
		Kind:   "page",
		ID:     "page1",
		Title:  "Launch plan",
		Fields: map[string]string{"source": "desktop", "zeta": "last", "alpha": "first"},
	}.ItemForLayout(LayoutDocument)
	joined := strings.Join(documentDetailLines(item), "\n")
	for _, want := range []string{"provider: notion", "source: desktop"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("document detail missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "source: notion") {
		t.Fatalf("document detail should not duplicate provider as source:\n%s", joined)
	}
	if strings.Index(joined, "alpha: first") > strings.Index(joined, "zeta: last") {
		t.Fatalf("field tail should be stable and sorted:\n%s", joined)
	}
}

func TestModelFilterAndRender(t *testing.T) {
	m := newModel(Options{
		Title: "notcrawl",
		Items: []Item{
			{Title: "Roadmap", Subtitle: "page", Detail: "product plan"},
			{Title: "Invoices", Subtitle: "database", Detail: "finance rows"},
		},
	})
	m.query = "road"
	m.applyFilter()
	if len(m.filtered) != 1 {
		t.Fatalf("filtered rows = %d, want 1", len(m.filtered))
	}
	view := m.View()
	if !strings.Contains(view, "Roadmap") {
		t.Fatalf("view missing filtered item:\n%s", view)
	}
	if strings.Contains(view, "Invoices") {
		t.Fatalf("view included filtered-out item:\n%s", view)
	}
}

func TestModelRenderUsesArchivePanesAndSourceFooter(t *testing.T) {
	m := newModel(Options{
		Title:          "notcrawl archive",
		SourceKind:     SourceRemote,
		SourceLocation: "git@example.com:archive/notcrawl.git",
		Items: []Item{{
			Title:    "Roadmap",
			Subtitle: "page  workspace",
			Detail:   "product plan",
			Tags:     []string{"notion", "page"},
		}},
	})
	m.width = 120
	m.height = 24
	view := m.View()
	for _, want := range []string{"Groups", "Items", "Detail", "remote git@example.com:archive/notcrawl.git", "Roadmap", "product plan"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	localBG, _ := footerPalette(SourceLocal)
	remoteBG, _ := footerPalette(SourceRemote)
	if localBG == remoteBG {
		t.Fatal("local and remote footers should use different colors")
	}
}

func TestModelRenderUsesCompleteANSISequencesWhenNarrow(t *testing.T) {
	m := newModel(Options{
		Title: "slacrawl archive",
		Items: []Item{{
			Title:    strings.Repeat("a", 80),
			Subtitle: strings.Repeat("b", 80),
			Detail:   strings.Repeat("c", 80),
			Tags:     []string{"slack", "message", "general"},
		}},
	})
	m.width = 24
	m.height = 12
	view := m.View()
	withoutValidEscapes := stripANSI(view)
	if strings.Contains(withoutValidEscapes, "\x1b") {
		t.Fatalf("view contains broken escape sequence: %q", view)
	}
	if strings.Contains(view, "\x1b[") && !strings.Contains(view, "\x1b[0m") {
		t.Fatalf("styled view did not reset styles: %q", view)
	}
}

func TestWrap(t *testing.T) {
	got := wrap("one two three four", 8)
	if got != "one two\nthree\nfour" {
		t.Fatalf("wrap = %q", got)
	}
}
