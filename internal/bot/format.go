package bot

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// mdParser is a standard goldmark instance with GFM extensions.
// Raw HTML in markdown source is escaped (no WithUnsafe), so goldmark
// controls all tag generation and output is predictable.
var mdParser = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
)

// Regex patterns for HTML tag conversion.
var (
	headingOpenRe  = regexp.MustCompile(`<h[1-6][^>]*>`)
	headingCloseRe = regexp.MustCompile(`</h[1-6]>`)
	hrRe           = regexp.MustCompile(`(?i)<hr\s*/?>`)
	pOpenRe        = regexp.MustCompile(`<p[^>]*>`)
	brRe           = regexp.MustCompile(`(?i)<br\s*/?>`)
	liOpenRe       = regexp.MustCompile(`<li[^>]*>`)
	ulOlOpenRe     = regexp.MustCompile(`<(?:ul|ol)[^>]*>`)
	ulOlCloseRe    = regexp.MustCompile(`</(?:ul|ol)>`)
	imgRe          = regexp.MustCompile(`<img[^>]*>`)
	inputCheckedRe = regexp.MustCompile(`<input[^>]*checked[^>]*/?>`)
	inputRe        = regexp.MustCompile(`<input[^>]*/?>`)
	thCloseRe      = regexp.MustCompile(`</th>`)
	tdCloseRe      = regexp.MustCompile(`</td>`)
	trCloseRe      = regexp.MustCompile(`</tr>`)
	tableTagRe     = regexp.MustCompile(`</?(?:table|thead|tbody|tfoot|tr|th|td|col|colgroup|caption)[^>]*>`)

	// Whitelist: match any HTML tag, used by stripUnsupportedTags to keep only Telegram-supported ones.
	anyTagRe = regexp.MustCompile(`<(/?)([a-zA-Z][a-zA-Z0-9-]*)\b[^>]*>`)
)

// escapeHTML escapes text for Telegram HTML parse mode.
func escapeHTML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return r.Replace(s)
}

// truncateUTF8 truncates s to at most maxBytes bytes without splitting
// a multi-byte UTF-8 character. The result is always valid UTF-8.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
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

// markdownToTelegramHTML converts Markdown to Telegram-compatible HTML.
// Uses goldmark for parsing, then converts unsupported HTML elements.
func markdownToTelegramHTML(md string) string {
	var buf bytes.Buffer
	if err := mdParser.Convert([]byte(md), &buf); err != nil {
		return escapeHTML(md)
	}
	return sanitizeForTelegram(buf.String())
}

// sanitizeForTelegram converts standard HTML to Telegram-supported HTML.
//
// Telegram supports: <b>, <strong>, <i>, <em>, <u>, <ins>, <s>, <strike>, <del>,
// <code>, <pre>, <a>, <blockquote>, <tg-spoiler>.
//
// Everything else must be converted or stripped.
func sanitizeForTelegram(html string) string {
	// Headings → bold + newline
	html = headingOpenRe.ReplaceAllString(html, "<b>")
	html = headingCloseRe.ReplaceAllString(html, "</b>\n")

	// <hr> → text separator
	html = hrRe.ReplaceAllString(html, "\n———\n")

	// Task list checkboxes
	html = inputCheckedRe.ReplaceAllString(html, "✅ ")
	html = inputRe.ReplaceAllString(html, "☐ ")

	// Tables → pipe-delimited text
	html = thCloseRe.ReplaceAllString(html, " | ")
	html = tdCloseRe.ReplaceAllString(html, " | ")
	html = trCloseRe.ReplaceAllString(html, "\n")
	html = tableTagRe.ReplaceAllString(html, "")

	// Lists → bullet text
	html = ulOlOpenRe.ReplaceAllString(html, "")
	html = ulOlCloseRe.ReplaceAllString(html, "")
	html = liOpenRe.ReplaceAllString(html, "• ")
	html = strings.ReplaceAll(html, "</li>", "\n")

	// Paragraphs → newlines
	html = pOpenRe.ReplaceAllString(html, "")
	html = strings.ReplaceAll(html, "</p>", "\n\n")

	// <br> → newline
	html = brRe.ReplaceAllString(html, "\n")

	// Images → strip
	html = imgRe.ReplaceAllString(html, "")

	// Final pass: strip any remaining tags not in Telegram's whitelist
	html = stripUnsupportedTags(html)

	// Clean up whitespace
	for strings.Contains(html, "\n\n\n") {
		html = strings.ReplaceAll(html, "\n\n\n", "\n\n")
	}
	html = strings.TrimSpace(html)

	return html
}

// telegramSupportedTags is the set of HTML tags Telegram accepts.
var telegramSupportedTags = map[string]bool{
	"b": true, "strong": true,
	"i": true, "em": true,
	"u": true, "ins": true,
	"s": true, "strike": true, "del": true,
	"code": true, "pre": true,
	"a": true, "blockquote": true,
	"tg-spoiler": true, "tg-emoji": true,
}

// stripUnsupportedTags removes any HTML tag not in Telegram's supported set.
func stripUnsupportedTags(html string) string {
	return anyTagRe.ReplaceAllStringFunc(html, func(tag string) string {
		matches := anyTagRe.FindStringSubmatch(tag)
		if len(matches) < 3 {
			return ""
		}
		tagName := strings.ToLower(matches[2])
		if telegramSupportedTags[tagName] {
			return tag
		}
		return ""
	})
}

// toolIcon returns the emoji for a tool state.
func toolIcon(state string) string {
	switch state {
	case "running", "pending":
		return "⏳"
	case "completed":
		return "✅"
	case "error":
		return "❌"
	default:
		return "🔧"
	}
}

// formatToolsHTML renders tool statuses as a compact HTML line.
// Deduplicates by name, showing each tool once with its latest state.
func formatToolsHTML(tools []toolStatus) string {
	if len(tools) == 0 {
		return ""
	}

	seen := make(map[string]int) // name → index in deduped
	var deduped []toolStatus

	for _, t := range tools {
		if idx, exists := seen[t.Name]; exists {
			deduped[idx] = t
		} else {
			seen[t.Name] = len(deduped)
			deduped = append(deduped, t)
		}
	}

	var parts []string
	for _, t := range deduped {
		parts = append(parts, fmt.Sprintf("%s <code>%s</code>", toolIcon(t.State), escapeHTML(t.Name)))
	}
	return strings.Join(parts, " · ")
}

// formatToolsPlain renders tool statuses as plain text (no HTML tags).
func formatToolsPlain(tools []toolStatus) string {
	if len(tools) == 0 {
		return ""
	}

	seen := make(map[string]int)
	var deduped []toolStatus

	for _, t := range tools {
		if idx, exists := seen[t.Name]; exists {
			deduped[idx] = t
		} else {
			seen[t.Name] = len(deduped)
			deduped = append(deduped, t)
		}
	}

	var parts []string
	for _, t := range deduped {
		parts = append(parts, fmt.Sprintf("%s %s", toolIcon(t.State), t.Name))
	}
	return strings.Join(parts, " · ")
}
