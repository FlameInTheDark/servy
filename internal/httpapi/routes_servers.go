package httpapi

import (
        "encoding/json"
        "fmt"
        "os"
        "path/filepath"
        "strconv"
        "strings"

        "github.com/gofiber/fiber/v3"

        "github.com/FlameInTheDark/servy/internal/authz"
        "github.com/FlameInTheDark/servy/internal/configedit"
        "github.com/FlameInTheDark/servy/internal/events"
        "github.com/FlameInTheDark/servy/internal/files"
        "github.com/FlameInTheDark/servy/internal/models"
        "github.com/FlameInTheDark/servy/internal/repo/gen"
        "github.com/FlameInTheDark/servy/internal/servers"
)

func (a *App) mountServers(api fiber.Router) {
        api.Get("/servers", a.listServers)
        api.Post("/servers", a.createServer)
        api.Post("/servers/stop_all", a.stopAllServers)
        api.Get("/servers/status", a.serversStatus)

        api.Get("/servers/:id", a.getServer)
        api.Patch("/servers/:id", a.patchServer)
        api.Delete("/servers/:id", a.deleteServer)
        api.Patch("/servers/:id/update/config", a.patchServerConfig)

        api.Post("/servers/:id/start", a.serverStart)
        api.Post("/servers/:id/stop", a.serverStop)
        api.Post("/servers/:id/restart", a.serverRestart)
        api.Post("/servers/:id/kill", a.serverKill)
        api.Post("/servers/:id/stdin", a.serverStdin)
        api.Post("/servers/:id/agree_eula", a.serverAgreeEULA)
        api.Post("/servers/:id/update_executable", a.serverUpdateExecutable)
        api.Post("/servers/:id/clone", a.serverClone)

        api.Get("/servers/:id/stats", a.serverStats)
        api.Get("/servers/:id/history", a.serverHistory)
        api.Get("/servers/:id/public", a.serverPublic)
        api.Get("/servers/:id/logs", a.serverLogs)
        api.Get("/servers/:id/players", a.serverPlayers)
        api.Get("/servers/:id/terminal", a.serverTerminal)

        // config editor (visual + raw)
        api.Get("/servers/:id/config", a.serverConfigList)
        api.Get("/servers/:id/config/:file", a.serverConfigGet)
        api.Put("/servers/:id/config/:file", a.serverConfigPut)

        // import/upload handled under files group; node placement
        api.Get("/servers/:id/users", a.serverUsers)
}

