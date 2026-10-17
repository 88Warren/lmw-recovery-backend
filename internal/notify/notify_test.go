package notify

import (
	"context"
	"errors"
	"testing"
)

type fakeNotifier struct {
	enabled bool
	err     error
	calls   int
}

func (f *fakeNotifier) Enabled() bool { return f.enabled }
func (f *fakeNotifier) Send(ctx context.Context, msg string) error {
	f.calls++
	return f.err
}

func TestMultiSkipsDisabled(t *testing.T) {
	on := &fakeNotifier{enabled: true}
	off := &fakeNotifier{enabled: false}
	m := NewMulti(on, off, nil)

	if !m.Enabled() {
		t.Fatal("Multi should be enabled when any channel is enabled")
	}
	if err := m.Send(context.Background(), "hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if on.calls != 1 {
		t.Errorf("enabled channel calls = %d, want 1", on.calls)
	}
	if off.calls != 0 {
		t.Errorf("disabled channel calls = %d, want 0", off.calls)
	}
}

func TestMultiJoinsErrors(t *testing.T) {
	e1 := errors.New("boom1")
	e2 := errors.New("boom2")
	a := &fakeNotifier{enabled: true, err: e1}
	b := &fakeNotifier{enabled: true, err: e2}
	m := NewMulti(a, b)

	err := m.Send(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected joined error")
	}
	// Both channels are still attempted even though the first failed.
	if a.calls != 1 || b.calls != 1 {
		t.Errorf("calls a=%d b=%d, want 1 and 1", a.calls, b.calls)
	}
	if !errors.Is(err, e1) || !errors.Is(err, e2) {
		t.Errorf("joined error missing a cause: %v", err)
	}
}

func TestMultiDisabledWhenAllOff(t *testing.T) {
	m := NewMulti(&fakeNotifier{enabled: false})
	if m.Enabled() {
		t.Error("Multi should be disabled when no channel is enabled")
	}
	if err := m.Send(context.Background(), "hi"); err != nil {
		t.Errorf("Send on all-disabled should be nil, got %v", err)
	}
}

func TestChannelsDisabledWithoutConfig(t *testing.T) {
	if NewTelegram("", "").Enabled() {
		t.Error("Telegram should be disabled without token/chatID")
	}
	if NewWhatsApp("", "").Enabled() {
		t.Error("WhatsApp should be disabled without phone/apiKey")
	}
}
