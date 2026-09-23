package main

import (
	"testing"
)

func TestRequestMeta(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		model  string
		stream bool
	}{
		{
			name:   "model and stream flag",
			body:   `{"model":"zai-glm-5-3","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			model:  "zai-glm-5-3",
			stream: true,
		},
		{
			name:   "non streaming",
			body:   `{"model":"mistral-small-latest","messages":[]}`,
			model:  "mistral-small-latest",
			stream: false,
		},
		{
			name:   "missing model",
			body:   `{"stream":true}`,
			model:  "",
			stream: true,
		},
		{
			name:   "not json",
			body:   `garbage`,
			model:  "",
			stream: false,
		},
		{
			name:   "empty body",
			body:   ``,
			model:  "",
			stream: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, stream := requestMeta([]byte(tt.body))
			if model != tt.model {
				t.Errorf("requestMeta() model = %q, want %q", model, tt.model)
			}

			if stream != tt.stream {
				t.Errorf("requestMeta() stream = %v, want %v", stream, tt.stream)
			}
		})
	}
}
