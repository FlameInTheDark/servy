package servers

import (
	"strings"
	"sync"
	"time"

	"github.com/FlameInTheDark/servy/internal/events"
)

// Terminal is the per-server virtual terminal: a ring buffer of sanitized
// lines plus a batcher that fans lines out over the event bus
// (vterm_new_line, batches of up to 20 lines or 100ms).
type Terminal struct {
	mu       sync.Mutex
	lines    []string
	maxLines int
	keywords []string
	// pending batch
	pending  []string
	lastSend time.Time
	// callbacks
	publish func(events.Event)
	onLine  func(line string)
}

// NewTerminal builds a terminal buffer with the configured capacity.
// A background ticker flushes partial batches so single lines still stream.
func NewTerminal(maxLines int, keywords []string) *Terminal {
	if maxLines <= 0 {
		maxLines = 70
	}
	t := &Terminal{maxLines: maxLines, keywords: keywords, lastSend: time.Now()}
	go t.flushLoop()
	return t
}

// flushLoop drains pending batches every 150ms (lifetime: supervisor).
func (t *Terminal) flushLoop() {
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		t.maybeFlush(false)
	}
}

// SetPublisher wires the event bus sink (called once at server registration).
func (t *Terminal) SetPublisher(fn func(events.Event)) { t.publish = fn }

// SetOnLine wires a raw-line observer (used by the player tracker & crash detector).
func (t *Terminal) SetOnLine(fn func(line string)) { t.onLine = fn }

// Add processes raw output bytes: splits on CR/LF (handling incomplete trailing
// lines), strips ANSI escapes, then buffers & batches for broadcast.
func (t *Terminal) Add(raw []byte) {
	text := string(raw)
	// split on CR/LF/LF
	for strings.Contains(text, "\r") {
		// treat \r as line separator too (bedrock consoles use \r progress)
		idx := strings.IndexByte(text, '\r')
		t.pushLine(text[:idx])
		text = text[idx+1:]
	}
	for {
		idx := strings.IndexByte(text, '\n')
		if idx < 0 {
			if text != "" {
				t.pushLine(text)
			}
			break
		}
		t.pushLine(text[:idx])
		text = text[idx+1:]
	}
	t.maybeFlush(false)
}

// pushLine sanitizes and stores one line.
func (t *Terminal) pushLine(line string) {
	line = strings.TrimRight(stripANSI(line), " \t\x00")
	if line == "" && len(t.lines) == 0 {
		return
	}
	t.mu.Lock()
	if len(t.lines) >= t.maxLines {
		t.lines = t.lines[1:]
	}
	t.lines = append(t.lines, line)
	t.pending = append(t.pending, line)
	t.mu.Unlock()
	if t.onLine != nil {
		t.onLine(line)
	}
}

// maybeFlush broadcasts pending lines when the batch is full or the window elapsed.
func (t *Terminal) maybeFlush(force bool) {
	t.mu.Lock()
	if len(t.pending) == 0 {
		t.mu.Unlock()
		return
	}
	full := len(t.pending) >= 20
	elapsed := time.Since(t.lastSend) >= 100*time.Millisecond
	if !full && !elapsed && !force {
		t.mu.Unlock()
		return
	}
	batch := t.pending
	t.pending = nil
	t.lastSend = time.Now()
	t.mu.Unlock()
	if t.publish != nil && len(batch) > 0 {
		t.publish(events.Event{Type: events.EvVtermNewLine, Data: map[string]interface{}{"lines": batch}})
	}
}

// Tail returns up to n buffered lines (n<=0 = all).
func (t *Terminal) Tail(n int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n <= 0 || n > len(t.lines) {
		n = len(t.lines)
	}
	out := make([]string, n)
	copy(out, t.lines[len(t.lines)-n:])
	return out
}

// Clear resets the buffer (server restart).
func (t *Terminal) Clear() {
	t.mu.Lock()
	t.lines = nil
	t.pending = nil
	t.mu.Unlock()
}

// stripANSI removes CSI/OSC escape sequences and backspace art.
func stripANSI(s string) string {
	if !strings.ContainsAny(s, "\x1b\x08") {
		return s
	}
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == 0x1b { // ESC
			if i+1 < len(runes) {
				next := runes[i+1]
				if next == '[' { // CSI ... final byte @-~
					i += 2
					for i < len(runes) && !(runes[i] >= 0x40 && runes[i] <= 0x7e) {
						i++
					}
					continue
				} else if next == ']' { // OSC ... BEL or ST
					i += 2
					for i < len(runes) && runes[i] != 0x07 {
						i++
					}
					continue
				} else {
					i++ // two-char escape
					continue
				}
			}
			continue
		}
		if r == 0x08 { // backspace: drop previous char
			str := b.String()
			if len(str) > 0 {
				b.Reset()
				b.WriteString(str[:len(str)-1])
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
