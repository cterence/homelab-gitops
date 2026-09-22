package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/abadojack/whatlanggo"
)

const (
	mistralAPIBase = "https://api.mistral.ai"

	chatCleanModel = "mistral-medium-latest"
	chatTitleModel = "mistral-small-latest"
	ttsModel       = "voxtral-mini-tts-2603"

	voiceEnglish = "gb_jane_neutral"
	voiceFrench  = "fr_marie_neutral"

	// titleMaxRunes caps how much raw text is sent when extracting the
	// title, matching the previous Python behavior.
	titleMaxRunes = 2000
)

type languageCode string

const (
	langEnglish languageCode = "en"
	langFrench  languageCode = "fr"
)

// mistralClient calls the Mistral REST API.
type mistralClient struct {
	httpClient        *http.Client
	apiKey            string
	apiBase           string
	systemPromptClean string
	systemPromptTitle string
}

func newMistralClient(httpClient *http.Client, apiKey, apiBase, cleanPrompt, titlePrompt string) *mistralClient {
	return &mistralClient{
		httpClient:        httpClient,
		apiKey:            apiKey,
		apiBase:           apiBase,
		systemPromptClean: cleanPrompt,
		systemPromptTitle: titlePrompt,
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// chatComplete runs a single-turn chat completion.
func (m *mistralClient) chatComplete(ctx context.Context, model, prompt string) (string, error) {
	reqBody, err := json.Marshal(chatRequest{
		Model:       model,
		Messages:    []chatMessage{{Role: "user", Content: prompt}},
		Temperature: 0.1,
	})
	if err != nil {
		return "", fmt.Errorf("encoding chat request: %w", err)
	}

	url := m.apiBase + "/v1/chat/completions"

	content, err := m.postJSON(ctx, url, reqBody)
	if err != nil {
		return "", fmt.Errorf("chat completion failed: %w", err)
	}

	var response chatResponse
	if err := json.Unmarshal(content, &response); err != nil {
		return "", fmt.Errorf("decoding chat response: %w", err)
	}

	if len(response.Choices) == 0 {
		return "", errors.New("chat response had no choices")
	}

	return response.Choices[0].Message.Content, nil
}

// cleanText strips non-article content from raw text so it reads naturally
// when spoken.
func (m *mistralClient) cleanText(ctx context.Context, rawText string) (string, error) {
	prompt := m.systemPromptClean + "\n\n" + rawText
	return m.chatComplete(ctx, chatCleanModel, prompt)
}

// articleTitle extracts the article's main title; on any failure it falls
// back to a generic title so delivery is never blocked.
func (m *mistralClient) articleTitle(ctx context.Context, rawText string) string {
	title, err := m.chatComplete(ctx, chatTitleModel, m.systemPromptTitle+"\n\n"+truncateRunes(rawText, titleMaxRunes))
	if err != nil || strings.TrimSpace(title) == "" {
		return "article"
	}

	return strings.TrimSpace(title)
}

type ttsRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	VoiceID        string `json:"voice_id"`
	ResponseFormat string `json:"response_format"`
}

type ttsResponse struct {
	AudioData string `json:"audio_data"`
}

// generateTTS converts text to MP3 audio, picking the voice from the
// detected language.
func (m *mistralClient) generateTTS(ctx context.Context, text string) ([]byte, error) {
	reqBody, err := json.Marshal(ttsRequest{
		Model:          ttsModel,
		Input:          text,
		VoiceID:        selectVoice(detectLanguage(text)),
		ResponseFormat: "mp3",
	})
	if err != nil {
		return nil, fmt.Errorf("encoding TTS request: %w", err)
	}

	url := m.apiBase + "/v1/audio/speech"

	content, err := m.postJSON(ctx, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("TTS generation failed: %w", err)
	}

	var response ttsResponse
	if err := json.Unmarshal(content, &response); err != nil {
		return nil, fmt.Errorf("decoding TTS response: %w", err)
	}

	audio, err := base64.StdEncoding.DecodeString(response.AudioData)
	if err != nil {
		return nil, fmt.Errorf("decoding audio data: %w", err)
	}

	return audio, nil
}

func (m *mistralClient) postJSON(ctx context.Context, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	// The body is fully read below; nothing to propagate from Close.
	defer func() { _ = resp.Body.Close() }()

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateRunes(string(content), 200))
	}

	return content, nil
}

// detectLanguage returns the ISO-ish code for the text's language,
// defaulting to English when detection is inconclusive.
func detectLanguage(text string) languageCode {
	info := whatlanggo.Detect(text)
	switch info.Lang {
	case whatlanggo.Eng:
		return langEnglish
	case whatlanggo.Fra:
		return langFrench
	default:
		return languageCode(strings.ToLower(info.Lang.String()))
	}
}

// selectVoice maps the detected language to a TTS voice: English gets Jane,
// everything else gets Marie.
func selectVoice(lang languageCode) string {
	if lang == langEnglish {
		return voiceEnglish
	}

	return voiceFrench
}

// truncateRunes caps a string at n runes without splitting a multi-byte
// character.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}

	r := []rune(s)
	if len(r) <= n {
		return s
	}

	return string(r[:n])
}