// serverView is the API shape of a server.
func (a *App) serverView(def gen.Server, rt *models.ServerRuntime) map[string]interface{} {
        v := map[string]interface{}{
                "server_id":         def.ServerID,
                "created":           def.Created,
                "server_name":       def.ServerName,
                "type":              def.Type,
                "path":              def.Path,
                "executable":        def.Executable,
                "log_path":          def.LogPath,
                "execution_command": def.ExecutionCommand,
                "auto_start":        def.AutoStart == 1,
                "auto_start_delay":  def.AutoStartDelay,
                "crash_detection":   def.CrashDetection == 1,
                "stop_command":      def.StopCommand,
                "stop_action":       def.StopAction,
                "io_mode":           def.IoMode,
                "shutdown_timeout":  def.ShutdownTimeout,
                "server_ip":         def.ServerIp,
                "server_port":       def.ServerPort,
                "show_status":       def.ShowStatus == 1,
                "count_players":     def.CountPlayers == 1,
                "update_watcher":    def.UpdateWatcher == 1,
                "logs_delete_after": def.LogsDeleteAfter,
                "node_id":           "",
                "eula_pending":      false,
                // execution environment
                "java_selection":    def.JavaSelection,
                "shell":             def.Shell,
                "shell_default":     servers.DefaultShellID(),
                "execution_backend": def.ExecutionBackend,
                "docker_autoremove": def.DockerAutoremove == 1,
        }
        if def.DockerImage != nil {
                v["docker_image"] = *def.DockerImage
        }
        if def.DockerEnv != nil {
                v["docker_env"] = jsonMapOf(def.DockerEnv)
        }
        if def.DockerPorts != nil {
                v["docker_ports"] = jsonStringsOf(def.DockerPorts)
        }
        if def.DockerBinds != nil {
                v["docker_binds"] = jsonStringsOf(def.DockerBinds)
        }
        if def.DockerNetwork != nil {
                v["docker_network"] = *def.DockerNetwork
        }
        if def.DockerWorkdir != nil {
                v["docker_workdir"] = *def.DockerWorkdir
        }
        if def.NodeID != nil {
                v["node_id"] = *def.NodeID
        }
        if def.ExecutableUpdateUrl != nil {
                v["executable_update_url"] = *def.ExecutableUpdateUrl
        }
        if def.AppID != nil {
                v["app_id"] = *def.AppID
        }
        // Live stats block. ALWAYS emitted with zero defaults for stopped,
        // remote-managed or not-yet-registered servers: the UI renders these
        // fields directly (e.g. cpu.toFixed(1)) and a missing key crashes it.
        stats := models.ServerRuntime{State: models.StateStopped, Players: []string{}}
        if rt != nil {
                stats = *rt
        }
        if stats.Players == nil {
                stats.Players = []string{}
        }
        v["state"] = string(stats.State)
        v["running"] = stats.Running
        v["crashed"] = stats.Crashed
        v["updating"] = stats.Updating
        v["importing"] = stats.Importing
        v["cpu"] = stats.CPU
        v["mem"] = stats.Mem
        v["mem_percent"] = stats.MemPercent
        v["online"] = stats.Online
        v["max"] = stats.Max
        v["players"] = stats.Players
        v["version"] = stats.Version
        v["desc"] = stats.Desc
        v["world_size"] = stats.WorldSize
        v["int_ping_results"] = stats.PingMS
        v["eula_pending"] = stats.EULAPending
        if !stats.StartedAt.IsZero() {
                v["started_at"] = stats.StartedAt
        } else {
                v["started_at"] = nil // never started: explicit null (UI shows "—")
        }
        return v
}

// loadServer returns (sup, def, remoteAddr, errorResponse) — remote servers
// return a definition with a non-empty remoteAddr and nil sup.
func (a *App) loadServer(c fiber.Ctx, perm int) (*servers.Supervisor, gen.Server, string, bool) {
        p := a.requireAuth(c)
        if p == nil {
                return nil, gen.Server{}, "", false
        }
        serverID := c.Params("id")
        if !p.HasServerAccess(serverID) {
                _ = fail(c, 403, "NOT_AUTHORIZED")
                return nil, gen.Server{}, "", false
        }
        if perm >= 0 && !p.CanServer(serverID, perm) {
                _ = fail(c, 403, "NOT_AUTHORIZED")
                return nil, gen.Server{}, "", false
        }
        def, err := a.DB.GetServer(serverID)
        if err != nil {
                _ = fail(c, 404, "SERVER_NOT_FOUND")
                return nil, gen.Server{}, "", false
        }
        if addr := a.remoteNodeAddr(serverID); addr != "" {
                return nil, def, addr, true
        }
        sup, err := a.Registry.Get(serverID)
        if err != nil {
                _ = fail(c, 404, "SERVER_NOT_FOUND")
                return nil, gen.Server{}, "", false
        }
        return sup, def, "", true
}

func (a *App) listServers(c fiber.Ctx) error {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        ids := map[string]bool{}
        for _, id := range p.ServerIDs() {
                ids[id] = true
        }
        out := []map[string]interface{}{}
        serverRows, _ := a.DB.ListServers()
        for _, def := range serverRows {
                if !p.Superuser && !ids[def.ServerID] {
                        continue
                }
                var rt *models.ServerRuntime
                if sup, err := a.Registry.Get(def.ServerID); err == nil {
                        r := sup.Runtime()
                        rt = &r
                }
                out = append(out, a.serverView(def, rt))
        }
        // user's custom order
        if p != nil && !p.Superuser {
                if u, err := a.DB.GetUserByID(p.UserID); err == nil && u.ServerOrder != nil && *u.ServerOrder != "" {
                        order := strings.Split(*u.ServerOrder, ",")
                        sortByOrder(out, order)
                }
        }
        return ok(c, out)
}

