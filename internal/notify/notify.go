// Package notify sends lightweight out-of-band booking alerts to the studio
// owner over whichever channels are configured. Each channel is optional and
// best-effort: a channel with no config is skipped, and a send failure is
// surfaced to the caller to log but never blocks a booking.
//
// Supported channels:
//   - Telegram (free, no limits) — recommended. See telegram.go.
//   - WhatsApp via CallMeBot (free, but the service is sometimes at capacity).
//     See whatsapp.go.
package notify

import (
	"context"
	"errors"
)

// Notifier sends a plain-text alert. Implementations are safe to call even when
// unconfigured, in which case Send is a no-op returning nil.
type Notifier interface {
	Enabled() bool
	Send(ctx context.Context, message string) error
}

// Multi fans a message out to every enabled notifier and joins any errors.
type Multi struct {
	channels []Notifier
}

// NewMulti builds a Multi from the given notifiers. Nil entries are ignored.
func NewMulti(channels ...Notifier) *Multi {
	cleaned := make([]Notifier, 0, len(channels))
	for _, c := range channels {
		if c != nil {
			cleaned = append(cleaned, c)
		}
	}
	return &Multi{channels: cleaned}
}

// Enabled reports whether at least one underlying channel is configured.
func (m *Multi) Enabled() bool {
	for _, c := range m.channels {
		if c.Enabled() {
			return true
		}
	}
	return false
}

// Send delivers message to every enabled channel, collecting errors so one
// channel failing does not stop the others.
func (m *Multi) Send(ctx context.Context, message string) error {
	var errs []error
	for _, c := range m.channels {
		if !c.Enabled() {
			continue
		}
		if err := c.Send(ctx, message); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
