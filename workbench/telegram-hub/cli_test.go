package main

import (
	"context"
	"errors"
	"testing"
)

func TestParseTTSArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    ttsFlags
		wantErr bool
	}{
		{"url only defaults to the cache file", []string{"https://example.com/a"}, ttsFlags{url: "https://example.com/a"}, false},
		{"short output flag", []string{"-o", "out.mp3", "https://example.com/a"}, ttsFlags{url: "https://example.com/a", output: "out.mp3"}, false},
		{"long output flag", []string{"--output", "out.mp3", "https://example.com/a"}, ttsFlags{url: "https://example.com/a", output: "out.mp3"}, false},
		{"output flag with equals", []string{"-o=out.mp3", "https://example.com/a"}, ttsFlags{url: "https://example.com/a", output: "out.mp3"}, false},
		{"long output flag with equals", []string{"--output=out.mp3", "https://example.com/a"}, ttsFlags{url: "https://example.com/a", output: "out.mp3"}, false},
		{"stdout is opt-in", []string{"-o", "-", "https://example.com/a"}, ttsFlags{url: "https://example.com/a", output: "-"}, false},
		{"tts model flag", []string{"--model", "voxtral-mini", "https://example.com/a"}, ttsFlags{url: "https://example.com/a", model: "voxtral-mini"}, false},
		{"tts model flag with equals", []string{"--model=voxtral-mini", "https://example.com/a"}, ttsFlags{url: "https://example.com/a", model: "voxtral-mini"}, false},
		{"clean model flag", []string{"--clean-model", "mistral-small", "url"}, ttsFlags{url: "url", cleanModel: "mistral-small"}, false},
		{"title model flag", []string{"--title-model", "mistral-small", "url"}, ttsFlags{url: "url", titleModel: "mistral-small"}, false},
		{"api base flag", []string{"--api-base", "http://localhost:9999", "url"}, ttsFlags{url: "url", apiBase: "http://localhost:9999"}, false},
		{"voice flags", []string{"--voice-en", "en-voice", "--voice-fr=fr-voice", "url"}, ttsFlags{url: "url", voiceEn: "en-voice", voiceFr: "fr-voice"}, false},
		{"all overrides together", []string{
			"--model", "m", "--clean-model", "c", "--title-model", "t",
			"--api-base", "http://x", "--voice-en", "ve", "--voice-fr", "vf",
			"https://example.com/a", "-o", "out.mp3",
		}, ttsFlags{
			url: "https://example.com/a", output: "out.mp3",
			model: "m", cleanModel: "c", titleModel: "t",
			apiBase: "http://x", voiceEn: "ve", voiceFr: "vf",
		}, false},
		{"missing url", []string{"-o", "out.mp3"}, ttsFlags{}, true},
		{"missing output value", []string{"https://example.com/a", "-o"}, ttsFlags{}, true},
		{"missing model value", []string{"https://example.com/a", "--model"}, ttsFlags{}, true},
		{"unknown flag", []string{"https://example.com/a", "-x"}, ttsFlags{}, true},
		{"two urls", []string{"https://a.example", "https://b.example"}, ttsFlags{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTTSArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseTTSArgs(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}

			if err != nil {
				return
			}

			if got != tt.want {
				t.Errorf("parseTTSArgs(%v) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestParseSendArgs(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantText     string
		wantParseMod string
		wantErr      bool
	}{
		{"single word", []string{"hello"}, "hello", "", false},
		{"words are joined", []string{"hello", "world"}, "hello world", "", false},
		{"parse mode", []string{"--parse-mode", "HTML", "hi"}, "hi", "HTML", false},
		{"no text", nil, "", "", true},
		{"missing parse mode value", []string{"--parse-mode"}, "", "", true},
		{"unknown flag", []string{"-x", "hi"}, "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, parseMode, err := parseSendArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseSendArgs() error = %v, wantErr %v", err, tt.wantErr)
			}

			if text != tt.wantText || parseMode != tt.wantParseMod {
				t.Errorf("parseSendArgs() = (%q, %q), want (%q, %q)", text, parseMode, tt.wantText, tt.wantParseMod)
			}
		})
	}
}

func TestRunDispatch(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr error
	}{
		{"bare invocation prints help and exits clean", nil, nil},
		{"help exits clean", []string{"help"}, nil},
		{"short help exits clean", []string{"-h"}, nil},
		{"unknown command is a usage error", []string{"bogus"}, errUsage},
		{"tts without url is a usage error", []string{"tts"}, errUsage},
		{"send without text is a usage error", []string{"send"}, errUsage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(context.Background(), tt.args)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("run(%v) error = %v, want %v", tt.args, err, tt.wantErr)
			}
		})
	}
}
