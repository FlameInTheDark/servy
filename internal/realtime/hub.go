// Package realtime implements the typed WebSocket hub: clients authenticate
// with their JWT, subscribe to topics (server.<id>, host, notifications) and
// receive permission-filtered events from the bus.
package realtime

import (
        "encoding/json"
        "strings"
        "sync"

        "github.com/fasthttp/websocket"

        "github.com/FlameInTheDark/servy/internal/auth"
        "github.com/FlameInTheDark/servy/internal/authz"
        "github.com/FlameInTheDark/servy/internal/events"
        "github.com/FlameInTheDark/servy/internal/logging"
)

// wsMessage is the wire protocol envelope.
type wsMessage struct {
        Type  string      `json:"type"` // subscribe | unsubscribe | event | pong
        Topic string      `json:"topic,omitempty"`
        Event string      `json:"event,omitempty"`
        Data  interface{} `json:"data,omitempty"`
}

// client is one connected socket.
type client struct {
        conn      *websocket.Conn
        userID    int64
        principal *authz.Principal
        mu        sync.Mutex
        topics    map[string]bool
        send      chan wsMessage
}

// Hub manages clients and bus subscriptions.
type Hub struct {
        auth *auth.Service
        log  *logging.Manager
        bus  *events.Bus

        mu      sync.Mutex
        clients map[*client]bool

        stopOnce sync.Once
}

// NewHub builds the hub and starts the bus pump.
func NewHub(authSvc *auth.Service, log *logging.Manager, bus *events.Bus) *Hub {
        h := &Hub{auth: authSvc, log: log, bus: bus, clients: map[*client]bool{}}
        ch, cancel := bus.Subscribe("*")
        go func() {
                for ev := range ch {
                        h.broadcast(ev)
                }
                cancel()
        }()
        return h
}

// HandleWS is the websocket endpoint (mounted after the auth middleware; the
// resolved principal is passed in by the Fiber handler that owns the upgrade).
func (h *Hub) HandleWS(c *websocket.Conn, principal *authz.Principal) {
        cl := &client{
                conn: c, userID: principal.UserID, principal: principal,
                topics: map[string]bool{}, send: make(chan wsMessage, 256),
        }

        // writer goroutine
        done := make(chan struct{})
        go func() {
                defer close(done)
                for msg := range cl.send {
                        data, err := json.Marshal(msg)
                        if err != nil {
                                continue
                        }
                        cl.mu.Lock()
                        err = c.WriteMessage(websocket.TextMessage, data)
                        cl.mu.Unlock()
                        if err != nil {
                                return
                        }
                }
        }()

        h.mu.Lock()
        h.clients[cl] = true
        h.mu.Unlock()
        // Single teardown path, ordered so a send on a closed channel is
        // impossible: the client is removed from the hub map under h.mu and
        // only then is its channel closed. broadcast() fans out under the
        // same lock, so once unregistered no sender can reach cl.send.
        defer func() {
                h.mu.Lock()
                delete(h.clients, cl)
                h.mu.Unlock()
                close(cl.send)
                <-done
                c.Close()
        }()

        for {
                _, data, err := c.ReadMessage()
                if err != nil {
                        return
                }
                var msg wsMessage
                if err := json.Unmarshal(data, &msg); err != nil {
                        continue
                }
                switch msg.Type {
                case "subscribe":
                        if h.canSubscribe(cl, msg.Topic) {
                                cl.mu.Lock()
                                cl.topics[msg.Topic] = true
                                cl.mu.Unlock()
                                cl.send <- wsMessage{Type: "event", Topic: msg.Topic, Event: "subscribed", Data: map[string]string{"topic": msg.Topic}}
                        } else {
                                cl.send <- wsMessage{Type: "event", Topic: msg.Topic, Event: "error", Data: map[string]string{"error": "NOT_AUTHORIZED"}}
                        }
                case "unsubscribe":
                        cl.mu.Lock()
                        delete(cl.topics, msg.Topic)
                        cl.mu.Unlock()
                case "ping":
                        cl.send <- wsMessage{Type: "pong"}
                }
        }
}

// canSubscribe enforces topic permissions.
func (h *Hub) canSubscribe(cl *client, topic string) bool {
        switch {
        case topic == "host":
                return true
        case topic == "notifications":
                return true
        case strings.HasPrefix(topic, "server."):
                serverID := strings.TrimSuffix(strings.TrimPrefix(topic, "server."), ".terminal")
                if !cl.principal.HasServerAccess(serverID) {
                        return false
                }
                if strings.HasSuffix(topic, ".terminal") {
                        // terminal stream requires the TERMINAL permission (parity)
                        return cl.principal.CanServer(serverID, authz.ServerTerminal)
                }
                return true
        }
        return false
}

// broadcast fans an event out to subscribed, authorized clients. It holds
// h.mu for the whole fan-out: clients are unregistered (and their channel
// closed) under the same lock, which guarantees no send ever targets a
// closed channel. Sends are non-blocking, so holding the lock is safe.
func (h *Hub) broadcast(ev events.Event) {
        h.mu.Lock()
        defer h.mu.Unlock()
        msg := wsMessage{Type: "event", Topic: topicFor(ev), Event: ev.Type, Data: ev.Data}
        for cl := range h.clients {
                if !h.deliverTo(cl, ev) {
                        continue
                }
                select {
                case cl.send <- msg:
                default: // slow consumer — drop
                }
        }
}

// deliverTo checks topic match + per-event permission filtering.
// Called with h.mu held; topic reads take cl.mu because the reader
// goroutine mutates topics under the same mutex.
func (h *Hub) deliverTo(cl *client, ev events.Event) bool {
        if ev.ServerID != "" {
                topic := "server." + ev.ServerID
                if ev.Type == events.EvVtermNewLine {
                        topic = "server." + ev.ServerID + ".terminal"
                }
                cl.mu.Lock()
                subscribed := cl.topics[topic]
                cl.mu.Unlock()
                if !subscribed {
                        return false
                }
                // permission re-check per event type (parity with the legacy per-message checks)
                if ev.Type == events.EvVtermNewLine && !cl.principal.CanServer(ev.ServerID, authz.ServerTerminal) {
                        return false
                }
                return true
        }
        // panel-wide: host stats + notifications (user-scoped)
        if ev.Type == events.EvNotification {
                if m, ok := ev.Data.(map[string]interface{}); ok {
                        if uid, ok := m["user_id"].(int64); ok && uid != cl.userID {
                                return false
                        }
                }
        }
        cl.mu.Lock()
        ok := cl.topics["host"] || cl.topics["notifications"]
        cl.mu.Unlock()
        return ok
}

func topicFor(ev events.Event) string {
        if ev.ServerID != "" {
                if ev.Type == events.EvVtermNewLine {
                        return "server." + ev.ServerID + ".terminal"
                }
                return "server." + ev.ServerID
        }
        return "host"
}

// OnlineUsers counts connected clients per user (for presence features).
func (h *Hub) OnlineUsers() map[int64]int {
        h.mu.Lock()
        defer h.mu.Unlock()
        out := map[int64]int{}
        for cl := range h.clients {
                out[cl.userID]++
        }
        return out
}
