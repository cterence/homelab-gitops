package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newMistralTestClient(t *testing.T, handler http.HandlerFunc) (*mistralClient, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &mistralClient{
		httpClient:        server.Client(),
		apiKey:            "test-key",
		apiBase:           server.URL,
		systemPromptClean: defaultCleanPrompt,
		systemPromptTitle: defaultTitlePrompt,
	}, server
}

func decodeJSON(t *testing.T, r io.Reader) map[string]any {
	t.Helper()

	var body map[string]any
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}

	return body
}

func TestCleanTextSendsPromptToChatModel(t *testing.T) {
	client, _ := newMistralTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}

		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}

		body := decodeJSON(t, r.Body)
		if body["model"] != "mistral-medium-latest" {
			t.Errorf("model = %v, want mistral-medium-latest", body["model"])
		}
		// temperature 0.1 unmarshals as float64.
		if body["temperature"] != 0.1 {
			t.Errorf("temperature = %v, want 0.1", body["temperature"])
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "cleaned text"}}},
		})
	})

	got, err := client.cleanText(context.Background(), "raw article text")
	if err != nil {
		t.Fatalf("cleanText() error = %v", err)
	}

	if got != "cleaned text" {
		t.Errorf("cleanText() = %q, want %q", got, "cleaned text")
	}
}

func TestCleanTextIncludesArticleInPrompt(t *testing.T) {
	client, _ := newMistralTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeJSON(t, r.Body)
		messages := body["messages"].([]any)

		content := messages[0].(map[string]any)["content"].(string)
		if !strings.Contains(content, "the article body") {
			t.Errorf("prompt missing article text: %q", content)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "ok"}}},
		})
	})

	if _, err := client.cleanText(context.Background(), "the article body"); err != nil {
		t.Fatalf("cleanText() error = %v", err)
	}
}

func TestArticleTitle(t *testing.T) {
	client, _ := newMistralTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeJSON(t, r.Body)
		if body["model"] != "mistral-small-latest" {
			t.Errorf("model = %v, want mistral-small-latest", body["model"])
		}

		messages := body["messages"].([]any)

		content := messages[0].(map[string]any)["content"].(string)
		if !strings.HasSuffix(content, "article headline here") {
			t.Errorf("prompt = %q, want it to end with the article text", content)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "  The Headline  "}}},
		})
	})

	got := client.articleTitle(context.Background(), "article headline here")
	if got != "The Headline" {
		t.Errorf("articleTitle() = %q, want %q", got, "The Headline")
	}
}

func TestArticleTitleTruncatesLongText(t *testing.T) {
	client, _ := newMistralTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeJSON(t, r.Body)
		messages := body["messages"].([]any)

		content := messages[0].(map[string]any)["content"].(string)
		if got := len([]rune(content)); got != len([]rune(defaultTitlePrompt+"\n\n"))+2000 {
			t.Errorf("prompt length = %d, want title prompt + 2000 runes", got)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "ok"}}},
		})
	})

	_ = client.articleTitle(context.Background(), strings.Repeat("é", 5000))
}

func TestArticleTitleFallsBackOnError(t *testing.T) {
	client, _ := newMistralTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	if got := client.articleTitle(context.Background(), "some text"); got != "article" {
		t.Errorf("articleTitle() = %q, want fallback %q", got, "article")
	}
}

func TestArticleTitleFallsBackOnEmptyResult(t *testing.T) {
	client, _ := newMistralTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "   "}}},
		})
	})

	if got := client.articleTitle(context.Background(), "some text"); got != "article" {
		t.Errorf("articleTitle() = %q, want fallback %q", got, "article")
	}
}

func TestGenerateTTS(t *testing.T) {
	client, _ := newMistralTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			t.Errorf("path = %q, want /v1/audio/speech", r.URL.Path)
		}

		body := decodeJSON(t, r.Body)
		if body["model"] != "voxtral-mini-tts-2603" {
			t.Errorf("model = %v, want voxtral-mini-tts-2603", body["model"])
		}

		if body["response_format"] != "mp3" {
			t.Errorf("response_format = %v, want mp3", body["response_format"])
		}

		voiceID, _ := body["voice_id"].(string)
		if voiceID == "" {
			t.Error("voice_id missing from request")
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"audio_data": base64.StdEncoding.EncodeToString([]byte("fake-mp3-bytes")),
		})
	})

	audio, err := client.generateTTS(context.Background(), "Hello there, this is a test of the audio system.")
	if err != nil {
		t.Fatalf("generateTTS() error = %v", err)
	}

	if string(audio) != "fake-mp3-bytes" {
		t.Errorf("generateTTS() = %q, want %q", audio, "fake-mp3-bytes")
	}
}

func TestGenerateTTSVoiceByLanguage(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"english text picks jane", "The quick brown fox jumps over the lazy dog near the river bank every morning.", "gb_jane_neutral"},
		{"french text picks marie", "Le rapide renard brun saute par-dessus le chien paresseux près de la rivière.", "fr_marie_neutral"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newMistralTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				body := decodeJSON(t, r.Body)
				if body["voice_id"] != tt.want {
					t.Errorf("voice_id = %v, want %q", body["voice_id"], tt.want)
				}

				_ = json.NewEncoder(w).Encode(map[string]any{
					"audio_data": base64.StdEncoding.EncodeToString([]byte("x")),
				})
			})

			if _, err := client.generateTTS(context.Background(), tt.text); err != nil {
				t.Fatalf("generateTTS() error = %v", err)
			}
		})
	}
}

func TestSelectVoice(t *testing.T) {
	tests := []struct {
		name string
		lang languageCode
		want string
	}{
		{"english", langEnglish, voiceEnglish},
		{"french", langFrench, voiceFrench},
		{"unknown defaults to french voice", languageCode("de"), voiceFrench},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectVoice(tt.lang); got != tt.want {
				t.Errorf("selectVoice(%q) = %q, want %q", tt.lang, got, tt.want)
			}
		})
	}
}

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		name string
		text string
		want languageCode
	}{
		{"english", "This is a fairly long English sentence about everyday things and places.", langEnglish},
		{"french", "Voici une phrase assez longue en français qui parle de choses quotidiennes.", langFrench},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectLanguage(tt.text); got != tt.want {
				t.Errorf("detectLanguage() = %q, want %q", got, tt.want)
			}
		})
	}
}
