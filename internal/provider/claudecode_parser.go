package provider

import (
	"encoding/json"
	"strings"
)

// Claude Code stream-json event types (with --include-partial-messages).
type claudeEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	Event         *claudeStreamEvent     `json:"event,omitempty"`
	Message       *claudeMessage         `json:"message,omitempty"`
	Result        string                 `json:"result,omitempty"`
	IsError       bool                   `json:"is_error,omitempty"`
	Error         string                 `json:"error,omitempty"`
	Tool          *claudeTool            `json:"tool,omitempty"`
	ToolInput     map[string]interface{} `json:"tool_input,omitempty"`
	ToolUseResult json.RawMessage        `json:"tool_use_result,omitempty"`
}

type claudeStreamEvent struct {
	Type         string       `json:"type"`
	Delta        *claudeDelta `json:"delta,omitempty"`
	ContentBlock *claudeBlock `json:"content_block,omitempty"`
}

type claudeDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

type claudeMessage struct {
	Content []claudeBlock `json:"content"`
}

type claudeBlock struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

type claudeTool struct {
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

// claudeParser tracks state across stream-json events.
type claudeParser struct {
	textBuf      strings.Builder
	currentTool  string          // tool name from the current content_block_start
	inputBuf     strings.Builder // accumulates input_json_delta for current tool
	pendingTools []string        // FIFO of tools awaiting results
}

func (p *claudeParser) appendText(delta string) string {
	p.textBuf.WriteString(delta)
	return p.textBuf.String()
}

func (p *claudeParser) resetText() {
	p.textBuf.Reset()
}

// extractToolDetail extracts a human-readable detail string from tool input.
func extractToolDetail(name string, tool *claudeTool, topInput map[string]interface{}) string {
	input := topInput
	if tool != nil && tool.Input != nil {
		input = tool.Input
	}
	if input == nil {
		return ""
	}

	switch name {
	case "Agent":
		if desc, ok := input["description"].(string); ok && desc != "" {
			return desc
		}
	case "Bash":
		if cmd, ok := input["command"].(string); ok && cmd != "" {
			if idx := strings.IndexByte(cmd, '\n'); idx >= 0 {
				cmd = cmd[:idx]
			}
			return cmd
		}
		if desc, ok := input["description"].(string); ok && desc != "" {
			return desc
		}
	case "Read":
		if fp, ok := input["file_path"].(string); ok && fp != "" {
			return shortenPath(fp)
		}
	case "Edit":
		if fp, ok := input["file_path"].(string); ok && fp != "" {
			return shortenPath(fp)
		}
	case "Write":
		if fp, ok := input["file_path"].(string); ok && fp != "" {
			return shortenPath(fp)
		}
	case "Grep":
		if pat, ok := input["pattern"].(string); ok && pat != "" {
			return pat
		}
	case "Glob":
		if pat, ok := input["pattern"].(string); ok && pat != "" {
			return pat
		}
	case "WebFetch":
		if url, ok := input["url"].(string); ok && url != "" {
			return url
		}
	case "WebSearch":
		if q, ok := input["query"].(string); ok && q != "" {
			return q
		}
	case "Skill":
		if s, ok := input["skill"].(string); ok && s != "" {
			return s
		}
	case "NotebookEdit":
		if fp, ok := input["notebook_path"].(string); ok && fp != "" {
			return shortenPath(fp)
		}
	case "TodoWrite":
		return "updating tasks"
	}
	return ""
}

// shortenPath returns the last 2 path segments for compact display.
func shortenPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// parseEvent parses a single stream-json line and returns a StreamEvent if relevant.
func (p *claudeParser) parseEvent(line []byte) *StreamEvent {
	var evt claudeEvent
	if err := json.Unmarshal(line, &evt); err != nil {
		return nil
	}

	switch evt.Type {
	case "stream_event":
		if evt.Event == nil {
			return nil
		}
		switch evt.Event.Type {
		case "content_block_start":
			if evt.Event.ContentBlock != nil && evt.Event.ContentBlock.Type == "tool_use" {
				name := evt.Event.ContentBlock.Name
				p.currentTool = name
				p.inputBuf.Reset()
				p.pendingTools = append(p.pendingTools, name)
				return &StreamEvent{Type: "tool_use", ToolName: name, ToolState: "running"}
			}

		case "content_block_delta":
			if evt.Event.Delta == nil {
				return nil
			}
			switch evt.Event.Delta.Type {
			case "text_delta":
				if evt.Event.Delta.Text != "" {
					fullText := p.appendText(evt.Event.Delta.Text)
					return &StreamEvent{Type: "text", Text: fullText}
				}
			case "input_json_delta":
				p.inputBuf.WriteString(evt.Event.Delta.PartialJSON)
			}

		case "content_block_stop":
			// Tool call fully generated — parse accumulated input for detail.
			if p.currentTool != "" {
				name := p.currentTool
				p.currentTool = ""
				detail := p.extractDetailFromInputBuf(name)
				if detail != "" {
					return &StreamEvent{Type: "tool_use", ToolName: name, ToolState: "running", ToolDetail: detail}
				}
			}
		}

	case "assistant":
		if evt.Message == nil {
			return nil
		}
		// Extract text and tool details from the complete message.
		var text string
		for _, block := range evt.Message.Content {
			switch block.Type {
			case "text":
				text += block.Text
			case "tool_use":
				if block.Name != "" && block.Input != nil {
					detail := extractToolDetailFromMap(block.Name, block.Input)
					if detail != "" {
						return &StreamEvent{Type: "tool_use", ToolName: block.Name, ToolState: "running", ToolDetail: detail}
					}
				}
			}
		}
		if text != "" {
			p.resetText()
			p.appendText(text)
			return &StreamEvent{Type: "text", Text: text}
		}

	case "user":
		// A "user" event with tool_use_result means a tool finished executing.
		if evt.ToolUseResult != nil && len(p.pendingTools) > 0 {
			name := p.pendingTools[0]
			p.pendingTools = p.pendingTools[1:]

			state := "completed"
			// String result starting with "Error:" indicates tool error.
			if len(evt.ToolUseResult) > 0 && evt.ToolUseResult[0] == '"' {
				var s string
				if json.Unmarshal(evt.ToolUseResult, &s) == nil && strings.HasPrefix(s, "Error:") {
					state = "error"
				}
			}
			return &StreamEvent{Type: "tool_use", ToolName: name, ToolState: state}
		}

	case "result":
		if evt.Subtype == "error" || evt.IsError {
			errMsg := evt.Error
			if errMsg == "" {
				errMsg = "prompt failed"
			}
			return &StreamEvent{Type: "error", Error: errMsg}
		}
		// Don't emit "done" here — the Prompt() goroutine sends it after
		// merge-back completes, ensuring merge_failed events arrive first.
		return nil
	}

	return nil
}

// extractDetailFromInputBuf parses the accumulated input JSON buffer
// and extracts a human-readable detail string.
func (p *claudeParser) extractDetailFromInputBuf(name string) string {
	raw := p.inputBuf.String()
	if raw == "" {
		return ""
	}
	var input map[string]interface{}
	if json.Unmarshal([]byte(raw), &input) != nil {
		return ""
	}
	return extractToolDetailFromMap(name, input)
}

// extractToolDetailFromMap extracts a human-readable detail string from a tool input map.
func extractToolDetailFromMap(name string, input map[string]interface{}) string {
	return extractToolDetail(name, &claudeTool{Input: input}, nil)
}
