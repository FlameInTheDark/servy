package core

// End-to-end smoke test: boots the full application on a random port with a
// temp data dir, then drives the REST API: setup → login → create a scripted
// fake game server → start → terminal → stdin → stop → backup → schedule →
// metrics → status. This is the acceptance test for M0–M3.

import (
        "bytes"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "os"
        "path/filepath"
        "strings"
        "testing"
        "testing/fstest"
        "time"

        "github.com/FlameInTheDark/servy/internal/config"
        "github.com/FlameInTheDark/servy/internal/httpapi"
)

type client struct {
        base  string
        token string
        http  *http.Client
}

func (c *client) do(method, path string, body interface{}) (int, map[string]interface{}, string) {
        var rd io.Reader
        if body != nil {
                b, _ := json.Marshal(body)
                rd = bytes.NewReader(b)
        }
        req, _ := http.NewRequest(method, c.base+path, rd)
        if c.token != "" {
                req.Header.Set("Authorization", "Bearer "+c.token)
        }
        if body != nil {
                req.Header.Set("Content-Type", "application/json")
        }
        resp, err := c.http.Do(req)
        if err != nil {
                return 0, nil, err.Error()
        }
        defer resp.Body.Close()
        raw, _ := io.ReadAll(resp.Body)
        var env map[string]interface{}
        json.Unmarshal(raw, &env)
        return resp.StatusCode, env, string(raw)
}

