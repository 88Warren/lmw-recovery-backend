package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Telegram sends messages to a single chat via the Telegram Bot API. It is free
// with no per-message cost or volume limits for this use case.
//
// One-time setup:
//  1. In Telegram, message @BotFather and create a bot — it returns a token.
//  2. Message your new bot anything (e.g. "hi") so it can message you back.
//  3. Visit https://api.telegram.org/bot<token>/getUpdates and copy the
//     "chat":{"id":...} value — that is your chat ID.
//
// Set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID to enable. When either is empty
// the notifier is disabled and Send is a no-op.
type Telegram struct {
	token  string
	chatID string
	client *http.Client
}

// NewTelegram returns a Telegram notifier. Disabled when token or chatID is empty.
func NewTelegram(token, chatID string) *Telegram {
	return &Telegram{
		token:  token,
		chatID: chatID,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Enabled reports whether the notifier has the config needed to send.
func (t *Telegram) Enabled() bool {
	return t.token != "" && t.chatID != ""
}

// Send delivers a plain-text Telegram message. No-op when disabled.
func (t *Telegram) Send(ctx context.Context, message string) error {
	if !t.Enabled() {
		return nil
	}

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.token)

	form := url.Values{}
	form.Set("chat_id", t.chatID)
	form.Set("text", message)
	// disable link previews so the what3words/map links don't balloon the message
	form.Set("disable_web_page_preview", "true")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("send telegram: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		// Telegram returns a JSON body with a human-readable "description".
		var parsed struct {
			Description string `json:"description"`
		}
		if json.Unmarshal(body, &parsed) == nil && parsed.Description != "" {
			return fmt.Errorf("telegram returned status %d: %s", resp.StatusCode, parsed.Description)
		}
		return fmt.Errorf("telegram returned status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
