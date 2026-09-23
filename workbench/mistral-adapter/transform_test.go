package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNormalizeContent(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		content string
		reason  string
		ok      bool
	}{
		{
			name:    "plain string passthrough marker",
			raw:     `"just text"`,
			content: "",
			reason:  "",
			ok:      false,
		},
		{
			name:    "null is not an array",
			raw:     `null`,
			content: "",
			reason:  "",
			ok:      false,
		},
		{
			name:    "text only",
			raw:     `[{"type":"text","text":"hello"}]`,
			content: "hello",
			reason:  "",
			ok:      true,
		},
		{
			name:    "thinking then text",
			raw:     `[{"type":"thinking","thinking":[{"type":"text","text":"pondering"}],"closed":true},{"type":"text","text":"answer"}]`,
			content: "answer",
			reason:  "pondering",
			ok:      true,
		},
		{
			name:    "thinking only",
			raw:     `[{"type":"thinking","thinking":[{"type":"text","text":"partial thought"}],"closed":false}]`,
			content: "",
			reason:  "partial thought",
			ok:      true,
		},
		{
			name:    "multiple text parts concatenated",
			raw:     `[{"type":"text","text":"foo"},{"type":"text","text":"bar"}]`,
			content: "foobar",
			reason:  "",
			ok:      true,
		},
		{
			name:    "unrecognized part type falls back to passthrough",
			raw:     `[{"type":"image_url","image_url":{"url":"x"}}]`,
			content: "",
			reason:  "",
			ok:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, reason, ok := normalizeContent([]byte(tt.raw))
			if ok != tt.ok {
				t.Fatalf("normalizeContent() ok = %v, want %v", ok, tt.ok)
			}

			if content != tt.content {
				t.Errorf("normalizeContent() content = %q, want %q", content, tt.content)
			}

			if reason != tt.reason {
				t.Errorf("normalizeContent() reasoning = %q, want %q", reason, tt.reason)
			}
		})
	}
}

func TestTransformChatPayload(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		want     string // JSON object: content / reasoning_content assertions
		wantSame bool   // payload must be returned unchanged
	}{
		{
			name:     "non-streaming typed content array is flattened",
			body:     `{"id":"x","object":"chat.completion","created":1,"model":"zai-glm-5-3","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"hmm"}],"closed":true},{"type":"text","text":"hi there"}],"tool_calls":null},"finish_reason":"stop"}]}`,
			want:     `{"role":"assistant","content":"hi there","reasoning_content":"hmm","tool_calls":null}`,
			wantSame: false,
		},
		{
			name:     "streaming delta chunk is flattened",
			body:     `{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"more thought"}],"closed":false}],"role":null},"finish_reason":null}]}`,
			want:     `{"reasoning_content":"more thought","role":null}`,
			wantSame: false,
		},
		{
			name:     "plain string content is untouched",
			body:     `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"plain"},"finish_reason":"stop"}]}`,
			wantSame: true,
		},
		{
			name:     "tool call chunk without content is untouched",
			body:     `{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"f"}}]},"finish_reason":null}]}`,
			wantSame: true,
		},
		{
			name:     "not json is untouched",
			body:     `not json at all`,
			wantSame: true,
		},
		{
			name:     "error response is untouched",
			body:     `{"error":{"message":"bad","type":"invalid_request_error"}}`,
			wantSame: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := transformChatPayload([]byte(tt.body))
			if tt.wantSame {
				if !bytes.Equal(out, []byte(tt.body)) {
					t.Fatalf("transformChatPayload() = %s, want unchanged", out)
				}

				return
			}

			var root map[string]json.RawMessage
			if err := json.Unmarshal(out, &root); err != nil {
				t.Fatalf("transformChatPayload() output is not JSON: %v", err)
			}

			var choices []map[string]json.RawMessage
			if err := json.Unmarshal(root["choices"], &choices); err != nil {
				t.Fatalf("unmarshalling choices: %v", err)
			}

			var msg map[string]json.RawMessage
			if _, ok := choices[0]["message"]; ok {
				if err := json.Unmarshal(choices[0]["message"], &msg); err != nil {
					t.Fatalf("unmarshalling message: %v", err)
				}
			} else {
				if err := json.Unmarshal(choices[0]["delta"], &msg); err != nil {
					t.Fatalf("unmarshalling delta: %v", err)
				}
			}

			var want map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
				t.Fatalf("test bug: bad want JSON: %v", err)
			}

			for key, wantVal := range want {
				gotVal, ok := msg[key]
				if !ok {
					t.Errorf("key %q missing in transformed message: %s", key, msg)
					continue
				}

				var got, wantDecoded any
				if err := json.Unmarshal(gotVal, &got); err != nil {
					t.Fatalf("unmarshalling %q: %v", key, err)
				}

				if err := json.Unmarshal(wantVal, &wantDecoded); err != nil {
					t.Fatalf("unmarshalling want %q: %v", key, err)
				}

				if got != wantDecoded {
					t.Errorf("key %q = %v, want %v", key, got, wantDecoded)
				}
			}
		})
	}
}

func TestTransformSSELine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "event line untouched",
			line: `event: message`,
			want: `event: message`,
		},
		{
			name: "done sentinel untouched",
			line: "data: [DONE]",
			want: "data: [DONE]",
		},
		{
			name: "empty data line untouched",
			line: `data:`,
			want: `data:`,
		},
		{
			name: "comment untouched",
			line: `: keepalive`,
			want: `: keepalive`,
		},
		{
			name: "plain string chunk untouched",
			line: `data: {"choices":[{"delta":{"content":"hi"}}]}`,
			want: `data: {"choices":[{"delta":{"content":"hi"}}]}`,
		},
		{
			name: "typed chunk rewritten",
			line: `data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"t"}]},{"type":"text","text":"hi"}]}}]}`,
			want: `data: {"choices":[{"delta":{"content":"hi","reasoning_content":"t"}}]}`,
		},
		{
			name: "typed chunk with cr preserved",
			line: "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}]}\r",
			want: "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\r",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transformSSELine([]byte(tt.line))
			if string(got) != tt.want {
				t.Errorf("transformSSELine() = %s, want %s", got, tt.want)
			}
		})
	}
}
