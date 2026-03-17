package bot

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/leonid-shevtsov/telegold"
	"github.com/yuin/goldmark"
)

var tgRenderer = goldmark.New(goldmark.WithRenderer(telegold.NewRenderer()))

// escapeHTML escapes text for Telegram HTML parse mode.
func escapeHTML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return r.Replace(s)
}

// formatTimeAgo returns a human-readable relative time string.
func formatTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("%dh ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "yesterday"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// markdownToTelegramHTML converts standard Markdown to Telegram-safe HTML.
func markdownToTelegramHTML(md string) string {
	var buf bytes.Buffer
	if err := tgRenderer.Convert([]byte(md), &buf); err != nil {
		return escapeHTML(md)
	}
	result := buf.String()
	// telegold may produce trailing newline
	result = strings.TrimRight(result, "\n")
	return result
}
