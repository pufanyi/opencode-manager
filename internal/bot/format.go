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

	// Balance tags to ensure all are properly closed/nested
	html = balanceHTML(html)

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

// balanceHTML ensures all HTML tags are properly closed and nested.
// It closes unclosed tags at the end, removes orphaned closing tags,
// and fixes nesting violations (e.g., <strong> inside <code>).
func balanceHTML(html string) string {
	// Tags that cannot contain formatting (bold/italic/etc.)
	noFormatTags := map[string]bool{"code": true, "pre": true}
	formatTags := map[string]bool{
		"b": true, "strong": true, "i": true, "em": true,
		"u": true, "ins": true, "s": true, "strike": true, "del": true,
	}

	var result strings.Builder
	var stack []string // open tag names
	i := 0

	for i < len(html) {
		// Find next tag
		tagStart := strings.Index(html[i:], "<")
		if tagStart == -1 {
			result.WriteString(html[i:])
			break
		}
		tagStart += i

		// Write text before the tag
		result.WriteString(html[i:tagStart])

		tagEnd := strings.Index(html[tagStart:], ">")
		if tagEnd == -1 {
			// Incomplete tag at end — strip it
			break
		}
		tagEnd += tagStart + 1
		tag := html[tagStart:tagEnd]

		matches := anyTagRe.FindStringSubmatch(tag)
		if matches == nil {
			// Not a recognized tag pattern (e.g., &lt;), pass through
			result.WriteString(tag)
			i = tagEnd
			continue
		}

		isClosing := matches[1] == "/"
		tagName := strings.ToLower(matches[2])

		if !isClosing {
			// Check for nesting violation: format tag inside code/pre
			inNoFormat := false
			for _, s := range stack {
				if noFormatTags[s] {
					inNoFormat = true
					break
				}
			}
			if inNoFormat && formatTags[tagName] {
				// Skip this tag — can't nest formatting inside code/pre
				i = tagEnd
				continue
			}

			// Self-closing tags (like <br/>, <hr/>, <img/>) don't go on stack
			if !strings.HasSuffix(strings.TrimSpace(tag[:len(tag)-1]), "/") {
				stack = append(stack, tagName)
			}
			result.WriteString(tag)
		} else {
			// Find matching open tag on stack
			found := -1
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j] == tagName {
					found = j
					break
				}
			}
			if found == -1 {
				// Orphaned closing tag — skip it
				i = tagEnd
				continue
			}

			// Close any tags that were opened after the matching one
			for j := len(stack) - 1; j > found; j-- {
				result.WriteString(fmt.Sprintf("</%s>", stack[j]))
			}
			result.WriteString(tag)
			stack = stack[:found]
		}

		i = tagEnd
	}

	// Close remaining unclosed tags in reverse order
	for j := len(stack) - 1; j >= 0; j-- {
		result.WriteString(fmt.Sprintf("</%s>", stack[j]))
	}

	return result.String()
}

// truncateHTML truncates HTML content to maxBytes without breaking tags.
// It finds a safe cut point before maxBytes, then closes any unclosed tags.
func truncateHTML(html string, maxBytes int) string {
	if len(html) <= maxBytes {
		return html
	}

	// Find a safe cut point: not inside a tag
	cut := maxBytes
	// If we're inside a tag (between < and >), back up to before the <
	for j := cut - 1; j >= 0; j-- {
		if html[j] == '>' {
			break // We're not inside a tag
		}
		if html[j] == '<' {
			cut = j // Cut before this incomplete tag
			break
		}
	}

	// Also ensure we don't cut in the middle of an HTML entity (&amp; etc.)
	for cut > 0 && html[cut-1] == '&' {
		cut--
	}
	// Check if we're inside an entity (between & and ;)
	for j := cut - 1; j >= 0 && j > cut-10; j-- {
		if html[j] == ';' || html[j] == ' ' || html[j] == '\n' {
			break
		}
		if html[j] == '&' {
			cut = j
			break
		}
	}

	// UTF-8 safe
	for cut > 0 && !utf8.RuneStart(html[cut]) {
		cut--
	}

	truncated := html[:cut]
	return balanceHTML(truncated)
}