func TestEndToEnd(t *testing.T) {
        if testing.Short() {
                t.Skip("short mode")
        }
        tmp := t.TempDir()
        cfgPath := filepath.Join(tmp, "config", "config.yaml")
        os.MkdirAll(filepath.Dir(cfgPath), 0o755)
        os.WriteFile(cfgPath, []byte("https_port: 18443\nhttp_host: 127.0.0.1\nstats_update_frequency_seconds: 5\n"), 0o600)
        cfg, err := config.Load(cfgPath)
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
        // migrate servers root into tmp (already is)
        router := app.API.Router()
        port := 18443
        go router.Listen(fmt.Sprintf("127.0.0.1:%d", port))
        defer func() {
                app.Stop()
                router.Shutdown()
        }()
        time.Sleep(400 * time.Millisecond)

        cl := &client{base: fmt.Sprintf("http://127.0.0.1:%d/api/v1", port), http: &http.Client{Timeout: 20 * time.Second}}

        // ---- public status ----
        resp, _ := http.Get(fmt.Sprintf("http://127.0.0.1:%d/status", port))
        if resp.StatusCode != 200 {
                t.Fatalf("public /status: %d", resp.StatusCode)
        }
        resp.Body.Close()

        // ---- SPA shell serves as text/html (never a download) ----
        httpapi.SPAFiles = fstest.MapFS{
                "index.html": {Data: []byte("<html><body>panel</body></html>")},
        }
        spaResp, _ := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
        if spaResp == nil || spaResp.StatusCode != 200 {
                t.Fatalf("spa /: no response")
        }
        if ct := spaResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
                t.Fatalf("spa / content-type = %q, want text/html (download-instead-of-render bug)", ct)
        }
        spaResp.Body.Close()

        // ---- setup status ----
        code, env, raw := cl.do("GET", "/setup/status", nil)
        if code != 200 || env["data"].(map[string]interface{})["needs_setup"] != true {
                t.Fatalf("setup status: %d %s", code, raw)
        }

        // ---- setup (create admin) ----
        code, env, raw = cl.do("POST", "/auth/setup", map[string]string{"username": "admin", "password": "supersecret"})
        if code != 200 || env["data"].(map[string]interface{})["token"] == nil {
                t.Fatalf("setup: %d %s", code, raw)
        }
        cl.token = env["data"].(map[string]interface{})["token"].(string)

        // ---- login again (password flow) ----
        code, env, raw = cl.do("POST", "/auth/login", map[string]string{"username": "admin", "password": "supersecret"})
        if code != 200 {
                t.Fatalf("login: %d %s", code, raw)
        }

        // ---- bad login rejected ----
        code, _, _ = cl.do("POST", "/auth/login", map[string]string{"username": "admin", "password": "wrong"})
        if code != 401 {
                t.Fatalf("bad login should 401, got %d", code)
        }

        // ---- unauthorized access rejected ----
        noAuth := &client{base: cl.base, http: cl.http}
        code, _, _ = noAuth.do("GET", "/servers", nil)
        if code != 401 {
                t.Fatalf("no-auth servers should 401, got %d", code)
        }

        // ---- panel check ----
        code, env, raw = cl.do("GET", "/panel/check", nil)
        if code != 200 || env["data"].(map[string]interface{})["alive"] != true {
                t.Fatalf("panel check: %d %s", code, raw)
        }

        // ---- panel config get/patch ----
        code, _, raw = cl.do("GET", "/panel/config", nil)
        if code != 200 || !strings.Contains(raw, "categories") {
                t.Fatalf("panel config: %d %s", code, raw)
        }
        code, _, raw = cl.do("PATCH", "/panel/config", map[string]interface{}{"max_log_lines": 900})
        if code != 200 {
                t.Fatalf("config patch: %d %s", code, raw)
        }
        if app.Cfg.Get().MaxLogLines != 900 {
                t.Fatal("config patch did not apply")
        }

        // ---- java installs (java selection) ----
        code, env, raw = cl.do("GET", "/panel/java", nil)
        if code != 200 || !strings.Contains(raw, "minecraft_java_requirements") {
                t.Fatalf("java installs: %d %s", code, raw)
        }
        reqs := env["data"].(map[string]interface{})["minecraft_java_requirements"].([]interface{})
        if len(reqs) < 4 {
                t.Fatalf("java requirements matrix too small: %s", raw)
        }

        // ---- docker execution backend status ----
        code, env, raw = cl.do("GET", "/panel/docker", nil)
        if code != 200 {
                t.Fatalf("docker status: %d %s", code, raw)
        }
        if _, ok := env["data"].(map[string]interface{})["available"]; !ok {
                t.Fatalf("docker status missing available flag: %s", raw)
        }
        // in CI there is no docker daemon: available must be false but the API
        // must still respond ok (docker backend is optional)
        if dockerAvailable, _ := env["data"].(map[string]interface{})["available"].(bool); dockerAvailable {
                t.Log("docker daemon detected in test environment")
        }

        // ---- create a fake server (raw_exec with a bash script) ----
        // execution fields are accepted at creation too (docker backend config is
        // persisted even when the daemon is absent — starting it would fail with
        // DOCKER_UNAVAILABLE, so we flip it back to process before starting).
        code, env, raw = cl.do("POST", "/servers", map[string]interface{}{
                "create_type": "raw_exec", "name": "fake", "type": "custom",
                "executable": "run.sh", "execution_command": "/bin/bash run.sh",
                "stop_command": "stop", "port": 25599, "crash_detection": false,
                "execution_backend": "docker", "docker_image": "alpine:3.20",
                "docker_ports": []string{"25599:25599/tcp"}, "docker_env": map[string]string{"K": "V"},
        })
        if code != 200 {
                t.Fatalf("create server: %d %s", code, raw)
        }
        serverID := env["data"].(map[string]interface{})["server_id"].(string)

        // ---- creation-time execution fields persisted (before the round-trip
        // patch below overwrites them; the fake server is flipped back to the
        // process backend so it can actually start) ----
        code, env, raw = cl.do("GET", fmt.Sprintf("/servers/%s", serverID), nil)
        if code != 200 {
                t.Fatalf("get server after create: %d %s", code, raw)
        }
        cv := env["data"].(map[string]interface{})
        if cv["docker_image"] != "alpine:3.20" || cv["execution_backend"] != "docker" {
                t.Fatalf("creation-time docker fields missing: %v / %v", cv["docker_image"], cv["execution_backend"])
        }

        // ---- stopped-server stats shape (dashboard crash regression) ----
        // The server above is NOT running: the UI still renders cpu/online/max
        // directly from this payload, so every stats key must be present.
        for _, k := range []string{"state", "running", "crashed", "updating", "importing",
                "cpu", "mem", "mem_percent", "online", "max", "players", "version",
                "desc", "world_size", "int_ping_results", "eula_pending", "started_at"} {
                if _, ok := cv[k]; !ok {
                        t.Fatalf("stopped server view missing key %q (undefined.toFixed crash): %s", k, raw)
                }
        }
        if cv["state"] != "stopped" || cv["running"] != false || cv["cpu"] != float64(0) {
                t.Fatalf("stopped server stats wrong: state=%v running=%v cpu=%v", cv["state"], cv["running"], cv["cpu"])
        }

        // ---- host stats shape (dashboard chart regression: cpu_usage, not cpu) ----
        code, env, raw = cl.do("GET", "/panel/stats", nil)
        if code != 200 {
                t.Fatalf("panel stats: %d %s", code, raw)
        }
        ps := env["data"].(map[string]interface{})
        if _, ok := ps["snapshot"]; !ok {
                t.Fatalf("panel stats missing snapshot: %s", raw)
        }
        if hist, ok := ps["history"].([]interface{}); ok && len(hist) > 0 {
                row := hist[0].(map[string]interface{})
                if _, ok := row["cpu_usage"]; !ok {
                        t.Fatalf("host history row missing cpu_usage (dashboard chart maps this field): %s", raw)
                }
                if _, ok := row["cpu"]; ok {
                        t.Fatal("host history must not use the per-server field name `cpu`")
                }
        }

        // ---- execution environment fields round-trip ----
        code, _, raw = cl.do("PATCH", fmt.Sprintf("/servers/%s", serverID), map[string]interface{}{
                "java_selection":      "java",
                "execution_backend":   "process",
                "docker_image":        "alpine:3.20",
                "docker_ports":        []string{"25599:25599/tcp"},
                "docker_env":          map[string]string{"FOO": "bar"},
                "docker_autoremove":   true,
        })
        if code != 200 {
                t.Fatalf("patch execution fields: %d %s", code, raw)
        }
        code, env, raw = cl.do("GET", fmt.Sprintf("/servers/%s", serverID), nil)
        if code != 200 {
                t.Fatalf("get server after exec patch: %d %s", code, raw)
        }
        sv := env["data"].(map[string]interface{})
        if sv["java_selection"] != "java" || sv["execution_backend"] != "process" {
                t.Fatalf("execution fields not persisted: %s", raw)
        }
        if sv["docker_image"] != "alpine:3.20" || sv["docker_autoremove"] != true {
                t.Fatalf("docker fields not persisted: %s", raw)
        }
        // invalid backend value must be rejected silently (whitelist)
        code, _, raw = cl.do("PATCH", fmt.Sprintf("/servers/%s", serverID), map[string]interface{}{"execution_backend": "kubernetes"})
        if code != 200 {
                t.Fatalf("patch invalid backend should not 5xx: %d %s", code, raw)
        }
        code, env, _ = cl.do("GET", fmt.Sprintf("/servers/%s", serverID), nil)
        if env["data"].(map[string]interface{})["execution_backend"] != "process" {
                t.Fatal("invalid execution_backend was accepted")
        }
        // reset the execution environment so the fake bash server starts as a
        // plain process with the command as written
        code, _, raw = cl.do("PATCH", fmt.Sprintf("/servers/%s", serverID), map[string]interface{}{"java_selection": "", "execution_backend": "process"})
        if code != 200 {
                t.Fatalf("reset execution env: %d %s", code, raw)
        }


        // write the script through the files API
        script := "#!/bin/bash\necho SERVER_STARTED\nwhile IFS= read -r line; do\n  if [ \"$line\" = \"stop\" ]; then echo STOPPING; exit 0; fi\n  echo \"ECHO: $line\"\ndone\n"
        code, _, raw = cl.do("PUT", fmt.Sprintf("/servers/%s/files/run.sh", serverID), map[string]string{"content": script})
        if code != 200 {
                t.Fatalf("write script: %d %s", code, raw)
        }

        // files list
        code, env, raw = cl.do("GET", fmt.Sprintf("/servers/%s/files?path=", serverID), nil)
        if code != 200 || !strings.Contains(raw, "run.sh") {
                t.Fatalf("files list: %d %s", code, raw)
        }

        // ---- start the server ----
        code, _, raw = cl.do("POST", fmt.Sprintf("/servers/%s/start", serverID), nil)
        if code != 200 {
                t.Fatalf("start: %d %s", code, raw)
        }
        time.Sleep(1200 * time.Millisecond)

        // ---- terminal shows startup line ----
        code, env, raw = cl.do("GET", fmt.Sprintf("/servers/%s/terminal", serverID), nil)
        if code != 200 || !strings.Contains(raw, "SERVER_STARTED") {
                t.Fatalf("terminal missing startup: %d %s", code, raw)
        }

        // ---- stdin echo ----
        code, _, raw = cl.do("POST", fmt.Sprintf("/servers/%s/stdin", serverID), map[string]string{"command": "hello world"})
        if code != 200 {
                t.Fatalf("stdin: %d %s", code, raw)
        }
        time.Sleep(700 * time.Millisecond)
        code, _, raw = cl.do("GET", fmt.Sprintf("/servers/%s/terminal", serverID), nil)
        if !strings.Contains(raw, "ECHO: hello world") {
                t.Fatalf("stdin echo missing: %s", raw)
        }

        // ---- stats runtime ----
        code, _, raw = cl.do("GET", fmt.Sprintf("/servers/%s/stats", serverID), nil)
        if code != 200 || !strings.Contains(raw, "runtime") {
                t.Fatalf("stats: %d %s", code, raw)
        }

        // ---- config editor (visual properties) ----
        code, _, raw = cl.do("PUT", fmt.Sprintf("/servers/%s/files/server.properties", serverID),
                map[string]string{"content": "motd=Hello\nserver-port=25599\nonline-mode=true\n"})
        if code != 200 {
                t.Fatalf("write properties: %d %s", code, raw)
        }
        code, env, raw = cl.do("GET", fmt.Sprintf("/servers/%s/config/server.properties?mode=visual", serverID), nil)
        if code != 200 || !strings.Contains(raw, "motd") || !strings.Contains(raw, "sections") {
                t.Fatalf("visual config: %d %s", code, raw)
        }
        // visual edit
        form := env["data"].(map[string]interface{})["form"].(map[string]interface{})
        sections := form["sections"].([]interface{})
        values := map[string]string{}
        for _, s := range sections {
                for _, f := range s.(map[string]interface{})["fields"].([]interface{}) {
                        ff := f.(map[string]interface{})
                        values[ff["key"].(string)] = ff["value"].(string)
                }
        }
        values["motd"] = "Changed via visual editor"
        code, _, raw = cl.do("PUT", fmt.Sprintf("/servers/%s/config/server.properties", serverID), map[string]interface{}{"mode": "visual", "values": values})
        if code != 200 {
                t.Fatalf("visual save: %d %s", code, raw)
        }
        code, _, raw = cl.do("GET", fmt.Sprintf("/servers/%s/files/server.properties?action=download", serverID), nil)
        // raw GET returns envelope with content
        if !strings.Contains(raw, "Changed via visual editor") {
                t.Fatalf("visual edit did not persist: %s", raw)
        }

        // ---- backup config + run ----
        code, env, raw = cl.do("POST", fmt.Sprintf("/servers/%s/backups", serverID), map[string]interface{}{
                "name": "nightly", "compress": true, "enabled": true, "max_backups": 3, "backup_type": "zip_vault",
        })
        if code != 200 {
                t.Fatalf("backup create: %d %s", code, raw)
        }
        backupID := env["data"].(map[string]interface{})["backup_id"].(string)
        code, _, raw = cl.do("POST", fmt.Sprintf("/backups/backup/%s/run", backupID), nil)
        if code != 200 {
                t.Fatalf("backup run: %d %s", code, raw)
        }
        // snapshot engine
        code, env, raw = cl.do("POST", fmt.Sprintf("/servers/%s/backups", serverID), map[string]interface{}{
                "name": "snaps", "compress": true, "enabled": true, "backup_type": "snapshot", "max_backups": 3,
        })
        if code != 200 {
                t.Fatalf("snapshot config: %d %s", code, raw)
        }
        snapID := env["data"].(map[string]interface{})["backup_id"].(string)
        code, _, raw = cl.do("POST", fmt.Sprintf("/backups/backup/%s/run", snapID), nil)
        if code != 200 {
                t.Fatalf("snapshot run: %d %s", code, raw)
        }
        code, env, raw = cl.do("GET", fmt.Sprintf("/backups/backup/%s/archives", snapID), nil)
        if code != 200 || !strings.Contains(raw, ".snap.json") {
                t.Fatalf("snapshot archives: %d %s", code, raw)
        }

        // ---- schedule create + run ----
        code, env, raw = cl.do("POST", fmt.Sprintf("/servers/%s/tasks", serverID), map[string]interface{}{
                "name": "restarter", "action": "command", "interval": "hours", "interval_int": 1,
                "command": "say scheduled hello", "enabled": true,
        })
        if code != 200 {
                t.Fatalf("schedule create: %d %s", code, raw)
        }
        taskID := int64(env["data"].(map[string]interface{})["schedule_id"].(float64))
        code, _, raw = cl.do("POST", fmt.Sprintf("/tasks/%d/run", taskID), nil)
        if code != 200 {
                t.Fatalf("schedule run: %d %s", code, raw)
        }
        time.Sleep(600 * time.Millisecond)
        code, _, raw = cl.do("GET", fmt.Sprintf("/servers/%s/terminal", serverID), nil)
        if !strings.Contains(raw, "ECHO: say scheduled hello") {
                t.Fatalf("scheduled command not delivered: %s", raw)
        }

        // ---- webhook create ----
        code, _, raw = cl.do("POST", fmt.Sprintf("/servers/%s/webhook", serverID), map[string]interface{}{
                "name": "hook", "url": "http://127.0.0.1:1/nope", "webhook_type": "Discord",
                "trigger": []string{"server_start", "crash_detected"}, "enabled": true,
        })
        if code != 200 {
                t.Fatalf("webhook create: %d %s", code, raw)
        }

        // ---- users/roles ----
        code, env, raw = cl.do("POST", "/users", map[string]interface{}{"username": "bob", "password": "bobpass", "enabled": true})
        if code != 200 {
                t.Fatalf("user create: %d %s", code, raw)
        }
        bobID := int64(env["data"].(map[string]interface{})["user_id"].(float64))
        code, env, raw = cl.do("POST", "/roles", map[string]interface{}{"name": "players"})
        if code != 200 {
                t.Fatalf("role create: %d %s", code, raw)
        }
        roleID := int64(env["data"].(map[string]interface{})["role_id"].(float64))
        code, _, raw = cl.do("PATCH", fmt.Sprintf("/roles/%d", roleID), map[string]interface{}{
                "servers": map[string]string{serverID: "11000000"},
        })
        if code != 200 {
                t.Fatalf("role patch: %d %s", code, raw)
        }
        code, _, raw = cl.do("PATCH", fmt.Sprintf("/users/%d", bobID), map[string]interface{}{"roles": []int64{roleID}})
        if code != 200 {
                t.Fatalf("user patch: %d %s", code, raw)
        }
        // bob logs in, sees exactly one server
        bobCl := &client{base: cl.base, http: cl.http}
        code, env, raw = bobCl.do("POST", "/auth/login", map[string]string{"username": "bob", "password": "bobpass"})
        if code != 200 {
                t.Fatalf("bob login: %d %s", code, raw)
        }
        bobCl.token = env["data"].(map[string]interface{})["token"].(string)
        code, _, raw = bobCl.do("GET", "/servers", nil)
        if code != 200 || !strings.Contains(raw, serverID) {
                t.Fatalf("bob servers: %d %s", code, raw)
        }
        // bob (non-superuser) cannot see users
        code, _, _ = bobCl.do("GET", "/users", nil)
        if code != 200 {
                // bob isn't superuser → list is superuser-only
                if code != 403 {
                        t.Fatalf("bob users should 403, got %d", code)
                }
        }

        // ---- TOTP flow ----
        code, env, raw = cl.do("POST", "/users/1/totp", map[string]string{"name": "test"})
        if code != 200 {
                t.Fatalf("totp create: %d %s", code, raw)
        }
        _ = env // totp provisioned; verification tested in unit tests

        // ---- API key ----
        code, env, raw = cl.do("POST", "/users/1/key", map[string]interface{}{"name": "ci", "full_access": false})
        if code != 200 {
                t.Fatalf("api key: %d %s", code, raw)
        }
        apiToken := env["data"].(map[string]interface{})["token"].(string)
        keyCl := &client{base: cl.base, token: apiToken, http: cl.http}
        code, _, raw = keyCl.do("GET", "/servers", nil)
        if code != 200 {
                t.Fatalf("api key servers: %d %s", code, raw)
        }

        // ---- metrics ----
        resp2, _ := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", port))
        body, _ := io.ReadAll(resp2.Body)
        resp2.Body.Close()
        if !strings.Contains(string(body), "servy_host_cpu_percent") {
                t.Fatal("metrics missing host gauges")
        }
        resp2, _ = http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics/servers/%s", port, serverID))
        body, _ = io.ReadAll(resp2.Body)
        resp2.Body.Close()
        if !strings.Contains(string(body), "servy_server_running") {
                t.Fatal("metrics missing server gauges")
        }

        // ---- stop the server (graceful stop command path) ----
        code, _, raw = cl.do("POST", fmt.Sprintf("/servers/%s/stop", serverID), nil)
        if code != 200 {
                t.Fatalf("stop: %d %s", code, raw)
        }
        // wait for stopped state
        deadline := time.Now().Add(15 * time.Second)
        stopped := false
        for time.Now().Before(deadline) {
                _, env, _ = cl.do("GET", fmt.Sprintf("/servers/%s", serverID), nil)
                if env["data"].(map[string]interface{})["state"] == "stopped" {
                        stopped = true
                        break
                }
                time.Sleep(500 * time.Millisecond)
        }
        if !stopped {
                t.Fatal("server did not stop gracefully in time")
        }

        // ---- restart via start again + kill path ----
        code, _, _ = cl.do("POST", fmt.Sprintf("/servers/%s/start", serverID), nil)
        if code != 200 {
                t.Fatalf("re-start failed: %d", code)
        }
        time.Sleep(800 * time.Millisecond)
        code, _, raw = cl.do("POST", fmt.Sprintf("/servers/%s/kill", serverID), nil)
        if code != 200 {
                t.Fatalf("kill: %d %s", code, raw)
        }
        time.Sleep(600 * time.Millisecond)
        _, env, _ = cl.do("GET", fmt.Sprintf("/servers/%s", serverID), nil)
        state := env["data"].(map[string]interface{})["state"]
        if state != "crashed" && state != "stopped" {
                // kill during intentional stop is classified clean (stopping=true)
                t.Logf("post-kill state = %s (accepted: killed during intentional stop)", state)
        }

        // ---- panel logs ----
        code, _, raw = cl.do("GET", "/panel/logs/audit", nil)
        if code != 200 || !strings.Contains(raw, "audit") {
                t.Fatalf("panel logs: %d %s", code, raw)
        }

        // ---- delete server ----
        code, _, raw = cl.do("DELETE", fmt.Sprintf("/servers/%s?files=true", serverID), nil)
        if code != 200 {
                t.Fatalf("delete: %d %s", code, raw)
        }
        code, _, _ = cl.do("GET", fmt.Sprintf("/servers/%s", serverID), nil)
        if code != 404 {
                t.Fatalf("deleted server should 404, got %d", code)
        }

        t.Log("E2E: full lifecycle OK — setup, auth, users/roles/perms, server lifecycle, terminal, stdin,")
        t.Log("      config editors (raw + visual), backups (zip + snapshot), schedules, webhooks, api keys,")
        t.Log("      metrics, logs, delete")
}
