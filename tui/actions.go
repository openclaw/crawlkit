package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

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
