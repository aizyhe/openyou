package llm

import (
	"context"
	"unicode/utf8"
)

type Masker interface {
	Mask(text string) string
	Unmask(text string) string
}

// MaskingClient pseudonymizes every prompt and restores every reply at the single
// choke point all callers share. Drop-in replacement for *Client.
type MaskingClient struct {
	inner  *Client
	masker Masker
}

func NewMaskingClient(inner *Client, masker Masker) *MaskingClient {
	return &MaskingClient{inner: inner, masker: masker}
}

func (m *MaskingClient) mask(s string) string {
	if m.masker == nil {
		return s
	}
	return m.masker.Mask(s)
}

func (m *MaskingClient) unmask(s string) string {
	if m.masker == nil {
		return s
	}
	return m.masker.Unmask(s)
}

// snapshottable marks maskers whose mapping can be reseeded at runtime —
// codenames are positional, so a mid-request reseed would corrupt unmasking.
type snapshottable interface{ Snapshot() any }

// snapshotOf pins the masker for one request; maskers without Snapshot are
// immutable by construction and used as-is.
func snapshotOf(m Masker) Masker {
	if m == nil {
		return nil
	}
	if s, ok := m.(snapshottable); ok {
		if snap, ok2 := s.Snapshot().(Masker); ok2 {
			return snap
		}
	}
	return m
}

func maskWith(mk Masker, s string) string {
	if mk == nil {
		return s
	}
	return mk.Mask(s)
}

func unmaskWith(mk Masker, s string) string {
	if mk == nil {
		return s
	}
	return mk.Unmask(s)
}

func (m *MaskingClient) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	mk := snapshotOf(m.masker)
	out, err := m.inner.Chat(ctx, maskWith(mk, systemPrompt), maskWith(mk, userMessage))
	if err != nil {
		return out, err
	}
	return unmaskWith(mk, out), nil
}

// StreamChat streams the reply for a system+user pair. Masking happens inside
// Stream (single snapshot); pre-masking here would be a redundant second pass.
func (m *MaskingClient) StreamChat(ctx context.Context, systemPrompt, userMessage string, onDelta func(string)) (string, error) {
	msgs := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}
	return m.Stream(ctx, msgs, onDelta)
}

func (m *MaskingClient) ChatMessages(ctx context.Context, messages []Message) (string, error) {
	mk := snapshotOf(m.masker)
	masked := make([]Message, len(messages))
	for i, msg := range messages {
		masked[i] = Message{Role: msg.Role, Content: maskWith(mk, msg.Content)}
	}
	out, err := m.inner.ChatMessages(ctx, masked)
	if err != nil {
		return out, err
	}
	return unmaskWith(mk, out), nil
}

// Stream unmasks deltas before onDelta. A codename can split across deltas and
// unmasking changes byte length, so it accumulates raw text, unmasks whole, and
// emits past a trailing hold window.
func (m *MaskingClient) Stream(ctx context.Context, messages []Message, onDelta func(string)) (string, error) {
	mk := snapshotOf(m.masker)
	masked := make([]Message, len(messages))
	for i, msg := range messages {
		masked[i] = Message{Role: msg.Role, Content: maskWith(mk, msg.Content)}
	}

	hold := holdBytesFor(mk)
	var rawFull string
	var emittedLen int
	var capped bool

	flush := func(final bool) {
		u := unmaskWith(mk, rawFull)
		safe := len(u)
		if !final {
			safe -= hold
		}
		if safe <= emittedLen {
			return
		}
		// Back off to a rune boundary — never split a multi-byte character or
		// emit an incomplete trailing UTF-8 sequence.
		for safe > emittedLen {
			if safe < len(u) {
				if utf8.RuneStart(u[safe]) {
					break
				}
			} else {
				if utf8.ValidString(u[emittedLen:safe]) {
					break
				}
			}
			safe--
		}
		if safe > emittedLen {
			if onDelta != nil {
				onDelta(u[emittedLen:safe])
			}
			emittedLen = safe
		}
	}

	_, err := m.inner.Stream(ctx, masked, func(delta string) {
		if capped {
			return
		}
		rawFull += delta
		// Cap rawFull at 512 KB; past it, flush safely and stop accumulating —
		// trimming would shift masked/unmasked offsets apart.
		const maxRawFullLen = 512 * 1024
		if len(rawFull) > maxRawFullLen {
			capped = true
			flush(true)
			return
		}
		flush(false)
	})
	if capped {
		return unmaskWith(mk, rawFull), err
	}
	final := unmaskWith(mk, rawFull)
	if err != nil {
		// Flush the tail so the client has received exactly what we return;
		// skipping this would persist text the UI never showed.
		flush(true)
		return final, err
	}
	flush(true)
	return final, nil
}

// holdBytesFor withholds trailing bytes until more context arrives —
// MaskWindow() if advertised, else a conservative default.
func holdBytesFor(mk Masker) int {
	const def = 64
	if mk == nil {
		return 0
	}
	if w, ok := mk.(interface{ MaskWindow() int }); ok {
		if n := w.MaskWindow(); n > 0 {
			return n
		}
	}
	return def
}