func sortByOrder(items []map[string]interface{}, order []string) {
        pos := map[string]int{}
        for i, id := range order {
                pos[id] = i
        }
        for i := 1; i < len(items); i++ {
                for j := i; j > 0; j-- {
                        aID, _ := items[j-1]["server_id"].(string)
                        bID, _ := items[j]["server_id"].(string)
                        pa, aok := pos[aID]
                        pb, bok := pos[bID]
                        if !aok {
                                pa = 1 << 30
                        }
                        if !bok {
                                pb = 1 << 30
                        }
                        if pa <= pb {
                                break
                        }
                        items[j-1], items[j] = items[j], items[j-1]
                }
        }
}

func (a *App) createServer(c fiber.Ctx) error {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        if !p.CanPanel(authz.PanelServerCreation) {
                return fail(c, 403, "NOT_AUTHORIZED")
        }
        // quantity limit
        if !p.Superuser && p.LimitServers >= 0 {
                if int64(len(p.ServerIDs())) >= p.LimitServers {
                        return failMsg(c, 403, "LIMIT_REACHED", "server creation limit reached")
                }
        }
        var req creationRequest
        if err := parseBody(c, &req); err != nil {
                return err
        }
        cr := req.toCreationRequest()
        // optional node placement
        if req.Node != "" {
                node, err := a.DB.GetNode(req.Node)
                if err != nil {
                        return failMsg(c, 400, "INVALID_NODE", "unknown node")
                }
                // create on remote agent, then record locally
                def := req.toDef(a.ServersRoot)
                if err := a.Cluster.CreateServer(node.Address, def); err != nil {
                        return failMsg(c, 502, "NODE_ERROR", err.Error())
                }
                a.DB.SetServerNode(&node.NodeID, databaseNow(), def.ServerID)
                return ok(c, fiber.Map{"server_id": def.ServerID, "remote": true, "node": node.NodeID})
        }
        res, err := a.Creation.Create(cr, p.UserID)
        if err != nil {
                return failMsg(c, 400, err.Error(), err.Error())
        }
        return ok(c, res)
}

func (a *App) getServer(c fiber.Ctx) error {
        sup, def, addr, good := a.loadServer(c, -1)
        if !good {
                return nil
        }
        if addr != "" {
                st, err := a.Cluster.Status(addr, def.ServerID)
                if err == nil {
                        return ok(c, fiber.Map{
                                "server_id": def.ServerID, "server_name": def.ServerName, "type": def.Type,
                                "node_id": deref(def.NodeID), "remote": true,
                                "state": st.State, "cpu": st.Cpu, "mem": st.Mem, "online": st.Online, "max": st.Max,
                                "players": st.Players, "version": st.Version,
                        })
                }
                return ok(c, a.serverView(def, nil))
        }
        rt := sup.Runtime()
        return ok(c, a.serverView(def, &rt))
}

func (a *App) patchServer(c fiber.Ctx) error {
        return a.patchServerConfig(c)
}

func (a *App) patchServerConfig(c fiber.Ctx) error {
        sup, _, _, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        var patch map[string]interface{}
        if err := parseBody(c, &patch); err != nil {
                return err
        }
        if err := a.Registry.UpdateConfig(sup.Definition().ServerID, func(def *gen.Server) {
                applyServerPatch(def, patch)
        }); err != nil {
                return failMsg(c, 400, err.Error(), err.Error())
        }
        updated, _ := a.DB.GetServer(sup.Definition().ServerID)
        rt := sup.Runtime()
        return ok(c, a.serverView(updated, &rt))
}

