// Package webhooks dispatches legacy-style webhook notifications on server
// lifecycle events. Providers: Discord, Slack, Mattermost, Teams, Generic JSON.
package webhooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/FlameInTheDark/servy/internal/database"
	"github.com/FlameInTheDark/servy/internal/events"
	"github.com/FlameInTheDark/servy/internal/logging"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

// Default templates per event (legacy parity).
var defaultTemplates = map[string]string{
	events.EvServerStart:   "{{server_name}} has been started",
	events.EvServerStop:    "{{server_name}} has been stopped",
	events.EvServerRestart: "{{server_name}} has been restarted",
	events.EvServerKill:    "{{server_name}} has been killed",
	events.EvServerCrash:   "{{server_name}} has crashed!",
	events.EvSendCommand:   "Command: '{{command}}' was executed on {{server_name}}",
	events.EvServerBackup:  "A backup for {{server_name}} was completed",
	events.EvServerUpdate:  "An update was performed on {{server_name}}",
}

// defaultColors per event (Discord embed colors).
var defaultColors = map[string]string{
	events.EvServerStart:   "3065992",  // green
	events.EvServerStop:    "15158332", // red
	events.EvServerRestart: "15105570",
	events.EvServerKill:    "15158332",
	events.EvServerCrash:   "15158332",
	events.EvSendCommand:   "16776960",
	events.EvServerBackup:  "3447003",
	events.EvServerUpdate:  "3447003",
}

// Service dispatches webhooks.
type Service struct {
	db     *database.Queries
	log    *logging.Manager
	bus    *events.Bus
	client *http.Client
	stop   chan struct{}
	once   sync.Once
}

// New builds the webhook service and subscribes to the event bus.
func New(db *database.Queries, log *logging.Manager, bus *events.Bus) *Service {
	s := &Service{db: db, log: log, bus: bus, client: &http.Client{Timeout: 10 * time.Second}, stop: make(chan struct{})}
	return s
}

// Start subscribes to lifecycle events.
func (s *Service) Start() {
	ch, cancel := s.bus.Subscribe("*")
	go func() {
		defer cancel()
		for {
			select {
			case <-s.stop:
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if _, isLifecycle := defaultTemplates[ev.Type]; isLifecycle {
					s.dispatch(ev)
				}
			}
		}
	}()
}

// Stop shuts the service down.
func (s *Service) Stop() { s.once.Do(func() { close(s.stop) }) }

func (s *Service) dispatch(ev events.Event) {
	hooks, err := s.db.ListWebhooksByTrigger(ev.ServerID)
	if err != nil {
		return
	}
	for _, h := range hooks {
		if h.Trigger == nil || !containsTrigger(*h.Trigger, ev.Type) {
			continue
		}
		go s.send(h, ev)
	}
}

func containsTrigger(csv, event string) bool {
	for _, t := range strings.Split(csv, ",") {
		if strings.TrimSpace(t) == event {
			return true
		}
	}
	return false
}

// send renders the body template and posts to the provider.
func (s *Service) send(h gen.Webhook, ev events.Event) {
	serverName := ""
	if m, ok := ev.Data.(map[string]interface{}); ok {
		if n, ok := m["name"].(string); ok {
			serverName = n
		}
	}
	vars := map[string]string{
		"server_id":   h.ServerID,
		"server_name": serverName,
		"event":       ev.Type,
		"bot_name":    deref(h.BotName),
	}
	// enrich with event payload fields
	if m, ok := ev.Data.(map[string]interface{}); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				vars[k] = s
			}
		}
	}
	body := deref(h.Body)
	if body == "" {
		body = defaultTemplates[ev.Type]
	}
	rendered := renderTemplate(body, vars)
	color := deref(h.Color)
	if color == "" {
		color = defaultColors[ev.Type]
	}
	if color == "" {
		color = "3065992"
	}

	var payload interface{}
	switch h.WebhookType {
	case "Discord":
		payload = map[string]interface{}{
			"username": coalesce(deref(h.BotName), "Servy"),
			"embeds": []map[string]interface{}{{
				"title":       "Servy",
				"description": rendered,
				"color":       atoiSafe(color),
				"footer":      map[string]string{"text": "Servy"},
			}},
		}
	case "Slack", "Mattermost":
		payload = map[string]interface{}{
			"text":     rendered,
			"username": coalesce(deref(h.BotName), "Servy"),
		}
	case "Teams":
		payload = map[string]interface{}{
			"@type":    "MessageCard",
			"@context": "https://schema.org/extensions",
			"summary":  rendered,
			"title":    "Servy",
			"sections": []map[string]string{{"text": rendered}},
		}
	default: // Generic
		payload = map[string]interface{}{
			"event":        ev.Type,
			"server_id":    h.ServerID,
			"server_name":  serverName,
			messageField(): rendered,
			"timestamp":    time.Now().UTC().Format(time.RFC3339),
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	resp, err := s.client.Post(h.Url, "application/json", bytes.NewReader(b))
	if err != nil {
		s.log.Get("webhooks").Error("webhook %q (%s) failed: %v", h.Name, h.ServerID, err)
		return
	}
	resp.Body.Close()
	s.log.Get("webhooks").Info("webhook %q (%s) delivered: %s -> %s", h.Name, h.ServerID, ev.Type, h.Url)
}

func renderTemplate(tpl string, vars map[string]string) string {
	t, err := template.New("wh").Parse(tpl)
	if err != nil {
		return tpl
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return tpl
	}
	return buf.String()
}

func atoiSafe(s string) int {
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		v = v*10 + int(c-'0')
	}
	return v
}

func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func messageField() string { return "message" }

var _ = fmt.Sprintf
