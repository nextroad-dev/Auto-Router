package analyzer

// This file implements the Responses API rules: instructions plus input, where
// input is either a plain string or an item array.
//
// The item set is closed on purpose. Every recognized item type updates a named
// feature; anything else counts into UnrecognizedParts so a newer protocol
// revision shows up as a signal instead of being silently ignored. Only the
// message and function_call_output items enter the conversation view: the other
// items carry protocol machinery (tool calls, retrieval results, reasoning) that
// the routing question does not need, and forwarding them would send more client
// text to a hosted decision service for no benefit.

// responses walks one Responses API request body.
func (w *walk) responses(root node) {
	if root.kind != kindObject {
		if root.kind != kindInvalid {
			w.noteUnrecognized()
		}
		return
	}
	w.responsesInstructions(root)
	w.responsesInput(root)
	w.responsesTools(root)
	w.responsesToolChoice(root)
	w.responsesStream(root)
	w.responsesMaxOutput(root)
	w.responsesReasoning(root)
}

// responsesInstructions reads the system prompt. The Responses API puts it in
// "instructions"; a non-string value (for example an array of parts) is walked
// as content so a compatible client still contributes its system text.
func (w *walk) responsesInstructions(root node) {
	raw, ok := root.member("instructions")
	if !ok || raw.isNull() {
		return
	}
	if text, _ := w.contentText(raw, "system"); text != "" {
		w.addSystem(text)
	}
}

// responsesInput walks the input, which is either one string or an item array.
func (w *walk) responsesInput(root node) {
	raw, ok := root.member("input")
	if !ok || raw.isNull() {
		return
	}
	switch raw.kind {
	case kindString:
		// A bare string is a single user message.
		text := w.recordText("user", raw.text())
		w.features.MessageCount++
		w.addRole("user")
		w.addViewMessage("user", text)
	case kindArray:
		items := raw.items()
		if len(items) > maxItems {
			w.noteTruncated()
			items = items[:maxItems]
		}
		for _, item := range items {
			w.responsesItem(item)
		}
	default:
		if raw.kind != kindInvalid {
			w.noteUnrecognized()
		}
	}
}

// responsesItem interprets one input item.
func (w *walk) responsesItem(item node) {
	if item.kind != kindObject {
		if item.kind != kindInvalid {
			w.noteUnrecognized()
		}
		return
	}
	switch partTypeOf(item) {
	case "message":
		w.responsesMessage(item)
	case "function_call":
		w.features.MessageCount++
		w.addRole("assistant")
		w.features.ChainedToolUse = true
	case "function_call_output":
		w.responsesToolOutput(item)
	case "reasoning":
		w.features.MessageCount++
		w.addRole("reasoning")
		w.features.ReasoningLikely = true
	case "file_search_call":
		w.features.MessageCount++
		w.addRole("file_search")
		w.features.FileSearchUsed = true
	case "":
		// An item without a type tag that looks like a message is still a
		// message; anything else is counted as unrecognized.
		if _, ok := item.member("role"); ok {
			w.responsesMessage(item)
			return
		}
		w.noteUnrecognized()
	default:
		// A supported-but-not-vision item (web_search_call, computer_call,
		// image_generation_call, mcp_call, ...) is protocol machinery for the
		// provider, not an input the router classifies. It is counted as an
		// unrecognized item rather than silently dropped, so the feature set
		// still says "this request contained something stage 5 does not model".
		w.features.MessageCount++
		w.addRole("other")
		w.noteUnrecognized()
	}
}

// responsesMessage handles one message item: role plus content.
func (w *walk) responsesMessage(item node) {
	role := NormalizeRole(item.textMember("role"))
	w.features.MessageCount++
	w.addRole(role)
	raw, ok := item.member("content")
	if !ok || raw.isNull() {
		return
	}
	text, _ := w.contentText(raw, role)
	w.recordTurn(role, text)
}

// responsesToolOutput handles a function_call_output item: the tool result,
// which the routing question needs because it usually contains the payload the
// next turn reasons about.
func (w *walk) responsesToolOutput(item node) {
	w.features.MessageCount++
	w.addRole("tool")
	text := ""
	if output, ok := item.member("output"); ok && !output.isNull() {
		text, _ = w.contentText(output, "tool")
	}
	w.addViewMessage("tool", text)
}

// responsesTools records tool names, including MCP servers and hosted tools.
func (w *walk) responsesTools(root node) {
	raw, ok := root.member("tools")
	if !ok || raw.isNull() {
		return
	}
	if raw.kind != kindArray {
		w.noteUnrecognized()
		return
	}
	tools := raw.items()
	if len(tools) == 0 {
		return
	}
	w.features.HasTools = true
	w.features.ToolCount = len(tools)
	for _, tool := range tools {
		if tool.kind != kindObject {
			w.noteUnrecognized()
			continue
		}
		if partTypeOf(tool) == "file_search" {
			w.features.FileSearchUsed = true
		}
		// A function tool carries "name"; an MCP tool carries "server_label";
		// a hosted tool (web_search, file_search, code_interpreter) carries
		// only "type", which is already the retrieval signal where it matters.
		name := tool.textMember("name")
		if name == "" {
			name = tool.textMember("server_label")
		}
		if name == "" {
			name = partTypeOf(tool)
		}
		w.addToolName(name)
	}
}

// responsesToolChoice sets ForcedToolChoice when the client named a tool.
// The literals auto, none and required are not a forced choice.
func (w *walk) responsesToolChoice(root node) {
	raw, ok := root.member("tool_choice")
	if !ok || raw.isNull() {
		return
	}
	switch raw.kind {
	case kindString:
		// Only the literal forms are meaningful here.
	case kindObject:
		// {"type":"function","name":"lookup"}, {"type":"mcp",...} or a named
		// hosted tool all force a choice.
		if name := raw.textMember("name"); name != "" {
			w.features.ForcedToolChoice = true
			return
		}
		if choiceType := partTypeOf(raw); choiceType != "" && choiceType != "function" && choiceType != "mcp" {
			w.features.ForcedToolChoice = true
		}
	default:
		w.noteUnrecognized()
	}
}

// responsesStream reads the stream switch.
func (w *walk) responsesStream(root node) {
	raw, ok := root.member("stream")
	if !ok {
		return
	}
	if value, ok := raw.boolValue(); ok {
		w.features.StreamRequested = value
	}
}

// responsesMaxOutput reads max_output_tokens.
func (w *walk) responsesMaxOutput(root node) {
	raw, ok := root.member("max_output_tokens")
	if !ok {
		return
	}
	if value, ok := raw.intValue(); ok && value >= 1 {
		w.setMaxOutput(value)
	}
}

// responsesReasoning reads the reasoning control object. Any non-null object
// requests reasoning: the effort and summary fields inside it only refine how
// much, which stage 5 does not need to distinguish.
func (w *walk) responsesReasoning(root node) {
	raw, ok := root.member("reasoning")
	if !ok || raw.isNull() || raw.kind == kindInvalid {
		return
	}
	if raw.kind == kindObject {
		if effort, ok := raw.member("effort"); ok && IsNoReasoning(effort.stringValue()) {
			// {"effort": "none"} is an explicit request for no reasoning.
			return
		}
	}
	w.features.ReasoningLikely = true
}