func applyServerPatch(def *gen.Server, patch map[string]interface{}) {
        if v, ok := patch["server_name"].(string); ok && v != "" {
                def.ServerName = v
        }
        if v, ok := patch["executable"].(string); ok && v != "" {
                def.Executable = v
        }
        if v, ok := patch["log_path"].(string); ok {
                def.LogPath = v
        }
        if v, ok := patch["execution_command"].(string); ok && v != "" {
                def.ExecutionCommand = v
        }
        if v, ok := patch["stop_command"].(string); ok {
                def.StopCommand = v
        }
        if v, ok := patch["stop_action"].(string); ok {
                def.StopAction = v
        }
        if v, ok := patch["io_mode"].(string); ok {
                def.IoMode = v
        }
        if v, ok := patch["server_ip"].(string); ok {
                def.ServerIp = v
        }
        if v, ok := patch["type"].(string); ok && v != "" {
                def.Type = v
        }
        if v, ok := patch["executable_update_url"].(string); ok {
                def.ExecutableUpdateUrl = &v
        }
        if v, ok := patch["auto_start"]; ok {
                def.AutoStart = boolI(v)
        }
        if v, ok := patch["auto_start_delay"]; ok {
                def.AutoStartDelay = numI(v, def.AutoStartDelay)
        }
        if v, ok := patch["crash_detection"]; ok {
                def.CrashDetection = boolI(v)
        }
        if v, ok := patch["shutdown_timeout"]; ok {
                def.ShutdownTimeout = numI(v, def.ShutdownTimeout)
        }
        if v, ok := patch["server_port"]; ok {
                def.ServerPort = numI(v, def.ServerPort)
        }
        if v, ok := patch["logs_delete_after"]; ok {
                def.LogsDeleteAfter = numI(v, def.LogsDeleteAfter)
        }
        if v, ok := patch["show_status"]; ok {
                def.ShowStatus = boolI(v)
        }
        if v, ok := patch["count_players"]; ok {
                def.CountPlayers = boolI(v)
        }
        if v, ok := patch["update_watcher"]; ok {
                def.UpdateWatcher = boolI(v)
        }
        if v, ok := patch["ignored_exits"].(string); ok {
                def.IgnoredExits = &v
        }
        // --- execution environment ---
        if v, ok := patch["shell"].(string); ok && servers.ValidShellSelection(v) {
                def.Shell = v
        }
        if v, ok := patch["java_selection"].(string); ok {
                // legacy get_execution_java validation: "", "none", "java" or a
                // detected/absolute JVM path
                if v == "" || v == "none" || v == "java" || fileOrDirExists(v) {
                        def.JavaSelection = v
                }
        }
        if v, ok := patch["execution_backend"].(string); ok {
                if v == "process" || v == "docker" {
                        def.ExecutionBackend = v
                }
        }
        if v, ok := patch["docker_image"].(string); ok {
                def.DockerImage = &v
        }
        if v, ok := patch["docker_env"]; ok {
                if b, err := json.Marshal(toStringMap(v)); err == nil {
                        s := string(b)
                        def.DockerEnv = &s
                }
        }
        if v, ok := patch["docker_ports"]; ok {
                if b, err := json.Marshal(toStringSlice(v)); err == nil {
                        s := string(b)
                        def.DockerPorts = &s
                }
        }
        if v, ok := patch["docker_binds"]; ok {
                binds := []string{}
                for _, b := range toStringSlice(v) {
                        // host side must be absolute, no traversal
                        host := strings.SplitN(b, ":", 2)[0]
                        if filepath.IsAbs(host) && !strings.Contains(host, "..") {
                                binds = append(binds, b)
                        }
                }
                if b, err := json.Marshal(binds); err == nil {
                        s := string(b)
                        def.DockerBinds = &s
                }
        }
        if v, ok := patch["docker_network"].(string); ok {
                def.DockerNetwork = &v
        }
        if v, ok := patch["docker_workdir"].(string); ok {
                if v == "" || (strings.HasPrefix(v, "/") && !strings.Contains(v, "..")) {
                        def.DockerWorkdir = &v
                }
        }
        if v, ok := patch["docker_autoremove"]; ok {
                def.DockerAutoremove = boolI(v)
        }
}

func (a *App) deleteServer(c fiber.Ctx) error {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        if !p.Superuser && !p.CanPanel(0) && !p.CanServer(c.Params("id"), authz.ServerConfig) {
                return fail(c, 403, "NOT_AUTHORIZED")
        }
        serverID := c.Params("id")
        removeFiles := fiber.Query[bool](c, "files", false)
        if addr := a.remoteNodeAddr(serverID); addr != "" {
                if err := a.Cluster.DeleteServer(addr, serverID, removeFiles); err != nil {
                        return failMsg(c, 502, "NODE_ERROR", err.Error())
                }
                a.DB.DeleteServer(serverID)
                return ok(c, fiber.Map{"deleted": serverID})
        }
        if err := a.Creation.DeleteFlow(serverID, removeFiles, p.UserID); err != nil {
                return failMsg(c, 400, err.Error(), err.Error())
        }
        return ok(c, fiber.Map{"deleted": serverID})
}

