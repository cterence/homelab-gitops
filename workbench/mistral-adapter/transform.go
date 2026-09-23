package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

// contentPart mirrors one element of the typed content array Mistral returns
// for GLM reasoning models served over the OpenAI-compatible chat completions
// API (see https://github.com/mistralai/mistral-vibe/issues/1119):
//
//	[{"type":"thinking","thinking":[{"type":"text","text":"..."}],"closed":true},
//	 {"type":"text","text":"..."}]
type contentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text"`
	Thinking []contentPart `json:"thinking"`
}

// normalizeContent flattens a typed content array into visible text and
// reasoning text. ok is false when raw is not a transformable content array
// (plain string, null, or contains unrecognized part types) and the caller
// must pass the payload through untouched.
func normalizeContent(raw []byte) (content string, reasoning string, ok bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return "", "", false
	}

	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", "", false
	}

	var text, think strings.Builder

	for _, part := range parts {
		switch part.Type {
		case "text":
			text.WriteString(part.Text)
		case "thinking":
			for _, inner := range part.Thinking {
				think.WriteString(inner.Text)
			}
		default:
			return "", "", false
		}
	}

	return text.String(), think.String(), true
}

// transformMessageObject rewrites one message or delta object of a chat
// completion payload in place. It returns false when nothing changed.
func transformMessageObject(raw []byte) ([]byte, bool) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return raw, false
	}

	rawContent, ok := msg["content"]
	if !ok {
		return raw, false
	}

	content, reasoning, transformed := normalizeContent(rawContent)
	if !transformed {
		return raw, false
	}

	if content != "" {
		msg["content"], _ = json.Marshal(content)
	} else {
		delete(msg, "content")
	}

	if reasoning != "" {
		msg["reasoning_content"], _ = json.Marshal(reasoning)
	}

	out, err := json.Marshal(msg)
	if err != nil {
		return raw, false
	}

	return out, true
}

// transformChatPayload rewrites a chat completion JSON payload (a full
// non-streaming response or a single SSE chunk), flattening typed content
// arrays in choices[].message and choices[].delta into a plain string content
// with the reasoning moved to reasoning_content. Any payload it does not
// understand is returned unchanged.
func transformChatPayload(body []byte) []byte {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return body
	}

	rawChoices, ok := root["choices"]
	if !ok {
		return body
	}

	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(rawChoices, &choices); err != nil {
		return body
	}

	changed := false

	for i, choice := range choices {
		for _, key := range []string{"message", "delta"} {
			rawMsg, ok := choice[key]
			if !ok {
				continue
			}

			newMsg, msgChanged := transformMessageObject(rawMsg)
			if !msgChanged {
				continue
			}

			choice[key] = newMsg
			choices[i] = choice
			changed = true
		}
	}

	if !changed {
		return body
	}

	root["choices"], _ = json.Marshal(choices)

	out, err := json.Marshal(root)
	if err != nil {
		return body
	}

	return out
}

// transformSSELine rewrites one SSE line (without its trailing newline) if it
// carries a chat chunk. Non-data lines and payloads that need no changes are
// returned untouched.
func transformSSELine(line []byte) []byte {
	trimmed := bytes.TrimLeft(line, " \t")
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return line
	}

	payload := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	if len(payload) == 0 {
		return line
	}

	out := transformChatPayload(payload)
	if bytes.Equal(out, payload) {
		return line
	}

	cr := []byte{}
	if bytes.HasSuffix(line, []byte("\r")) {
		cr = []byte("\r")
	}

	rewritten := append([]byte("data: "), out...)

	return append(rewritten, cr...)
}
