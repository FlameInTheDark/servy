package core

// WebSocket smoke test: authenticates, subscribes to a server topic and
// verifies terminal streaming end-to-end through the real /ws endpoint.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FlameInTheDark/servy/internal/config"
	"golang.org/x/net/websocket"
)

func TestWebSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	tmp := t.TempDir()
	cfgPath := tmp + "/config/config.yaml"
	mkdirAll("config", tmp)
	writeFile(cfgPath, []byte("https_port: 18444\nhttp_host: 127.0.0.1\nstats_update_frequency_seconds: 5\n"))
	cfg, err := loadCfg(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg, Options{DataDir: tmp})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	router := app.API.Router()
	go router.Listen("127.0.0.1:18444")
	defer func() {
		app.Stop()
		router.Shutdown()
	}()
	time.Sleep(300 * time.Millisecond)

	hc := timeoutClient()
	cl := &client{base: "http://127.0.0.1:18444/api/v1", http: &hc}
	code, env, raw := cl.do("POST", "/auth/setup", map[string]string{"username": "wsadmin", "password": "supersecret"})
	if code != 200 {
		t.Fatalf("setup: %s", raw)
	}
	token := env["data"].(map[string]interface{})["token"].(string)
	cl.token = token

	// create + script the fake server
	code, env, raw = cl.do("POST", "/servers", map[string]interface{}{
		"create_type": "raw_exec", "name": "wsserver", "type": "custom",
		"executable": "run.sh", "execution_command": "/bin/bash run.sh",
		"stop_command": "stop", "crash_detection": false,
	})
	if code != 200 {
		t.Fatalf("create: %s", raw)
	}
	serverID := env["data"].(map[string]interface{})["server_id"].(string)
	script := "#!/bin/bash\necho WS_ONLINE\nwhile IFS= read -r l; do [ \"$l\" = stop ] && exit 0; echo \"WS: $l\"; done\n"
	cl.do("PUT", fmt.Sprintf("/servers/%s/files/run.sh", serverID), map[string]string{"content": script})

	// connect WS with token
	wsURL := fmt.Sprintf("ws://127.0.0.1:18444/ws?token=%s", token)
	ws, err := websocket.Dial(wsURL, "", "http://127.0.0.1/")
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer ws.Close()

	send := func(m map[string]interface{}) {
		b, _ := json.Marshal(m)
		if err := websocket.Message.Send(ws, string(b)); err != nil {
			t.Fatalf("ws send: %v", err)
		}
	}
	// subscribe to terminal + server topics
	send(map[string]interface{}{"type": "subscribe", "topic": "server." + serverID + ".terminal"})
	send(map[string]interface{}{"type": "subscribe", "topic": "server." + serverID})

	// start server via REST
	code, _, raw = cl.do("POST", fmt.Sprintf("/servers/%s/start", serverID), nil)
	if code != 200 {
		t.Fatalf("start: %s", raw)
	}

	// expect: subscribed acks + vterm_new_line with WS_ONLINE
	deadline := time.Now().Add(10 * time.Second)
	gotOnline := false
	var events []string
	for time.Now().Before(deadline) && !gotOnline {
		ws.SetDeadline(deadline)
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			t.Fatalf("ws receive: %v (events so far: %v)", err, events)
		}
		var m map[string]interface{}
		json.Unmarshal([]byte(msg), &m)
		events = append(events, fmt.Sprint(m["event"]))
		if m["event"] == "vterm_new_line" {
			b, _ := json.Marshal(m["data"])
			if strings.Contains(string(b), "WS_ONLINE") {
				gotOnline = true
			}
		}
	}
	if !gotOnline {
		t.Fatalf("terminal stream never delivered WS_ONLINE; events: %v", events)
	}

	// stdin over REST should stream through WS too
	cl.do("POST", fmt.Sprintf("/servers/%s/stdin", serverID), map[string]string{"command": "ping"})
	gotEcho := false
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && !gotEcho {
		ws.SetDeadline(deadline)
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			break
		}
		if strings.Contains(msg, "WS: ping") {
			gotEcho = true
		}
	}
	if !gotEcho {
		t.Fatal("stdin echo not streamed via WS")
	}

	// superuser may subscribe to any topic — the ack proves the protocol works
	send(map[string]interface{}{"type": "subscribe", "topic": "server.nonexistent.terminal"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ws.SetDeadline(deadline)
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			t.Fatalf("expected ack, got: %v", err)
		}
		if strings.Contains(msg, "subscribed") && strings.Contains(msg, "nonexistent") {
			t.Log("WS protocol OK: topic subscription, terminal streaming, permission model (superuser allow-all)")
			return
		}
	}
	t.Fatal("subscription ack not received")
}

// tiny helpers to avoid importing os/filepath in two test files
func mkdirAll(rel, base string)                 { os.MkdirAll(base+"/"+rel, 0o755) }
func writeFile(p string, b []byte)              { os.WriteFile(p, b, 0o600) }
func loadCfg(p string) (*config.Manager, error) { return config.Load(p) }
func timeoutClient() http.Client                { return http.Client{Timeout: 20 * time.Second} }