// serverAction runs a lifecycle action with remote routing.
func (a *App) serverAction(c fiber.Ctx, perm int, local func(sup *servers.Supervisor, p fiber.Ctx) error, remote func(addr, id string) error) error {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        if !p.CanServer(c.Params("id"), perm) {
                return fail(c, 403, "NOT_AUTHORIZED")
        }
        serverID := c.Params("id")
        if addr := a.remoteNodeAddr(serverID); addr != "" {
                if err := remote(addr, serverID); err != nil {
                        return failMsg(c, 502, "NODE_ERROR", err.Error())
                }
                return ok(c, fiber.Map{"server_id": serverID, "action": "remote"})
        }
        sup, err := a.Registry.Get(serverID)
        if err != nil {
                return fail(c, 404, "SERVER_NOT_FOUND")
        }
        if err := local(sup, c); err != nil {
                return failMsg(c, 409, err.Error(), err.Error())
        }
        return ok(c, fiber.Map{"server_id": serverID})
}

func (a *App) serverStart(c fiber.Ctx) error {
        return a.serverAction(c, authz.ServerCommands, func(sup *servers.Supervisor, c fiber.Ctx) error {
                return sup.Start(userIDOf(c))
        }, a.Cluster.Start)
}

func (a *App) serverStop(c fiber.Ctx) error {
        return a.serverAction(c, authz.ServerCommands, func(sup *servers.Supervisor, c fiber.Ctx) error {
                return sup.Stop("api", userIDOf(c))
        }, a.Cluster.Stop)
}

func (a *App) serverRestart(c fiber.Ctx) error {
        return a.serverAction(c, authz.ServerCommands, func(sup *servers.Supervisor, c fiber.Ctx) error {
                return sup.Restart(userIDOf(c), nil)
        }, a.Cluster.Restart)
}

func (a *App) serverKill(c fiber.Ctx) error {
        return a.serverAction(c, authz.ServerCommands, func(sup *servers.Supervisor, c fiber.Ctx) error {
                return sup.Kill(userIDOf(c))
        }, a.Cluster.Kill)
}

func (a *App) serverStdin(c fiber.Ctx) error {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        serverID := c.Params("id")
        if !p.CanServer(serverID, authz.ServerCommands) {
                return fail(c, 403, "NOT_AUTHORIZED")
        }
        var req struct {
                Command string `json:"command"`
        }
        if err := parseBody(c, &req); err != nil {
                return err
        }
        if addr := a.remoteNodeAddr(serverID); addr != "" {
                if err := a.Cluster.SendCommand(addr, serverID, req.Command); err != nil {
                        return failMsg(c, 502, "NODE_ERROR", err.Error())
                }
                return ok(c, fiber.Map{"sent": req.Command})
        }
        sup, err := a.Registry.Get(serverID)
        if err != nil {
                return fail(c, 404, "SERVER_NOT_FOUND")
        }
        if err := sup.SendCommand(req.Command, p.UserID); err != nil {
                return failMsg(c, 409, err.Error(), err.Error())
        }
        return ok(c, fiber.Map{"sent": req.Command})
}

func (a *App) serverAgreeEULA(c fiber.Ctx) error {
        return a.serverAction(c, authz.ServerConfig, func(sup *servers.Supervisor, c fiber.Ctx) error {
                if err := sup.AgreeEULA(); err != nil {
                        return err
                }
                sup.SetRuntime(func(r *models.ServerRuntime) { r.EULAPending = false })
                return nil
        }, func(addr, id string) error { return nil })
}

func (a *App) serverUpdateExecutable(c fiber.Ctx) error {
        sup, _, _, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        runBackup := func(serverID string) error {
                if b, err := a.Backups.DefaultBackup(serverID); err == nil {
                        return a.Backups.Run(b.BackupID, 0)
                }
                return nil
        }
        if err := a.Updater.Update(sup.Definition().ServerID, userIDOf(c), runBackup); err != nil {
                return failMsg(c, 500, err.Error(), err.Error())
        }
        return ok(c, fiber.Map{"updated": true})
}

