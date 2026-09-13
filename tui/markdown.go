package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func tuiRule(width int) string {
	return strings.Repeat("-", min(72, max(12, width)))
}

func markdownLines(value string, width int) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	width = max(20, width)
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
	contentWidth := max(8, width-lipgloss.Width(prefix))
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
