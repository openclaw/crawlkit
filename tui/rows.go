package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

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
