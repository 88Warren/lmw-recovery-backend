// Package notify sends lightweight out-of-band booking alerts to the studio
package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// WhatsApp sends messages to one configured phone number via CallMeBot.
type WhatsApp struct {
	phone  string
	apiKey string
	client *http.Client
}

// NewWhatsApp returns a WhatsApp notifier. If phone or apiKey is empty the
// returned notifier is disabled and Send does nothing (returns nil).
func NewWhatsApp(phone, apiKey string) *WhatsApp {
	return &WhatsApp{
		phone:  phone,
		apiKey: apiKey,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Enabled reports whether the notifier has the config needed to send.
func (w *WhatsApp) Enabled() bool {
	return w.phone != "" && w.apiKey != ""
}

// Send delivers a plain-text WhatsApp message. It is a no-op when disabled.
func (w *WhatsApp) Send(ctx context.Context, message string) error {
	if !w.Enabled() {
		return nil
	}

	q := url.Values{}
	q.Set("phone", w.phone)
	q.Set("text", message)
	q.Set("apikey", w.apiKey)
	endpoint := "https://api.callmebot.com/whatsapp.php?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build whatsapp request: %w", err)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("send whatsapp: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		// Drain a little of the body for context in logs upstream.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("whatsapp returned status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
