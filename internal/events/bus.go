// Package events is the in-process pub/sub event bus. Domain services publish
// lifecycle events; the websocket hub, webhooks and stats recorder subscribe.
package events

import "sync"

// Event mirrors the legacy websocket/event catalogue.
type Event struct {
        // Type is the event name, e.g. server_start, vterm_new_line, notification.
        Type string
        // ServerID scopes the event to a server ("" = panel-wide).
        ServerID string
        // Data is JSON-marshallable payload.
        Data interface{}
}

// Topic for server-scoped events: "server.<id>", or "*" for everything.
func topicFor(serverID string) string {
        if serverID == "" {
                return "*"
        }
        return "server." + serverID
}

// Bus is a fan-out event bus with per-topic subscriber sets.
type Bus struct {
        mu   sync.RWMutex
        subs map[string]map[int]chan Event
        next int
}

// NewBus creates an empty bus.
func NewBus() *Bus {
        return &Bus{subs: map[string]map[int]chan Event{}}
}

// Subscribe registers a listener; topic "*" receives all events.
// The returned cancel func removes the subscription and closes the channel.
func (b *Bus) Subscribe(topic string) (<-chan Event, func()) {
        b.mu.Lock()
        defer b.mu.Unlock()
        id := b.next
        b.next++
        ch := make(chan Event, 128)
        if _, ok := b.subs[topic]; !ok {
                b.subs[topic] = map[int]chan Event{}
        }
        b.subs[topic][id] = ch
        cancel := func() {
                b.mu.Lock()
                defer b.mu.Unlock()
                if set, ok := b.subs[topic]; ok {
                        if c, ok := set[id]; ok {
                                delete(set, id)
                                close(c)
                        }
                        if len(set) == 0 {
                                delete(b.subs, topic)
                        }
                }
        }
        return ch, cancel
}

// Publish sends an event to all matching subscribers (topic + "*" listeners).
// Delivery is non-blocking: slow consumers drop events rather than block publishers.
//
// Sends happen INSIDE the RLock critical section on purpose: cancel() closes
// a subscriber channel under the write lock, and an RWMutex writer waits for
// all readers to drain. A publish that snapshotted channels and sent after
// releasing the lock could race with that close (send on closed channel =
// process panic). Since sends are non-blocking, holding RLock is safe.
func (b *Bus) Publish(e Event) {
        topics := []string{"*"}
        if e.ServerID != "" {
                topics = append(topics, topicFor(e.ServerID))
        }
        b.mu.RLock()
        defer b.mu.RUnlock()
        for _, t := range topics {
                for _, ch := range b.subs[t] {
                        select {
                        case ch <- e:
                        default:
                        }
                }
        }
}

// Event names (catalogue parity with legacy broadcasts + new Go events).
const (
        // Server lifecycle
        EvServerStart       = "server_start"        // webhook name: started server
        EvServerStop        = "server_stop"         // webhook: stopped server
        EvServerRestart     = "server_restart"      // webhook: restarted server
        EvServerKill        = "server_kill"         // webhook: killed server
        EvServerCrash       = "crash_detected"      // webhook: crash detected
        EvServerBackup      = "backup_server"       // webhook: backup completed
        EvServerUpdate      = "server_upgrade"      // webhook: server update finished
        EvSendCommand       = "send_command"        // webhook: command executed
        // WS pushes
        EvVtermNewLine      = "vterm_new_line"      // terminal line batch
        EvSendStartReload   = "send_start_reload"   // server list changed (start/stop/delete)
        EvSendEulaBootbox   = "send_eula_bootbox"   // EULA agreement required
        EvNotification      = "notification"        // toast payload
        EvPanelConfig       = "panel_config_update"
        EvHostStats         = "host_stats"
        EvServerStats       = "server_stats"
        EvBackupDone        = "backup_end"
        EvBackupStart       = "backup_start"
        EvSupportLogs       = "support_log_status"
        EvAnnouncement      = "announcement"
        EvSteamCmdOutput    = "steamcmd_output"     // SteamCMD installation output line
)

// PublishSimple publishes a panel-wide event with a payload.
func (b *Bus) PublishSimple(typ string, data interface{}) {
        b.Publish(Event{Type: typ, Data: data})
}