func (a *App) serverClone(c fiber.Ctx) error {
        sup, def, _, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        _ = sup
        newName := def.ServerName + "-clone"
        cr := creationRequest{
                CreateType: "raw_exec", Name: newName, ServerType: def.Type,
                Executable: def.Executable, ExecutionCommand: def.ExecutionCommand,
                StopCommand: def.StopCommand, Port: int(def.ServerPort),
        }
        res, err := a.Creation.Create(cr.toCreationRequest(), userIDOf(c))
        if err != nil {
                return failMsg(c, 400, err.Error(), err.Error())
        }
        // copy files
        if dstSup, err := a.Registry.Get(res.ServerID); err == nil {
                files.New().Copy(files.NewGuardOrPanic(def.Path), ".", dstSup.Definition().Path)
        }
        return ok(c, res)
}

func (a *App) stopAllServers(c fiber.Ctx) error {
        p := a.requireSuperuser(c)
        if p == nil {
                return nil
        }
        for _, sup := range a.Registry.All() {
                if sup.State() == models.StateRunning {
                        go sup.Stop("stop_all", p.UserID)
                }
        }
        return ok(c, fiber.Map{"stopping": "all"})
}

func (a *App) serversStatus(c fiber.Ctx) error {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        ids := map[string]bool{}
        for _, id := range p.ServerIDs() {
                ids[id] = true
        }
        out := map[string]interface{}{}
        for _, sup := range a.Registry.All() {
                if !p.Superuser && !ids[sup.Definition().ServerID] {
                        continue
                }
                def := sup.Definition()
                if def.ShowStatus == 0 {
                        continue
                }
                rt := sup.Runtime()
                out[def.ServerID] = fiber.Map{"running": rt.Running, "state": string(rt.State)}
        }
        return ok(c, out)
}

func (a *App) serverStats(c fiber.Ctx) error {
        sup, _, addr, good := a.loadServer(c, -1)
        if !good {
                return nil
        }
        if addr != "" {
                st, err := a.Cluster.Status(addr, c.Params("id"))
                if err == nil {
                        return ok(c, st)
                }
                return failMsg(c, 502, "NODE_ERROR", err.Error())
        }
        rt := sup.Runtime()
        latest := servers.LatestStat(a.depsForStats(), sup.Definition().ServerID)
        return ok(c, fiber.Map{"runtime": rt, "latest": latest})
}

func (a *App) serverHistory(c fiber.Ctx) error {
        sup, _, _, good := a.loadServer(c, -1)
        if !good {
                return nil
        }
        hours := fiber.Query[int](c, "hours", 24)
        points := servers.History(a.depsForStats(), sup.Definition().ServerID, hours)
        return ok(c, points)
}

func (a *App) serverPublic(c fiber.Ctx) error {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        serverID := c.Params("id")
        def, err := a.DB.GetServer(serverID)
        if err != nil || def.ShowStatus == 0 {
                return fail(c, 404, "SERVER_NOT_FOUND")
        }
        rt := models.ServerRuntime{}
        if sup, err := a.Registry.Get(serverID); err == nil {
                rt = sup.Runtime()
        }
        return ok(c, fiber.Map{
                "server_name": def.ServerName, "type": def.Type, "online": rt.Online,
                "max": rt.Max, "players": rt.Players, "version": rt.Version, "desc": rt.Desc,
                "running": rt.Running, "icon": "",
        })
}

func (a *App) serverLogs(c fiber.Ctx) error {
        sup, _, _, good := a.loadServer(c, authz.ServerLogs)
        if !good {
                return nil
        }
        // log file (if exists) else virtual terminal
        def := sup.Definition()
        logPath := filepath.Join(def.Path, def.LogPath)
        if data, err := files.New().Read(files.NewGuardOrPanic(def.Path), def.LogPath, 8<<20); err == nil {
                _ = logPath
                lines := strings.Split(string(data), "\n")
                if len(lines) > 700 {
                        lines = lines[len(lines)-700:]
                }
                return ok(c, fiber.Map{"source": "file", "lines": lines})
        }
        return ok(c, fiber.Map{"source": "terminal", "lines": sup.Terminal().Tail(0)})
}

