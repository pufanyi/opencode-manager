package bot

import (
	"bytes"
	"strings"

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
