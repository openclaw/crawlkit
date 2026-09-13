package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m model) detailLines(item Item) []string {
	return m.detailLinesForWidth(item, 1000)
}

func (m model) detailLinesForWidth(item Item, width int) []string {
	width = max(20, width)
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
	raw := markdownLines(value, max(8, width-lipgloss.Width(prefix)))
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
	start := max(0, selectedIndex-radius)
	end := min(len(members), selectedIndex+radius+1)
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
