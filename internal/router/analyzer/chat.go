package analyzer

// This file implements the Chat Completions rules: messages[] with the OpenAI
// role set, string-or-array content, tools[].function.name, tool_choice, stream
// and the output-token ceiling.

// chat walks one Chat Completions request body.
func (w *walk) chat(root node) {
	if root.kind != kindObject {
		if root.kind != kindInvalid {
			w.noteUnrecognized()
		}
		return
	}
	w.chatMessages(root)
	w.chatTools(root)
	w.chatToolChoice(root)
	w.chatStream(root)
	w.chatMaxOutput(root)
	w.chatReasoning(root)
}

func (w *walk) chatMessages(root node) {
	raw, ok := root.member("messages")
	if !ok || raw.isNull() {
		return
	}
	if raw.kind != kindArray {
		// A client that sends messages as something other than an array is
		// malformed; the HTTP boundary already accepted the body, so the
		// analyzer reports the shape it could not use instead of inventing one.
		w.noteUnrecognized()
		return
	}
	messages := raw.items()
	if len(messages) > maxMessages {
		w.noteTruncated()
		messages = messages[:maxMessages]
	}
	for _, message := range messages {
		w.chatMessage(message)
	}
}

func (w *walk) chatMessage(message node) {
	w.features.MessageCount++
	if message.kind != kindObject {
		w.noteUnrecognized()
		w.addRole("other")
		return
	}
	role := NormalizeRole(message.textMember("role"))
	w.addRole(role)

	if content, ok := message.member("content"); ok && !content.isNull() {
		text, _ := w.contentText(content, role)
		w.recordTurn(role, text)
	}
	// A tool-calling turn may carry the assistant's legacy function_call
	// member, the newer tool_calls array, or both.
	if _, ok := message.member("function_call"); ok {
		w.features.ChainedToolUse = true
	}
	if calls, ok := message.member("tool_calls"); ok && !calls.isNull() {
		if calls.kind != kindArray {
			w.noteUnrecognized()
		} else if len(calls.items()) > 0 {
			w.features.ChainedToolUse = true
		}
	}
	// Chat has no reasoning content part: reasoning arrives as a separate
	// member (reasoning or reasoning_content) on an assistant message.
	if _, ok := message.member("reasoning_content"); ok {
		w.features.ReasoningLikely = true
	}
	if raw, ok := message.member("reasoning"); ok && !raw.isNull() && raw.kind != kindInvalid {
		w.features.ReasoningLikely = true
	}
}

// chatTools records tool names and the hosted file-search signal.
func (w *walk) chatTools(root node) {
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
		// file_search is a hosted retrieval tool: its presence is the routing
		// signal, and it has no function name to record.
		if partTypeOf(tool) == "file_search" {
			w.features.FileSearchUsed = true
			continue
		}
		function, ok := tool.member("function")
		if !ok || function.kind != kindObject {
			w.noteUnrecognized()
			continue
		}
		w.addToolName(function.textMember("name"))
	}
}

// chatToolChoice sets ForcedToolChoice when the client named one function. The
// literal values auto, required and none are provider policy hints, not a
// forced function, so they do not set the flag.
func (w *walk) chatToolChoice(root node) {
	raw, ok := root.member("tool_choice")
	if !ok || raw.isNull() {
		return
	}
	switch raw.kind {
	case kindObject:
		// {"type":"function","function":{"name":"lookup"}}, the legacy
		// {"type":"function","function":"lookup"} shape, or a named hosted tool
		// such as {"type":"file_search"}.
		if function, ok := raw.member("function"); ok {
			if function.kind == kindObject {
				if function.textMember("name") != "" {
					w.features.ForcedToolChoice = true
				}
				return
			}
			if function.kind == kindString && function.text() != "" {
				w.features.ForcedToolChoice = true
				return
			}
		}
		if name := partTypeOf(raw); name != "" && name != "function" {
			w.features.ForcedToolChoice = true
		}
	case kindString:
		// The literal forms are the only legal strings; anything else is a
		// client bug the HTTP boundary would have to reject, not a forced call.
	default:
		w.noteUnrecognized()
	}
}

// chatStream reads the stream switch. A non-boolean value is left as "no
// stream", which is what a provider would do with it.
func (w *walk) chatStream(root node) {
	raw, ok := root.member("stream")
	if !ok {
		return
	}
	if value, ok := raw.boolValue(); ok {
		w.features.StreamRequested = value
	}
}

// chatMaxOutput reads the output-token ceiling. max_completion_tokens takes
// precedence over the deprecated max_tokens, matching the OpenAI contract: when
// both are present the newer field is the one the provider honors.
func (w *walk) chatMaxOutput(root node) {
	for _, name := range []string{"max_completion_tokens", "max_tokens"} {
		raw, ok := root.member(name)
		if !ok {
			continue
		}
		if value, ok := raw.intValue(); ok && value >= 1 {
			w.setMaxOutput(value)
		}
		return
	}
}

// chatReasoning reads the chat-shaped reasoning controls.
func (w *walk) chatReasoning(root node) {
	if effort, ok := root.member("reasoning_effort"); ok {
		if !effort.isNull() && effort.kind != kindInvalid {
			if !IsNoReasoning(effort.stringValue()) {
				w.features.ReasoningLikely = true
			}
		}
	}
	if _, ok := root.member("reasoning"); ok {
		w.features.ReasoningLikely = true
	}
}
