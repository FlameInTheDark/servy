package httpapi

import (
        "encoding/json"
        "path"
        "regexp"
        "path/filepath"
        "sort"
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
        "github.com/FlameInTheDark/servy/internal/steamcmd"
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

        // config editor (visual + raw); the query form supports nested paths.
        api.Get("/servers/:id/config", a.serverConfigList)
        api.Get("/servers/:id/config-file", a.serverConfigGet)
        api.Put("/servers/:id/config-file", a.serverConfigPut)
        api.Get("/servers/:id/config/:file", a.serverConfigGet)
        api.Put("/servers/:id/config/:file", a.serverConfigPut)
        api.Get("/servers/:id/launch-options", a.serverLaunchOptions)

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
        if snapshot, err := steamcmd.GameSnapshotFromDefinitions(def.Definitions); err == nil && snapshot != nil {
                v["steam_manifest"] = true
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

// loadServer returns (sup, def, nodeID, ok) — remote servers come back with a
// non-empty nodeID and a nil supervisor.
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
        if nodeID := a.nodeIDForServer(serverID); nodeID != "" {
                return nil, def, nodeID, true
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
                } else if def.NodeID != nil && *def.NodeID != "" && *def.NodeID != servers.LocalNodeID {
                        rt = a.remoteRuntime(def.ServerID)
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
        // node placement: the node runs its own creation pipeline (downloads,
        // extraction, steam install) and the panel mirrors the resulting row.
        if req.Node != "" && req.Node != servers.LocalNodeID {
                return a.createServerOnNode(c, p, req.Node, cr)
        }
        res, err := a.Creation.Create(cr, p.UserID)
        if err != nil {
                return failMsg(c, 400, err.Error(), err.Error())
        }
        return ok(c, res)
}

func (a *App) getServer(c fiber.Ctx) error {
        sup, def, nodeID, good := a.loadServer(c, -1)
        if !good {
                return nil
        }
        if nodeID != "" {
                view := a.serverView(def, a.remoteRuntime(def.ServerID))
                view["remote"] = true
                view["node_id"] = nodeID
                view["node_online"] = a.nodeOnline(nodeID)
                return ok(c, view)
        }
        rt := sup.Runtime()
        return ok(c, a.serverView(def, &rt))
}

func (a *App) patchServer(c fiber.Ctx) error {
        return a.patchServerConfig(c)
}

// applyServerPatch lives in the servers package (shared with cluster agents).
func applyServerPatch(def *gen.Server, patch map[string]interface{}) error {
        return servers.ApplyServerPatch(def, patch)
}

func (a *App) patchServerConfig(c fiber.Ctx) error {
        sup, def, nodeID, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        var patch map[string]interface{}
        if err := parseBody(c, &patch); err != nil {
                return err
        }
        if nodeID != "" {
                if !a.nodeOnline(nodeID) {
                        return fail(c, 502, "NODE_OFFLINE")
                }
                // The panel mirrors the node's server row, so bad launch
                // parameters fail fast with 400 instead of a node round-trip.
                // The node validates again — it stays the source of truth.
                check := def
                if err := applyServerPatch(&check, patch); err != nil {
                        return failMsg(c, 400, err.Error(), err.Error())
                }
                raw, err := a.Cluster.UpdateConfig(nodeID, def.ServerID, patch)
                if err != nil {
                        return failMsg(c, 502, "NODE_ERROR", err.Error())
                }
                if err := a.applyRemoteDef(raw, nodeID); err != nil {
                        return failMsg(c, 500, "SERVER_ERROR", err.Error())
                }
                updated, _ := a.DB.GetServer(def.ServerID)
                view := a.serverView(updated, a.remoteRuntime(def.ServerID))
                view["remote"] = true
                view["node_id"] = nodeID
                return ok(c, view)
        }
        // Patch a copy first: a rejected launch parameter (bad password length,
        // unknown option, unusable memory value) must not be half-persisted.
        if err := applyServerPatch(&def, patch); err != nil {
                return failMsg(c, 400, err.Error(), err.Error())
        }
        if err := a.Registry.UpdateConfig(def.ServerID, func(current *gen.Server) {
                *current = def
        }); err != nil {
                return failMsg(c, 400, err.Error(), err.Error())
        }
        _ = sup
        updated, _ := a.DB.GetServer(sup.Definition().ServerID)
        rt := sup.Runtime()
        return ok(c, a.serverView(updated, &rt))
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
        if nodeID := a.nodeIDForServer(serverID); nodeID != "" {
                if !a.nodeOnline(nodeID) {
                        return fail(c, 502, "NODE_OFFLINE")
                }
                // drop every panel-side record first so the UI cannot show a
                // server whose node is gone, then delete on the node.
                if err := a.DB.DeleteBackupsForServer(serverID); err != nil {
                        return failMsg(c, 500, "SERVER_ERROR", err.Error())
                }
                if err := a.Cluster.Delete(nodeID, serverID, removeFiles); err != nil {
                        a.Log.Get("server").Error("cluster: delete %s on %s failed: %v", serverID, nodeID, err)
                }
                _ = a.DB.DeleteSchedulesForServer(serverID)
                _ = a.DB.DeleteWebhooksForServer(serverID)
                if err := a.DB.DeleteServer(serverID); err != nil {
                        return failMsg(c, 500, "SERVER_ERROR", err.Error())
                }
                return ok(c, fiber.Map{"deleted": serverID})
        }
        if err := a.Creation.DeleteFlow(serverID, removeFiles, p.UserID); err != nil {
                return failMsg(c, 400, err.Error(), err.Error())
        }
        return ok(c, fiber.Map{"deleted": serverID})
}

// serverAction runs a lifecycle action with remote routing.
func (a *App) serverAction(c fiber.Ctx, perm int, local func(sup *servers.Supervisor, p fiber.Ctx) error, remote func(nodeID, id string) error) error {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        if !p.CanServer(c.Params("id"), perm) {
                return fail(c, 403, "NOT_AUTHORIZED")
        }
        serverID := c.Params("id")
        if nodeID := a.nodeIDForServer(serverID); nodeID != "" {
                if !a.nodeOnline(nodeID) {
                        return fail(c, 502, "NODE_OFFLINE")
                }
                if err := remote(nodeID, serverID); err != nil {
                        return failMsg(c, 502, "NODE_ERROR", err.Error())
                }
                return ok(c, fiber.Map{"server_id": serverID, "action": "remote", "node_id": nodeID})
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
        }, a.Cluster.StartServer)
}

func (a *App) serverStop(c fiber.Ctx) error {
        return a.serverAction(c, authz.ServerCommands, func(sup *servers.Supervisor, c fiber.Ctx) error {
                return sup.Stop("api", userIDOf(c))
        }, a.Cluster.StopServer)
}

func (a *App) serverRestart(c fiber.Ctx) error {
        return a.serverAction(c, authz.ServerCommands, func(sup *servers.Supervisor, c fiber.Ctx) error {
                return sup.Restart(userIDOf(c), nil)
        }, a.Cluster.RestartServer)
}

func (a *App) serverKill(c fiber.Ctx) error {
        return a.serverAction(c, authz.ServerCommands, func(sup *servers.Supervisor, c fiber.Ctx) error {
                return sup.Kill(userIDOf(c))
        }, a.Cluster.KillServer)
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
        if nodeID := a.nodeIDForServer(serverID); nodeID != "" {
                if !a.nodeOnline(nodeID) {
                        return fail(c, 502, "NODE_OFFLINE")
                }
                if err := a.Cluster.SendCommand(nodeID, serverID, req.Command); err != nil {
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
        }, func(nodeID, id string) error { return a.Cluster.AgreeEula(nodeID, id) })
}

func (a *App) serverUpdateExecutable(c fiber.Ctx) error {
        sup, def, nodeID, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        if nodeID != "" {
                if !a.nodeOnline(nodeID) {
                        return fail(c, 502, "NODE_OFFLINE")
                }
                if err := a.Cluster.UpdateExecutable(nodeID, def.ServerID); err != nil {
                        return failMsg(c, 502, "NODE_ERROR", err.Error())
                }
                return ok(c, fiber.Map{"updated": true, "remote": true})
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
        sup, def, nodeID, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        if nodeID != "" {
                if !a.nodeOnline(nodeID) {
                        return fail(c, 502, "NODE_OFFLINE")
                }
                newID := newUUID()
                raw, err := a.Cluster.Clone(nodeID, def.ServerID, newID, def.ServerName+"-clone")
                if err != nil {
                        return failMsg(c, 502, "NODE_ERROR", err.Error())
                }
                uid := userIDOf(c)
                if err := a.applyRemoteDefWithOwner(raw, nodeID, &uid); err != nil {
                        return failMsg(c, 500, "SERVER_ERROR", err.Error())
                }
                if err := a.Backups.EnsureDefault(def, a.BackupRoot); err != nil {
                        // best-effort: cloning must not fail because of backups
                        a.Log.Get("server").Warning("cluster: default backup for clone failed: %v", err)
                }
                return ok(c, fiber.Map{"server_id": newID, "name": def.ServerName + "-clone", "node_id": nodeID})
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
        stopped, failed := 0, 0
        for _, sup := range a.Registry.All() {
                if sup.State() == models.StateRunning {
                        stopped++
                        go sup.Stop("stop_all", p.UserID)
                }
        }
        // remote servers are stopped through their nodes (best effort: one dead
        // node must not keep the rest running)
        byNode := map[string][]string{}
        rows, _ := a.DB.ListServers()
        for _, def := range rows {
                if def.NodeID == nil || *def.NodeID == "" || *def.NodeID == servers.LocalNodeID {
                        continue
                }
                rt := a.remoteRuntime(def.ServerID)
                if rt == nil || !rt.Running {
                        continue
                }
                byNode[*def.NodeID] = append(byNode[*def.NodeID], def.ServerID)
        }
        for nodeID, ids := range byNode {
                for _, serverID := range ids {
                        if err := a.Cluster.StopServer(nodeID, serverID); err != nil {
                                failed++
                                a.Log.Get("server").Warning("cluster: stop_all failed for %s on %s: %v", serverID, nodeID, err)
                                continue
                        }
                        stopped++
                }
        }
        return ok(c, fiber.Map{"stopping": "all", "servers": stopped, "failed": failed})
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
        // remote servers report through their node's cached status
        rows, _ := a.DB.ListServers()
        for _, def := range rows {
                if def.NodeID == nil || *def.NodeID == "" || *def.NodeID == servers.LocalNodeID {
                        continue
                }
                if !p.Superuser && !ids[def.ServerID] {
                        continue
                }
                if def.ShowStatus == 0 {
                        continue
                }
                rt := a.remoteRuntime(def.ServerID)
                if rt == nil {
                        out[def.ServerID] = fiber.Map{"running": false, "state": string(models.StateStopped)}
                        continue
                }
                out[def.ServerID] = fiber.Map{"running": rt.Running, "state": string(rt.State)}
        }
        return ok(c, out)
}

func (a *App) serverStats(c fiber.Ctx) error {
        sup, def, nodeID, good := a.loadServer(c, -1)
        if !good {
                return nil
        }
        if nodeID != "" {
                rt := a.remoteRuntime(def.ServerID)
                if rt == nil {
                        rt = &models.ServerRuntime{ServerID: def.ServerID, State: models.StateStopped, Players: []string{}}
                }
                return ok(c, fiber.Map{"runtime": rt, "latest": nil, "remote": true})
        }
        rt := sup.Runtime()
        latest := servers.LatestStat(a.depsForStats(), sup.Definition().ServerID)
        return ok(c, fiber.Map{"runtime": rt, "latest": latest})
}

func (a *App) serverHistory(c fiber.Ctx) error {
        sup, def, nodeID, good := a.loadServer(c, -1)
        if !good {
                return nil
        }
        hours := fiber.Query[int](c, "hours", 24)
        if nodeID != "" {
                raw, err := a.Cluster.History(nodeID, def.ServerID, hours)
                if err != nil {
                        return failMsg(c, 502, "NODE_ERROR", err.Error())
                }
                return ok(c, json.RawMessage(raw))
        }
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
        rt := models.ServerRuntime{Players: []string{}}
        if sup, err := a.Registry.Get(serverID); err == nil {
                rt = sup.Runtime()
        } else if r := a.remoteRuntime(serverID); r != nil {
                rt = *r
        }
        return ok(c, fiber.Map{
                "server_name": def.ServerName, "type": def.Type, "online": rt.Online,
                "max": rt.Max, "players": rt.Players, "version": rt.Version, "desc": rt.Desc,
                "running": rt.Running, "icon": "",
        })
}

func (a *App) serverLogs(c fiber.Ctx) error {
        sup, def, nodeID, good := a.loadServer(c, authz.ServerLogs)
        if !good {
                return nil
        }
        if nodeID != "" {
                if !a.nodeOnline(nodeID) {
                        return fail(c, 502, "NODE_OFFLINE")
                }
                if def.LogPath != "" {
                        if data, err := a.Cluster.ReadFile(nodeID, def.ServerID, def.LogPath, 8<<20); err == nil {
                                lines := strings.Split(string(data), "\n")
                                if len(lines) > 700 {
                                        lines = lines[len(lines)-700:]
                                }
                                return ok(c, fiber.Map{"source": "file", "lines": lines})
                        }
                }
                lines, err := a.Cluster.Terminal(nodeID, def.ServerID, 0)
                if err != nil {
                        return failMsg(c, 502, "NODE_ERROR", err.Error())
                }
                return ok(c, fiber.Map{"source": "terminal", "lines": lines})
        }
        // log file (if exists) else virtual terminal
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
        sup, def, nodeID, good := a.loadServer(c, authz.ServerTerminal)
        if !good {
                return nil
        }
        if nodeID != "" {
                lines, err := a.Cluster.Terminal(nodeID, def.ServerID, fiber.Query[int](c, "lines", 70))
                if err != nil {
                        return failMsg(c, 502, "NODE_ERROR", err.Error())
                }
                return ok(c, lines)
        }
        return ok(c, sup.Terminal().Tail(fiber.Query[int](c, "lines", 0)))
}

func (a *App) serverPlayers(c fiber.Ctx) error {
        sup, def, nodeID, good := a.loadServer(c, authz.ServerPlayers)
        if !good {
                return nil
        }
        if nodeID != "" {
                rt := a.remoteRuntime(def.ServerID)
                if rt == nil {
                        rt = &models.ServerRuntime{Players: []string{}}
                }
                return ok(c, fiber.Map{"online": rt.Online, "max": rt.Max, "players": rt.Players})
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

// configInfoFor classifies one config file for the editor UI.
func configInfoFor(file, label, description, editor string) map[string]interface{} {
        normalized := path.Clean(strings.ReplaceAll(file, `\`, "/"))
        if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") || strings.HasPrefix(normalized, "/") {
                return nil
        }
        n := strings.ToLower(normalized)
        info := map[string]interface{}{"file": normalized, "mode": "raw", "format": "text"}
        switch {
        case n == "server.properties":
                info["mode"], info["format"] = "visual+raw", "properties"
        case strings.HasSuffix(n, ".json"):
                info["format"] = "json"
        case strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml"):
                info["format"] = "yaml"
        case strings.HasSuffix(n, ".properties") || strings.HasSuffix(n, ".ini") || strings.HasSuffix(n, ".conf") || strings.HasSuffix(n, ".cfg"):
                info["format"] = "properties"
        case strings.HasSuffix(n, ".xml"):
                info["format"] = "xml"
        }
        if label != "" {
                info["label"] = label
        }
        if description != "" {
                info["description"] = description
        }
        if editor == "lines" {
                info["editor"] = "lines"
                info["mode"] = "lines"
        }
        return info
}

// isEditableConfigName reports whether a directory entry is worth listing.
func isEditableConfigName(name string) bool {
        n := strings.ToLower(name)
        return n == "server.properties" || strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".yml") ||
                strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".properties") || strings.HasSuffix(n, ".ini") ||
                strings.HasSuffix(n, ".conf") || strings.HasSuffix(n, ".cfg") || strings.HasSuffix(n, ".txt") || strings.HasSuffix(n, ".xml")
}

func sortConfigList(filesList []map[string]interface{}) []map[string]interface{} {
        sort.Slice(filesList, func(i, j int) bool {
                left := strings.ToLower(filesList[i]["file"].(string))
                right := strings.ToLower(filesList[j]["file"].(string))
                if left == right {
                        return filesList[i]["file"].(string) < filesList[j]["file"].(string)
                }
                return left < right
        })
        return filesList
}

// launchOptionView is one editable manifest launch parameter of a Steam
// server, resolved for display in the server view.
type launchOptionView struct {
        Key          string                       `json:"key"`
        Label        string                       `json:"label"`
        Description  string                       `json:"description,omitempty"`
        Type         string                       `json:"type"`
        Flag         string                       `json:"flag,omitempty"`
        Value        string                       `json:"value"`
        Default      string                       `json:"default,omitempty"`
        Required     bool                         `json:"required,omitempty"`
        Min          *int                         `json:"min,omitempty"`
        Max          *int                         `json:"max,omitempty"`
        MinLength    *int                         `json:"min_length,omitempty"`
        MaxLength    *int                         `json:"max_length,omitempty"`
        Options      []steamcmd.LaunchOptionValue `json:"options,omitempty"`
        Automatic    string                       `json:"automatic,omitempty"`
        UsingDefault bool                         `json:"using_default,omitempty"`
}

var (
        xmsValuePattern = regexp.MustCompile(`(?i)-Xms([0-9]+[KMG]?)`)
        xmxValuePattern = regexp.MustCompile(`(?i)-Xmx([0-9]+[KMG]?)`)
)

// serverLaunchOptions returns the parameters that were collected while the
// server was created (Steam launch options, -Xms/-Xmx memory flags) so the
// server view can show and change them later.
func (a *App) serverLaunchOptions(c fiber.Ctx) error {
        sup, def, nodeID, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        _ = sup
        view := buildLaunchOptions(def)
        if nodeID != "" {
                view["remote"] = true
                view["node_id"] = nodeID
                view["node_online"] = a.nodeOnline(nodeID)
                if state, ok := a.Nodes.Get(nodeID); ok {
                        view["node_name"] = state.Name
                }
        }
        return ok(c, view)
}

// buildLaunchOptions is the pure part of the launch-options view: it resolves
// the frozen game manifest and the memory flags of one server row.
func buildLaunchOptions(def gen.Server) fiber.Map {
        view := fiber.Map{
                "server_id": def.ServerID,
                "editable":  false,
                "argv":      []string{},
                "command":   def.ExecutionCommand,
        }
        if snapshot, err := steamcmd.GameSnapshotFromDefinitions(def.Definitions); err == nil && snapshot != nil {
                context := launchContextFor(def, snapshot)
                options := make([]launchOptionView, 0, len(snapshot.Game.LaunchOptions))
                for _, option := range snapshot.Game.LaunchOptions {
                        item := launchOptionView{
                                Key: option.Key, Label: option.Label, Description: option.Description,
                                Type: option.Type, Flag: option.Flag, Default: option.Default,
                                Required: option.Required, Min: option.Min, Max: option.Max,
                                MinLength: option.MinLength, MaxLength: option.MaxLength, Options: option.Options,
                        }
                        if option.Source != "" {
                                item.Automatic = option.Source
                                if value, err := steamcmd.SourceValue(option.Source, context); err == nil {
                                        item.Value = value
                                }
                        } else {
                                item.Value = snapshot.LaunchOptions[option.Key]
                                item.UsingDefault = item.Value == ""
                                if item.UsingDefault {
                                        item.Value = option.Default
                                }
                        }
                        options = append(options, item)
                }
                view["editable"] = true
                view["kind"] = "steam"
                view["steam"] = fiber.Map{
                        "game": fiber.Map{
                                "name": snapshot.Game.Name, "app_id": snapshot.Game.AppID,
                                "platform": snapshot.Game.Platform, "slug": snapshot.Game.Slug,
                                "stop_command": snapshot.Game.StopCommand,
                        },
                        "options": options,
                        "argv":    resolvedArgv(snapshot, context),
                }
        }
        if min, max, ok := memoryFlagsOf(def.ExecutionCommand); ok {
                view["editable"] = true
                view["memory"] = fiber.Map{"min": min, "max": max}
        }
        return view
}

// serverGameSnapshot returns the frozen game manifest of a server, if any.
func (a *App) serverGameSnapshot(def gen.Server) (*steamcmd.GameSnapshot, error) {
        return steamcmd.GameSnapshotFromDefinitions(def.Definitions)
}

func launchContextFor(def gen.Server, snapshot *steamcmd.GameSnapshot) steamcmd.LaunchContext {
        port := int(def.ServerPort)
        queryPort := snapshot.Game.QueryPort
        if queryPort == 0 {
                queryPort = port + 1
        }
        return steamcmd.LaunchContext{
                ServerName: def.ServerName, Port: port, QueryPort: queryPort,
                SaveDir: filepath.Join(def.Path, "saves"),
        }
}

// resolvedArgv is the launch argument vector a start would use right now.
func resolvedArgv(snapshot *steamcmd.GameSnapshot, context steamcmd.LaunchContext) []string {
        argv, _, err := steamcmd.ResolveLaunchOptions(snapshot.Game.LaunchOptions, snapshot.LaunchOptions, context)
        if err != nil {
                return []string{}
        }
        return argv
}

// memoryFlagsOf extracts -Xms/-Xmx from an execution command.
func memoryFlagsOf(command string) (string, string, bool) {
        min, max := "", ""
        if match := xmsValuePattern.FindStringSubmatch(command); match != nil {
                min = match[1]
        }
        if match := xmxValuePattern.FindStringSubmatch(command); match != nil {
                max = match[1]
        }
        if min == "" && max == "" {
                return "", "", false
        }
        return min, max, true
}

func (a *App) serverConfigList(c fiber.Ctx) error {
        sup, rdef, nodeID, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        var def gen.Server
        if nodeID != "" {
                def = rdef
        } else {
                def = sup.Definition()
        }
        if nodeID != "" {
                return a.remoteConfigList(c, nodeID, def)
        }
        guard, err := files.NewGuard(def.Path)
        if err != nil {
                return failMsg(c, 500, "CONFIG_LIST_FAILED", err.Error())
        }
        entries, err := a.Files.List(guard, "")
        if err != nil {
                return failMsg(c, 500, "CONFIG_LIST_FAILED", err.Error())
        }
        known := map[string]map[string]interface{}{}
        addFile := func(file string, label, description, editor string) {
                if info := configInfoFor(file, label, description, editor); info != nil {
                        known[info["file"].(string)] = info
                }
        }
        for _, entry := range entries {
                if entry.Dir || !isEditableConfigName(entry.Name) {
                        continue
                }
                addFile(entry.Name, "", "", "")
        }

        game, hasGame, err := a.serverGameManifest(def)
        if err != nil {
                return failMsg(c, 500, "INVALID_SERVER_DEFINITION", err.Error())
        }
        if hasGame {
                for _, declared := range game.ConfigFiles {
                        file := path.Clean(strings.ReplaceAll(declared, `\`, "/"))
                        if stat, err := a.Files.Stat(guard, file); err == nil && !stat.Dir {
                                addFile(file, "", "", "")
                        }
                }
                for _, descriptor := range game.ConfigEditors {
                        file := path.Clean(strings.ReplaceAll(descriptor.Path, `\`, "/"))
                        if stat, err := a.Files.Stat(guard, file); err == nil && !stat.Dir {
                                addFile(file, descriptor.Label, descriptor.Description, descriptor.Editor)
                        }
                }
        }

        filesList := make([]map[string]interface{}, 0, len(known))
        for _, info := range known {
                filesList = append(filesList, info)
        }
        return ok(c, sortConfigList(filesList))
}

func (a *App) serverGameManifest(def gen.Server) (steamcmd.Game, bool, error) {
        if snapshot, err := steamcmd.GameSnapshotFromDefinitions(def.Definitions); err != nil {
                return steamcmd.Game{}, false, err
        } else if snapshot != nil {
                return snapshot.Game, true, nil
        }
        if a.SteamCatalog != nil && def.AppID != nil && *def.AppID != "" {
                game, ok := a.SteamCatalog.ByAppID(*def.AppID)
                return game, ok, nil
        }
        return steamcmd.Game{}, false, nil
}

func configFileParam(c fiber.Ctx) string {
        file := c.Params("file")
        if file == "" {
                file = c.Query("path")
        }
        return file
}

func normalizeConfigFilePath(file string) (string, error) {
        if strings.TrimSpace(file) == "" || strings.ContainsRune(file, '\x00') {
                return "", files.ErrTraversal
        }
        normalized := path.Clean(strings.ReplaceAll(file, `\`, "/"))
        drivePath := len(normalized) >= 2 && ((normalized[0] >= 'a' && normalized[0] <= 'z') || (normalized[0] >= 'A' && normalized[0] <= 'Z')) && normalized[1] == ':'
        if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") || strings.HasPrefix(normalized, "/") || drivePath {
                return "", files.ErrTraversal
        }
        return normalized, nil
}

func (a *App) serverConfigGet(c fiber.Ctx) error {
        sup, rdef, nodeID, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        var def gen.Server
        if nodeID != "" {
                def = rdef
        } else {
                def = sup.Definition()
        }
        file, err := normalizeConfigFilePath(configFileParam(c))
        if err != nil {
                return fail(c, 400, "TRAVERSAL DETECTED")
        }
        var data []byte
        if nodeID != "" {
                if !a.nodeOnline(nodeID) {
                        return fail(c, 502, "NODE_OFFLINE")
                }
                data, err = a.Cluster.ReadFile(nodeID, def.ServerID, file, 16<<20)
                if err != nil {
                        return fail(c, 404, "NOT_FOUND")
                }
        } else {
                guard, gerr := files.NewGuard(def.Path)
                if gerr != nil {
                        return failMsg(c, 500, "CONFIG_READ_FAILED", gerr.Error())
                }
                data, err = a.Files.Read(guard, file, 16<<20)
                if err != nil {
                        return fail(c, 404, "NOT_FOUND")
                }
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
        sup, rdef, nodeID, good := a.loadServer(c, authz.ServerConfig)
        if !good {
                return nil
        }
        var def gen.Server
        if nodeID != "" {
                def = rdef
        } else {
                def = sup.Definition()
        }
        file, err := normalizeConfigFilePath(configFileParam(c))
        if err != nil {
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
        // read/write functions: node API for remote servers, disk for local
        readFile := func(name string) ([]byte, error) {
                if nodeID != "" {
                        return a.Cluster.ReadFile(nodeID, def.ServerID, name, 16<<20)
                }
                guard, gerr := files.NewGuard(def.Path)
                if gerr != nil {
                        return nil, gerr
                }
                return a.Files.Read(guard, name, 16<<20)
        }
        writeFile := func(name string, data []byte) error {
                if nodeID != "" {
                        return a.Cluster.WriteFileBytes(nodeID, def.ServerID, name, data)
                }
                guard, gerr := files.NewGuard(def.Path)
                if gerr != nil {
                        return gerr
                }
                return a.Files.Write(guard, name, data)
        }
        if nodeID != "" && !a.nodeOnline(nodeID) {
                return fail(c, 502, "NODE_OFFLINE")
        }
        n := strings.ToLower(file)
        switch req.Mode {
        case "visual":
                if !(n == "server.properties" || strings.HasSuffix(n, ".properties")) {
                        return failMsg(c, 400, "INVALID_MODE", "visual mode supports .properties files")
                }
                cur, err := readFile(file)
                if err != nil {
                        return fail(c, 404, "NOT_FOUND")
                }
                doc := configedit.ParseProperties(string(cur))
                configedit.ApplyVisualForm(doc, req.Values)
                if err := writeFile(file, []byte(configedit.RenderProperties(doc))); err != nil {
                        return failMsg(c, 500, err.Error(), err.Error())
                }
                return ok(c, fiber.Map{"saved": file, "mode": "visual"})
        case "json":
                if err := configedit.ValidateJSON([]byte(req.Content)); err != nil {
                        return failMsg(c, 400, "INVALID_JSON_SCHEMA", err.Error())
                }
                if err := writeFile(file, []byte(req.Content)); err != nil {
                        return failMsg(c, 500, err.Error(), err.Error())
                }
                return ok(c, fiber.Map{"saved": file, "mode": "json"})
        default:
                if err := writeFile(file, []byte(req.Content)); err != nil {
                        return failMsg(c, 500, err.Error(), err.Error())
                }
                a.Log.Audit("api", userIDOf(c), def.ServerID, "", "Edited config file "+file)
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
