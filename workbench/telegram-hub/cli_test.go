package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

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
		{"tts with two urls is a usage error", []string{"tts", "https://a.example", "https://b.example"}, errUsage},
		{"tts unknown flag is a usage error", []string{"tts", "https://example.com/a", "-x"}, errUsage},
		{"send without text is a usage error", []string{"send"}, errUsage},
		{"send unknown flag is a usage error", []string{"send", "-x", "hi"}, errUsage},
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

// TestTTSParsesFlagsAnywhere proves flags are accepted on either side of
// the url: execution reaches the url check inside runTTS. The url is
// invalid on purpose, so no network is touched.
func TestTTSParsesFlagsAnywhere(t *testing.T) {
	err := run(context.Background(), []string{"tts", "--model", "m", "-o", "out.mp3", "not-a-url"})
	if err == nil || errors.Is(err, errUsage) || !strings.Contains(err.Error(), "url must start with") {
		t.Fatalf(`run(tts --model m -o out.mp3 not-a-url) error = %v, want the url-validation error`, err)
	}
}

// TestSendParsesWordsAndFlags proves words and flags are accepted
// interleaved: execution reaches the parse-mode validation.
func TestSendParsesWordsAndFlags(t *testing.T) {
	err := run(context.Background(), []string{"send", "hello", "world", "--parse-mode", "bogus"})
	if err == nil || errors.Is(err, errUsage) || !strings.Contains(err.Error(), "parse_mode") {
		t.Fatalf(`run(send hello world --parse-mode bogus) error = %v, want the parse_mode error`, err)
	}
}
