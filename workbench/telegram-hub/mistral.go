package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	mistral "github.com/cterence/mistral-client-go/mistral"

	"github.com/abadojack/whatlanggo"
)

const (
	mistralAPIBase = "https://api.mistral.ai"

	chatCleanModel = "mistral-medium-latest"
	chatTitleModel = "mistral-small-latest"
	ttsModel       = "voxtral-mini-tts-latest"

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

// mistralClient calls the Mistral REST API through the
// cterence/mistral-client-go SDK.
type mistralClient struct {
	client            *mistral.APIClient
	systemPromptClean string
	systemPromptTitle string
	chatCleanModel    string
	chatTitleModel    string
	ttsModel          string
	voiceEnglish      string
	voiceFrench       string
}

func newMistralClient(httpClient *http.Client, cfg config) *mistralClient {
	sdkCfg := mistral.NewConfiguration()
	sdkCfg.HTTPClient = httpClient

	if u, err := url.Parse(cfg.mistralAPIBase); err == nil && u.Host != "" {
		sdkCfg.Scheme = u.Scheme
		sdkCfg.Host = u.Host
	}

	sdkCfg.AddDefaultHeader("Authorization", "Bearer "+cfg.mistralAPIKey)

	return &mistralClient{
		client:            mistral.NewAPIClient(sdkCfg),
		systemPromptClean: cfg.systemPromptClean,
		systemPromptTitle: defaultTitlePrompt,
		chatCleanModel:    cfg.chatCleanModel,
		chatTitleModel:    cfg.chatTitleModel,
		ttsModel:          cfg.ttsModel,
		voiceEnglish:      cfg.voiceEnglish,
		voiceFrench:       cfg.voiceFrench,
	}
}

// chatComplete runs a single-turn chat completion.
func (m *mistralClient) chatComplete(ctx context.Context, model, prompt string) (string, error) {
	resp, _, err := m.client.ChatAPI.ChatCompletionV1ChatCompletionsPost(ctx).
		ChatCompletionRequest(mistral.ChatCompletionRequest{
			Model: model,
			Messages: []mistral.MessagesInner{mistral.UserMessageAsMessagesInner(&mistral.UserMessage{
				Role:    mistral.PtrString("user"),
				Content: *mistral.NewNullableContent3(&mistral.Content3{String: mistral.PtrString(prompt)}),
			})},
			Temperature: *mistral.NewNullableFloat32(mistral.PtrFloat32(0.1)),
		}).Execute()
	if err != nil {
		return "", fmt.Errorf("chat completion failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", errors.New("chat response had no choices")
	}

	if content := resp.Choices[0].Message.Content.Get().String; content != nil {
		return *content, nil
	}

	return "", errors.New("chat response had no content")
}

// cleanText strips non-article content from raw text so it reads naturally
// when spoken.
func (m *mistralClient) cleanText(ctx context.Context, rawText string) (string, error) {
	prompt := m.systemPromptClean + "\n\n" + rawText
	return m.chatComplete(ctx, m.chatCleanModel, prompt)
}

// articleTitle extracts the article's main title; on any failure it falls
// back to a generic title so delivery is never blocked.
func (m *mistralClient) articleTitle(ctx context.Context, rawText string) string {
	title, err := m.chatComplete(ctx, m.chatTitleModel, m.systemPromptTitle+"\n\n"+truncateRunes(rawText, titleMaxRunes))
	if err != nil || strings.TrimSpace(title) == "" {
		return "article"
	}

	return strings.TrimSpace(title)
}

// generateTTS converts text to MP3 audio, picking the voice from the
// detected language.
func (m *mistralClient) generateTTS(ctx context.Context, text string) ([]byte, error) {
	format := mistral.SPEECHOUTPUTFORMAT_MP3

	resp, _, err := m.client.AudioSpeechAPI.SpeechV1AudioSpeechPost(ctx).
		SpeechRequest(mistral.SpeechRequest{
			Model:          *mistral.NewNullableString(mistral.PtrString(m.ttsModel)),
			VoiceId:        *mistral.NewNullableString(mistral.PtrString(m.selectVoice(detectLanguage(text)))),
			Input:          text,
			ResponseFormat: &format,
		}).Execute()
	if err != nil {
		return nil, fmt.Errorf("TTS generation failed: %w", err)
	}

	audio, err := base64.StdEncoding.DecodeString(resp.AudioData)
	if err != nil {
		return nil, fmt.Errorf("decoding audio data: %w", err)
	}

	return audio, nil
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

// selectVoice maps the detected language to a configured TTS voice.
func (m *mistralClient) selectVoice(lang languageCode) string {
	if lang == langEnglish {
		return m.voiceEnglish
	}

	return m.voiceFrench
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