func (a *App) serverTerminal(c fiber.Ctx) error {
        sup, _, addr, good := a.loadServer(c, authz.ServerTerminal)
        if !good {
                return nil
        }
        if addr != "" {
                lines, err := a.Cluster.Terminal(addr, c.Params("id"), fiber.Query[int](c, "lines", 70))
                if err != nil {
                        return failMsg(c, 502, "NODE_ERROR", err.Error())
                }
                return ok(c, lines)
        }
        return ok(c, sup.Terminal().Tail(fiber.Query[int](c, "lines", 0)))
}

func (a *App) serverPlayers(c fiber.Ctx) error {
        sup, _, _, good := a.loadServer(c, authz.ServerPlayers)
        if !good {
                return nil
        }
        rt := sup.Runtime()
        return ok(c, fiber.Map{
                "online": rt.Online, "max": rt.Max, "players": rt.Players,
        })
}

func (a *App) serverUsers(c fiber.Ctx) error {
        // list users with access to this server
        if a.requireAuth(c) == nil {
                return nil
        }
        serverID := c.Params("id")
        users, _ := a.DB.ListUsers()
        out := []map[string]interface{}{}
        for _, u := range users {
                p := a.Auth.PrincipalForUser(u)
                if p.HasServerAccess(serverID) {
                        mask := "1" // has access
                        if !p.Superuser {
                                mask = p.ServerMaskFor(serverID)
                        }
                        out = append(out, map[string]interface{}{"user_id": u.UserID, "username": u.Username, "permissions": mask})
                }
        }
        return ok(c, out)
}

// ---------------- config editor endpoints ----------------

func (a *App) serverConfigList(c fiber.Ctx) error {
        sup, _, _, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        def := sup.Definition()
        guard, _ := files.NewGuard(def.Path)
        entries, _ := a.Files.List(guard, "")
        known := []map[string]interface{}{}
        for _, e := range entries {
                n := strings.ToLower(e.Name)
                switch {
                case n == "server.properties":
                        known = append(known, map[string]interface{}{"file": e.Name, "mode": "visual+raw", "format": "properties"})
                case strings.HasSuffix(n, ".json"):
                        known = append(known, map[string]interface{}{"file": e.Name, "mode": "raw", "format": "json"})
                case strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml"):
                        known = append(known, map[string]interface{}{"file": e.Name, "mode": "raw", "format": "yaml"})
                case strings.HasSuffix(n, ".properties") || strings.HasSuffix(n, ".ini") || strings.HasSuffix(n, ".conf") || strings.HasSuffix(n, ".cfg"):
                        known = append(known, map[string]interface{}{"file": e.Name, "mode": "raw", "format": "properties"})
                case strings.HasSuffix(n, ".txt") || strings.HasSuffix(n, ".xml"):
                        known = append(known, map[string]interface{}{"file": e.Name, "mode": "raw", "format": "text"})
                }
        }
        return ok(c, known)
}

func (a *App) serverConfigGet(c fiber.Ctx) error {
        sup, _, _, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        def := sup.Definition()
        file := c.Params("file")
        if strings.Contains(file, "..") {
                return fail(c, 400, "TRAVERSAL DETECTED")
        }
        guard, _ := files.NewGuard(def.Path)
        data, err := a.Files.Read(guard, file, 16<<20)
        if err != nil {
                return fail(c, 404, "NOT_FOUND")
        }
        mode := c.Query("mode", "raw")
        n := strings.ToLower(file)
        if mode == "visual" && (n == "server.properties" || strings.HasSuffix(n, ".properties")) {
                doc := configedit.ParseProperties(string(data))
                form := configedit.BuildVisualForm(configedit.MCPropertiesSchema(), doc)
                return ok(c, fiber.Map{"file": file, "mode": "visual", "format": "properties", "form": form})
        }
        return ok(c, fiber.Map{"file": file, "mode": "raw", "content": string(data)})
}

