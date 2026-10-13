// Package logging provides the named panel loggers (panel, audit, session,
// schedule, cmd_queue, commander, auth, server errors) with in-memory ring
// buffers (for the panel log viewers) and rotating file output.
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry is one log line.
type Entry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// Logger is a named logger with a bounded ring buffer and a file sink.
type Logger struct {
	mu       sync.Mutex
	name     string
	buf      []Entry
	maxLines int
	file     *os.File
	// Stdout mirrors to console (legacy mirrored only the panel logger; we mirror all at debug).
	console bool
	subs    []chan Entry
}

// Manager owns all named loggers.
type Manager struct {
	mu       sync.RWMutex
	loggers  map[string]*Logger
	dir      string
	maxLines int
}

// NewManager creates the logging manager and the on-disk log directory.
func NewManager(dir string, maxLines int) (*Manager, error) {
	if maxLines <= 0 {
		maxLines = 700
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Manager{loggers: map[string]*Logger{}, dir: dir, maxLines: maxLines}, nil
}

// LoggerNames lists the panel log viewers (parity with the legacy log types).
func (m *Manager) LoggerNames() []string {
	return []string{"audit", "session", "schedule", "cmd_queue", "commander", "auth", "server"}
}

// Get returns (creating if needed) the named logger.
func (m *Manager) Get(name string) *Logger {
	m.mu.RLock()
	l, ok := m.loggers[name]
	m.mu.RUnlock()
	if ok {
		return l
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.loggers[name]; ok {
		return l
	}
	l = &Logger{name: name, maxLines: m.maxLines, buf: make([]Entry, 0, m.maxLines)}
	if m.dir != "" {
		path := filepath.Join(m.dir, "panel-"+name+".log")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			l.file = f
		}
	}
	m.loggers[name] = l
	return l
}

// Log writes one entry.
func (l *Logger) Log(level, msg string) {
	e := Entry{Time: time.Now().UTC(), Level: level, Message: msg}
	l.mu.Lock()
	if len(l.buf) >= l.maxLines {
		l.buf = l.buf[1:]
	}
	l.buf = append(l.buf, e)
	if l.file != nil {
		fmt.Fprintf(l.file, "%s [%s] %s\n", e.Time.Format(time.RFC3339), level, msg)
	}
	subs := l.subs
	l.mu.Unlock()
	if l.console {
		fmt.Printf("[%s] %s: %s\n", strings.ToUpper(level), l.name, msg)
	}
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Info logs at info level.
func (l *Logger) Info(format string, a ...interface{}) { l.Log("info", fmt.Sprintf(format, a...)) }

// Warning logs at warning level.
func (l *Logger) Warning(format string, a ...interface{}) {
	l.Log("warning", fmt.Sprintf(format, a...))
}

// Error logs at error level.
func (l *Logger) Error(format string, a ...interface{}) { l.Log("error", fmt.Sprintf(format, a...)) }

// Debug logs at debug level.
func (l *Logger) Debug(format string, a ...interface{}) { l.Log("debug", fmt.Sprintf(format, a...)) }

// Critical logs at critical level.
func (l *Logger) Critical(format string, a ...interface{}) {
	l.Log("critical", fmt.Sprintf(format, a...))
}

// Tail returns up to n most recent entries.
func (l *Logger) Tail(n int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.buf) {
		n = len(l.buf)
	}
	out := make([]Entry, n)
	copy(out, l.buf[len(l.buf)-n:])
	return out
}

// Subscribe streams new entries to a channel (non-blocking delivery).
func (l *Logger) Subscribe() chan Entry {
	ch := make(chan Entry, 64)
	l.mu.Lock()
	l.subs = append(l.subs, ch)
	l.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscription.
func (l *Logger) Unsubscribe(ch chan Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, c := range l.subs {
		if c == ch {
			l.subs = append(l.subs[:i], l.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

// Audit writes an audit entry (also mirrored to the audit logger).
func (m *Manager) Audit(user string, userID int64, serverID, ip, msg string) {
	m.Get("audit").Log("info", fmt.Sprintf("user=%s server=%s ip=%s :: %s", user, serverID, ip, msg))
}