func (a *App) serverConfigPut(c fiber.Ctx) error {
        sup, _, _, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        def := sup.Definition()
        file := c.Params("file")
        if strings.Contains(file, "..") {
                return fail(c, 400, "TRAVERSAL DETECTED")
        }
        var req struct {
                Mode    string            `json:"mode"`
                Content string            `json:"content"`
                Values  map[string]string `json:"values"`
        }
        if err := parseBody(c, &req); err != nil {
                return err
        }
        guard, _ := files.NewGuard(def.Path)
        n := strings.ToLower(file)
        switch req.Mode {
        case "visual":
                if !(n == "server.properties" || strings.HasSuffix(n, ".properties")) {
                        return failMsg(c, 400, "INVALID_MODE", "visual mode supports .properties files")
                }
                cur, err := a.Files.Read(guard, file, 16<<20)
                if err != nil {
                        return fail(c, 404, "NOT_FOUND")
                }
                doc := configedit.ParseProperties(string(cur))
                configedit.ApplyVisualForm(doc, req.Values)
                if err := a.Files.Write(guard, file, []byte(configedit.RenderProperties(doc))); err != nil {
                        return failMsg(c, 500, err.Error(), err.Error())
                }
                return ok(c, fiber.Map{"saved": file, "mode": "visual"})
        case "json":
                if err := configedit.ValidateJSON([]byte(req.Content)); err != nil {
                        return failMsg(c, 400, "INVALID_JSON_SCHEMA", err.Error())
                }
                if err := a.Files.Write(guard, file, []byte(req.Content)); err != nil {
                        return failMsg(c, 500, err.Error(), err.Error())
                }
                return ok(c, fiber.Map{"saved": file, "mode": "json"})
        default:
                if err := a.Files.Write(guard, file, []byte(req.Content)); err != nil {
                        return failMsg(c, 500, err.Error(), err.Error())
                }
                a.Log.Audit("api", 0, def.ServerID, "", "Edited config file "+file)
                return ok(c, fiber.Map{"saved": file, "mode": "raw"})
        }
}

func (a *App) depsForStats() servers.Deps {
        return servers.Deps{DB: a.DB, Bus: a.Bus, Log: a.Log, Cfg: a.Cfg, Docker: a.Docker, Catalog: a.SteamCatalog}
}

func userIDOf(c fiber.Ctx) int64 {
        if p, ok := c.Locals("principal").(fiber.Ctx); ok {
                _ = p
        }
        if p := currentUserFrom(c); p != nil {
                return p.UserID
        }
        return 0
}

func boolI(v interface{}) int64 {
        switch x := v.(type) {
        case bool:
                if x {
                        return 1
                }
        case float64:
                if x != 0 {
                        return 1
                }
        }
        return 0
}

// toStringMap coerces a JSON decoded value into map[string]string.
func toStringMap(v interface{}) map[string]string {
        out := map[string]string{}
        switch x := v.(type) {
        case map[string]interface{}:
                for k, val := range x {
                        if s, ok := val.(string); ok {
                                out[k] = s
                        } else {
                                out[k] = fmt.Sprintf("%v", val)
                        }
                }
        }
        return out
}

// jsonMapOf decodes a JSON object column into a map (nil-safe).
func jsonMapOf(src *string) map[string]string {
        if src == nil || *src == "" {
                return map[string]string{}
        }
        out := map[string]string{}
        _ = json.Unmarshal([]byte(*src), &out)
        return out
}

// jsonStringsOf decodes a JSON array column (nil-safe).
func jsonStringsOf(src *string) []string {
        if src == nil || *src == "" {
                return []string{}
        }
        var out []string
        _ = json.Unmarshal([]byte(*src), &out)
        if out == nil {
                return []string{}
        }
        return out
}

// fileOrDirExists reports whether the path resolves (java selection check).
func fileOrDirExists(p string) bool {
        _, err := os.Stat(p)
        return err == nil
}

func numI(v interface{}, def int64) int64 {
        switch x := v.(type) {
        case float64:
                return int64(x)
        case int:
                return int64(x)
        case int64:
                return x
        }
        return def
}

var _ = strconv.Itoa
var _ = events.EvSendStartReload

// toStringSlice coerces a JSON decoded value into []string.
func toStringSlice(v interface{}) []string {
        switch x := v.(type) {
        case []string:
                return x
        case []interface{}:
                out := make([]string, 0, len(x))
                for _, item := range x {
                        if s, ok := item.(string); ok {
                                out = append(out, s)
                        } else {
                                out = append(out, fmt.Sprintf("%v", item))
                        }
                }
                return out
        case string:
                if x == "" {
                        return []string{}
                }
                parts := strings.Split(x, ",")
                for i := range parts {
                        parts[i] = strings.TrimSpace(parts[i])
                }
                return parts
        }
        return []string{}
}
