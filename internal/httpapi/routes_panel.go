package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/FlameInTheDark/servy/internal/authz"
	"github.com/FlameInTheDark/servy/internal/cluster"
	"github.com/FlameInTheDark/servy/internal/config"
	"github.com/FlameInTheDark/servy/internal/configedit"
	"github.com/FlameInTheDark/servy/internal/creation"
	"github.com/FlameInTheDark/servy/internal/events"
	"github.com/FlameInTheDark/servy/internal/files"
	"github.com/FlameInTheDark/servy/internal/models"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
	"github.com/FlameInTheDark/servy/internal/safehttp"
	"github.com/FlameInTheDark/servy/internal/servers"
	"github.com/FlameInTheDark/servy/internal/steamcmd"
	"github.com/FlameInTheDark/servy/internal/version"
)

func (a *App) mountPanel(api fiber.Router) {
	g := api.Group("/panel")

	g.Get("/check", a.panelCheck)
	g.Get("/stats", a.panelHostStats)
	g.Get("/config", a.getPanelConfig)
	g.Patch("/config", a.patchPanelConfig)
	g.Get("/logs/:type", a.panelLogs)
	g.Get("/support_logs", a.supportLogs)
	g.Get("/support_logs/status", a.supportLogsStatus)
	g.Get("/JarCache", a.jarCache)
	g.Get("/SteamCache", a.steamCache)
	g.Get("/SteamCatalog", a.steamCatalog) // compatibility alias
	g.Get("/steam/catalog", a.steamCatalog)
	g.Get("/steam/catalog/custom", a.customSteamCatalog)
	g.Post("/steam/catalog/custom/validate", a.validateSteamManifest)
	g.Post("/steam/catalog/custom", a.saveSteamManifest)
	g.Delete("/steam/catalog/custom/:slug", a.deleteSteamManifest)

	// execution environment
	g.Get("/java", a.panelJavaInstalls)
	g.Get("/shells", a.panelShells)
	g.Get("/docker", a.panelDockerStatus)
	g.Post("/docker/images", a.panelDockerPullImage)
	g.Post("/admin/upload", a.adminUpload)
	g.Get("/antilockout", a.getAntiLockout)
	g.Post("/antilockout", a.postAntiLockout)
	g.Get("/update_check", a.updateCheck)

	// announcements
	g.Get("/announcements", a.getAnnouncements)
	g.Post("/announcements", a.clearAnnouncement)

	// webhooks (server-scoped mount lives under servers too — original path kept)
	api.Get("/servers/:id/webhook", a.listWebhooks)
	api.Post("/servers/:id/webhook", a.createWebhook)
	api.Get("/webhook/:webhook_id", a.getWebhook)
	api.Patch("/webhook/:webhook_id", a.patchWebhook)
	api.Delete("/webhook/:webhook_id", a.deleteWebhook)

	// cluster nodes: join tickets + membership
	g.Get("/nodes", a.listNodes)
	g.Get("/nodes/tickets", a.listNodeTickets)
	g.Post("/nodes/tickets", a.createNodeTicket)
	g.Delete("/nodes/tickets/:hash", a.revokeNodeTicket)
	g.Get("/nodes/:node_id", a.getNode)
	g.Post("/nodes/:node_id/ping", a.pingNode)
	g.Delete("/nodes/:node_id", a.deleteNode)

	// manifests (URL-importable server definitions)
	g.Get("/manifests", a.listManifests)
	g.Post("/manifests", a.addManifest)
	g.Get("/manifests/:definition_id", a.getManifest)
	g.Delete("/manifests/:definition_id", a.deleteManifest)
	g.Post("/manifests/apply", a.applyManifest)

	// audit log
	g.Get("/audit", a.auditLog)

	// setup status (public)
	api.Get("/setup/status", a.setupStatus)
}

// panelJavaInstalls lists detected JVMs (java selection) + the Minecraft
// Java requirement matrix. Available to any authenticated user with server
// config access; superuser sees the config-provided extra paths hint.
// panelShells lists the terminals available on this host (per-server shell
// selection UI): bash/sh on Unix, cmd/powershell/git-bash on Windows.
func (a *App) panelShells(c fiber.Ctx) error {
	if p := a.requireAuth(c); p == nil {
		return nil
	}
	return ok(c, fiber.Map{
		"default":  servers.DefaultShellID(),
		"platform": runtime.GOOS,
		"shells":   servers.DetectShells(),
	})
}

func (a *App) panelJavaInstalls(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	installs := servers.DetectJavas()
	return ok(c, fiber.Map{
		"installs":                    installs,
		"minecraft_java_requirements": servers.MinecraftJavaRequirements,
	})
}

// panelDockerStatus reports the docker execution backend availability.
func (a *App) panelDockerStatus(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	available := a.Docker != nil && a.Docker.Available()
	res := fiber.Map{"available": available}
	if a.Docker != nil {
		res["host"] = a.Docker.Host()
		if available {
			res["version"] = a.Docker.Version()
		}
	}
	return ok(c, res)
}

// panelDockerPullImage pre-pulls an image for the docker execution backend.
func (a *App) panelDockerPullImage(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	if a.Docker == nil || !a.Docker.Available() {
		return fail(c, 400, "DOCKER_UNAVAILABLE")
	}
	var req struct {
		Image string `json:"image"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if req.Image == "" {
		return failMsg(c, 400, "INVALID_JSON", "image required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	var lines []string
	if err := a.Docker.EnsureImage(ctx, req.Image, func(line string) {
		lines = append(lines, line)
		if len(lines) > 200 {
			lines = lines[len(lines)-200:]
		}
	}); err != nil {
		return failMsg(c, 400, "IMAGE_PULL_FAILED", err.Error())
	}
	return ok(c, fiber.Map{"image": req.Image, "output": lines})
}

func (a *App) panelCheck(c fiber.Ctx) error {
	return ok(c, fiber.Map{"alive": true, "version": version.Version, "time": databaseNow()})
}

func (a *App) panelHostStats(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	snap := a.HostStats.Snapshot()
	hours := fiber.Query[int](c, "hours", 24)
	history := servers.HostHistory(a.depsForStats(), hours)
	running, total := 0, 0
	for _, sup := range a.Registry.All() {
		total++
		if sup.State() == "running" {
			running++
		}
	}
	return ok(c, fiber.Map{"snapshot": snap, "history": history,
		"mem_usage": snap.MemUsage, "mem_total": snap.MemTotal, "mem_percent": snap.MemPercent,
		"cpu_usage": snap.CPUUsage, "running_servers": running, "total_servers": total})
}

// getPanelConfig returns the categorized config shown on the panel settings page.
func (a *App) getPanelConfig(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	cfg := a.Cfg.Get()
	b, _ := json.Marshal(cfg)
	var flat map[string]interface{}
	json.Unmarshal(b, &flat)
	categories := map[string]interface{}{}
	for cat, keys := range config.Categories {
		entries := map[string]interface{}{}
		for _, k := range keys {
			if v, ok := flat[k]; ok {
				entries[k] = v
			}
		}
		categories[cat] = entries
	}
	return ok(c, fiber.Map{"categories": categories})
}

func (a *App) patchPanelConfig(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	var patch map[string]interface{}
	if err := parseBody(c, &patch); err != nil {
		return err
	}
	if err := a.Cfg.Patch(patch); err != nil {
		return failMsg(c, 400, "INVALID_JSON_SCHEMA", err.Error())
	}
	a.Bus.PublishSimple("panel_config_update", map[string]interface{}{"updated": true})
	a.Log.Audit("api", 0, "", c.IP(), "Updated panel config")
	return ok(c, fiber.Map{"updated": true})
}

func (a *App) panelLogs(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	logType := c.Params("type")
	cfg := a.Cfg.Get()
	if !p.Superuser && !cfg.GeneralUserLogAccess {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	valid := map[string]bool{"audit": true, "session": true, "schedule": true, "cmd_queue": true, "commander": true, "auth": true, "server": true}
	if !valid[logType] {
		return failMsg(c, 404, "NOT_FOUND", "unknown log type")
	}
	logger := a.Log.Get(logType)
	return ok(c, fiber.Map{"type": logType, "lines": len(logger.Tail(0)), "log": logger.Tail(cfg.MaxLogLines)})
}

// supportLogs zips logs+config for debugging (superuser, progress via WS).
func (a *App) supportLogs(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	name := fmt.Sprintf("support_logs_%s.zip", time.Now().UTC().Format("2006-01-02_15-04-05"))
	path := filepath.Join(a.DataDir, name)
	if err := a.buildSupportZip(path); err != nil {
		return failMsg(c, 500, err.Error(), err.Error())
	}
	c.Set("Content-Disposition", "attachment; filename="+name)
	return c.SendFile(path)
}

func (a *App) supportLogsStatus(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	return ok(c, fiber.Map{"status": "idle"})
}

func (a *App) buildSupportZip(path string) error {
	// minimal zip: panel loggers + config (sanitized: no secrets)

	entries := []zipEntry{}
	for _, t := range a.Log.LoggerNames() {
		l := a.Log.Get(t)
		var sb strings.Builder
		for _, e := range l.Tail(0) {
			fmt.Fprintf(&sb, "%s [%s] %s\n", e.Time.Format(time.RFC3339), e.Level, e.Message)
		}
		entries = append(entries, zipEntry{"logs/" + t + ".log", sb.String()})
	}
	cfg := a.Cfg.Get()
	cfgBytes, _ := json.MarshalIndent(cfg, "", "  ")
	entries = append(entries, zipEntry{"config.json", string(cfgBytes)})
	return writeZip(path, entries)
}

func (a *App) jarCache(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	if fiber.Query[bool](c, "refresh", false) {
		a.Bucket.ForceRefresh()
	}
	m, err := a.Bucket.ListJars()
	if err != nil {
		return failMsg(c, 502, "UPSTREAM_ERROR", err.Error())
	}
	return ok(c, m)
}

func (a *App) steamCache(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	if fiber.Query[bool](c, "refresh", false) {
		a.Bucket.ForceRefresh()
	}
	m, err := a.Bucket.ListSteamApps()
	if err != nil {
		return failMsg(c, 502, "UPSTREAM_ERROR", err.Error())
	}
	return ok(c, m)
}

type steamCatalogEntry struct {
	steamcmd.Game
	Custom bool `json:"custom"`
}

func (a *App) steamCatalog(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	games := a.SteamCatalog.All()
	out := make([]steamCatalogEntry, 0, len(games))
	for _, game := range games {
		out = append(out, steamCatalogEntry{Game: game, Custom: a.SteamCatalog.IsCustom(game.Slug)})
	}
	return ok(c, out)
}

func (a *App) customSteamCatalog(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	return ok(c, a.SteamCatalog.CustomAll())
}

func (a *App) validateSteamManifest(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	game, err := a.parseSteamManifest(c)
	if err != nil {
		return failMsg(c, 400, "INVALID_MANIFEST", err.Error())
	}
	return ok(c, game)
}

func (a *App) saveSteamManifest(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	game, err := a.parseSteamManifest(c)
	if err != nil {
		return failMsg(c, 400, "INVALID_MANIFEST", err.Error())
	}
	if err := a.SteamCatalog.Upsert(game); err != nil {
		return failMsg(c, 400, "INVALID_MANIFEST", err.Error())
	}
	a.Log.Audit("api", userIDOf(c), "", c.IP(), fmt.Sprintf("Updated Steam game manifest %s", game.Slug))
	a.Bus.PublishSimple("steam_catalog_update", map[string]string{"slug": game.Slug})
	return ok(c, steamCatalogEntry{Game: game, Custom: true})
}

func (a *App) deleteSteamManifest(c fiber.Ctx) error {
	p := a.requireSuperuser(c)
	if p == nil {
		return nil
	}
	slug := c.Params("slug")
	if err := a.SteamCatalog.DeleteCustom(slug); err != nil {
		return failMsg(c, 404, "NOT_FOUND", err.Error())
	}
	a.Log.Audit("api", p.UserID, "", c.IP(), fmt.Sprintf("Removed Steam game manifest %s", slug))
	a.Bus.PublishSimple("steam_catalog_update", map[string]string{"slug": slug, "deleted": "true"})
	return ok(c, fiber.Map{"deleted": slug})
}

func (a *App) parseSteamManifest(c fiber.Ctx) (steamcmd.Game, error) {
	if len(c.Body()) > steamcmd.MaxGameManifestSize+4096 {
		return steamcmd.Game{}, fmt.Errorf("manifest exceeds the %d-byte limit", steamcmd.MaxGameManifestSize)
	}
	var req struct {
		Manifest string `json:"manifest"`
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return steamcmd.Game{}, fmt.Errorf("invalid request JSON: %w", err)
	}
	return steamcmd.ParseGameManifest([]byte(req.Manifest))
}

func (a *App) adminUpload(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	kind := c.Query("kind", "embed")
	name := c.Query("name", "upload.png")
	data := c.Body()
	cfg := a.Cfg.Get()
	if len(data) > cfg.MaxImageUploadSizeMB<<20 {
		return failMsg(c, 400, "FILE_TOO_LARGE", "image exceeds max size")
	}
	dir := filepath.Join(a.DataDir, "config", "uploads")
	os.MkdirAll(dir, 0o755)
	safe := filepath.Base(name)
	if err := os.WriteFile(filepath.Join(dir, kind+"-"+safe), data, 0o644); err != nil {
		return failMsg(c, 500, err.Error(), err.Error())
	}
	url := "/api/v1/panel/uploads/" + kind + "-" + safe
	return ok(c, fiber.Map{"url": url, "name": safe, "size": len(data)})
}

func (a *App) getAntiLockout(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	count, _ := a.DB.CountUsers()
	return ok(c, fiber.Map{"users": count, "available": count == 0})
}

func (a *App) postAntiLockout(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	count, _ := a.DB.CountUsers()
	if count > 0 {
		return failMsg(c, 400, "NOT_FOUND", "anti-lockout only available with zero users")
	}
	user, pass, err := a.Auth.AntiLockoutUser(a.DataDir)
	if err != nil {
		return failMsg(c, 500, err.Error(), err.Error())
	}
	return ok(c, fiber.Map{"username": user, "password": pass,
		"note": "credentials also written to default-creds.txt; user is removed on next boot"})
}

func (a *App) updateCheck(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	res, err := version.CheckForUpdate()
	if err != nil {
		return ok(c, res)
	}
	return ok(c, res)
}

// ---------------- announcements ----------------

// announcement mirrors legacy remote announcements.
type announcement struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Popup bool   `json:"popup"`
}

func builtinAnnouncements() []announcement {
	return []announcement{{
		ID: "welcome-go", Title: "Welcome to Servy",
		Body: "Servy panel — all your servers, faster, in one place.",
	}}
}

func (a *App) getAnnouncements(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	cleared := map[string]bool{}
	if u, err := a.DB.GetUserByID(p.UserID); err == nil && u.ClearedNotifs != nil {
		for _, id := range strings.Split(*u.ClearedNotifs, ",") {
			cleared[id] = true
		}
	}
	out := []announcement{}
	for _, ann := range builtinAnnouncements() {
		if !cleared[ann.ID] {
			out = append(out, ann)
		}
	}
	return ok(c, out)
}

func (a *App) handlePublicAnnouncements(c fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "ok", "data": builtinAnnouncements()})
}

func (a *App) clearAnnouncement(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	u, err := a.DB.GetUserByID(p.UserID)
	if err != nil {
		return fail(c, 404, "USER_NOT_FOUND")
	}
	cleared := map[string]bool{}
	if u.ClearedNotifs != nil {
		for _, id := range strings.Split(*u.ClearedNotifs, ",") {
			cleared[id] = true
		}
	}
	cleared[req.ID] = true
	joined := strings.Join(keys(cleared), ",")
	a.Auth.UpdateUser(p, p.UserID, map[string]interface{}{"cleared_notifs": joined})
	return ok(c, fiber.Map{"cleared": req.ID})
}

func keys(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------------- webhooks ----------------

func (a *App) webhookView(w gen.Webhook) map[string]interface{} {
	return map[string]interface{}{
		"webhook_id": w.WebhookID, "server_id": w.ServerID, "name": w.Name,
		"url": w.Url, "webhook_type": w.WebhookType, "bot_name": deref(w.BotName),
		"trigger": strings.Split(deref(w.Trigger), ","), "body": deref(w.Body),
		"color": deref(w.Color), "enabled": w.Enabled == 1, "created": w.Created,
	}
}

func (a *App) listWebhooks(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	serverID := c.Params("id")
	if !p.CanServer(serverID, authz.ServerConfig) {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	rows, _ := a.DB.ListWebhooksForServer(serverID)
	out := []map[string]interface{}{}
	for _, w := range rows {
		out = append(out, a.webhookView(w))
	}
	return ok(c, out)
}

func (a *App) createWebhook(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	serverID := c.Params("id")
	if !p.CanServer(serverID, authz.ServerConfig) {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	var req struct {
		Name    string   `json:"name"`
		URL     string   `json:"url"`
		Type    string   `json:"webhook_type"`
		BotName string   `json:"bot_name"`
		Trigger []string `json:"trigger"`
		Body    string   `json:"body"`
		Color   string   `json:"color"`
		Enabled bool     `json:"enabled"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if req.URL == "" {
		return failMsg(c, 400, "INVALID_JSON_SCHEMA", "url required")
	}
	trigger := strings.Join(req.Trigger, ",")
	w, err := a.DB.CreateWebhook(newUUID(), databaseNow(), serverID, req.Name, req.URL, req.Type,
		&req.BotName, &trigger, &req.Body, &req.Color, bool2i(req.Enabled))
	if err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, a.webhookView(w))
}

func (a *App) loadWebhook(c fiber.Ctx) (gen.Webhook, bool) {
	p := a.requireAuth(c)
	if p == nil {
		return gen.Webhook{}, false
	}
	w, err := a.DB.GetWebhook(c.Params("webhook_id"))
	if err != nil {
		_ = fail(c, 404, "NOT_FOUND")
		return gen.Webhook{}, false
	}
	if !p.CanServer(w.ServerID, authz.ServerConfig) {
		_ = fail(c, 403, "NOT_AUTHORIZED")
		return gen.Webhook{}, false
	}
	return w, true
}

func (a *App) getWebhook(c fiber.Ctx) error {
	w, good := a.loadWebhook(c)
	if !good {
		return nil
	}
	return ok(c, a.webhookView(w))
}

func (a *App) patchWebhook(c fiber.Ctx) error {
	w, good := a.loadWebhook(c)
	if !good {
		return nil
	}
	var req struct {
		Name    *string  `json:"name,omitempty"`
		URL     *string  `json:"url,omitempty"`
		Type    *string  `json:"webhook_type,omitempty"`
		BotName *string  `json:"bot_name,omitempty"`
		Trigger []string `json:"trigger,omitempty"`
		Body    *string  `json:"body,omitempty"`
		Color   *string  `json:"color,omitempty"`
		Enabled *bool    `json:"enabled,omitempty"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if req.Name != nil {
		w.Name = *req.Name
	}
	if req.URL != nil {
		w.Url = *req.URL
	}
	if req.Type != nil {
		w.WebhookType = *req.Type
	}
	if req.BotName != nil {
		w.BotName = req.BotName
	}
	if req.Trigger != nil {
		j := strings.Join(req.Trigger, ",")
		w.Trigger = &j
	}
	if req.Body != nil {
		w.Body = req.Body
	}
	if req.Color != nil {
		w.Color = req.Color
	}
	if req.Enabled != nil {
		w.Enabled = bool2i(*req.Enabled)
	}
	if err := a.DB.UpdateWebhook(w.Name, w.Url, w.WebhookType, w.BotName, w.Trigger, w.Body, w.Color, w.Enabled, w.WebhookID); err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	updated, _ := a.DB.GetWebhook(w.WebhookID)
	return ok(c, a.webhookView(updated))
}

func (a *App) deleteWebhook(c fiber.Ctx) error {
	w, good := a.loadWebhook(c)
	if !good {
		return nil
	}
	if err := a.DB.DeleteWebhook(w.WebhookID); err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, fiber.Map{"deleted": w.WebhookID})
}

// ---------------- cluster nodes ----------------

// splitCSV splits a comma separated capability list.
func splitCSV(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// nodeAddress resolves what agents should dial for the panel's own node API.
func (a *App) nodeAddress() string {
	c := a.Cfg.Get()
	host := c.HTTPHost
	if host == "" || host == "0.0.0.0" || host == "::" {
		if h, err := os.Hostname(); err == nil {
			host = h
		}
	}
	return fmt.Sprintf("%s:%d", host, c.GRPCPort)
}

// nodeView builds the API row of one node (live state + durable columns).
func (a *App) nodeViewFromState(st cluster.NodeState) map[string]interface{} {
	running, total := a.Nodes.Counts(st.NodeID)
	if total == 0 && st.Total > 0 {
		running, total = st.Running, st.Total
	}
	view := map[string]interface{}{
		"node_id": st.NodeID, "name": st.Name, "address": st.Address, "role": st.Role,
		"status": st.Status, "online": st.Online(), "version": st.Version,
		"capabilities": st.Capabilities, "cpu": st.CPU, "mem_percent": st.MemPct,
		"mem_used": st.MemUsed, "mem_total": st.MemTotal,
		"servers_running": running, "servers_total": total,
		"last_seen": st.LastSeen.UTC().Format(time.RFC3339), "created": st.Created.UTC().Format(time.RFC3339),
		"last_error": st.LastError, "local": false,
	}
	return view
}

func (a *App) listNodes(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	out := []map[string]interface{}{}
	// this panel is itself a node that can host servers
	snap := a.HostStats.Snapshot()
	local := map[string]interface{}{
		"node_id": servers.LocalNodeID, "name": "This panel", "address": a.nodeAddress(),
		"role": "panel", "status": "online", "online": true, "version": version.Version,
		"cpu": snap.CPUUsage, "mem_percent": snap.MemPercent, "mem_used": snap.MemUsage,
		"mem_total": snap.MemTotal, "capabilities": []string{"servers", "files", "terminal", "backups", "creation"},
		"local": true,
	}
	running, total := 0, 0
	for _, sup := range a.Registry.All() {
		total++
		if sup.State() == models.StateRunning || sup.State() == models.StateStarting {
			running++
		}
	}
	local["servers_running"], local["servers_total"] = running, total
	out = append(out, local)

	for _, st := range a.Nodes.Snapshot() {
		out = append(out, a.nodeViewFromState(st))
	}
	// durable rows that never finished joining (ticket issued, agent absent)
	for _, row := range a.listNodeRows() {
		if _, ok := a.Nodes.Get(row.NodeID); ok {
			continue
		}
		out = append(out, map[string]interface{}{
			"node_id": row.NodeID, "name": row.Name, "address": row.Address, "role": row.Role,
			"status": "offline", "online": false, "version": deref(row.Version),
			"capabilities": splitCSV(deref(row.Capabilities)), "cpu": 0, "mem_percent": 0,
			"mem_used": 0, "mem_total": 0, "servers_running": 0, "servers_total": 0,
			"last_seen": deref(row.LastSeen), "created": row.Created, "last_error": "", "local": false,
		})
	}
	// cluster enablement hint for the UI
	enabled := a.Cfg.Get().GRPCEnabled
	return ok(c, fiber.Map{"nodes": out, "tickets": a.pendingTickets(), "cluster_enabled": enabled,
		"panel_address": a.nodeAddress()})
}

func (a *App) listNodeRows() []gen.Node {
	rows, err := a.DB.ListNodes()
	if err != nil {
		return nil
	}
	return rows
}

func (a *App) pendingTickets() []map[string]interface{} {
	out := []map[string]interface{}{}
	if a.Tickets == nil {
		return out
	}
	for _, t := range a.Tickets.Pending() {
		out = append(out, map[string]interface{}{
			"note": t.Note, "created": t.Created.UTC().Format(time.RFC3339),
			"expires": t.Expires.UTC().Format(time.RFC3339), "expires_in": int(time.Until(t.Expires).Seconds()),
		})
	}
	return out
}

// createNodeTicket issues a one-shot join ticket the agent redeems.
func (a *App) createNodeTicket(c fiber.Ctx) error {
	p := a.requireSuperuser(c)
	if p == nil {
		return nil
	}
	var req struct {
		Note string `json:"note"`
	}
	if len(c.Body()) > 0 {
		_ = json.Unmarshal(c.Body(), &req)
	}
	ticket, expires := a.Tickets.Issue(req.Note)
	address := a.nodeAddress()
	cmdline := fmt.Sprintf("servy agent run --panel %s --ticket %s", address, ticket)
	a.Log.Audit(p.Username, p.UserID, "", c.IP(), "Issued node join ticket")
	return ok(c, fiber.Map{
		"ticket": ticket, "expires": expires.UTC().Format(time.RFC3339),
		"expires_in": int(time.Until(expires).Seconds()), "panel_address": address, "command": cmdline,
	})
}

func (a *App) listNodeTickets(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	return ok(c, a.pendingTickets())
}

func (a *App) revokeNodeTicket(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	a.Tickets.Revoke(c.Params("hash"))
	return ok(c, fiber.Map{"revoked": true})
}

func (a *App) getNode(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	nodeID := c.Params("node_id")
	serversOn := []map[string]interface{}{}
	var nodeIDPtr *string
	if nodeID != servers.LocalNodeID {
		nodeIDPtr = &nodeID
	}
	serversRows, _ := a.DB.ListServersByNode(nodeIDPtr)
	for _, def := range serversRows {
		rt := a.remoteRuntime(def.ServerID)
		item := map[string]interface{}{"server_id": def.ServerID, "server_name": def.ServerName, "type": def.Type}
		if rt != nil {
			item["state"] = string(rt.State)
			item["running"] = rt.Running
			item["online"] = rt.Online
		} else {
			item["state"] = string(models.StateStopped)
			item["running"] = false
		}
		serversOn = append(serversOn, item)
	}

	if nodeID == servers.LocalNodeID {
		return ok(c, fiber.Map{
			"node_id": servers.LocalNodeID, "name": "This panel", "address": a.nodeAddress(),
			"role": "panel", "status": "online", "online": true, "version": version.Version,
			"local": true, "servers": serversOn,
		})
	}
	if st, found := a.Nodes.Get(nodeID); found {
		view := a.nodeViewFromState(st)
		view["servers"] = serversOn
		return ok(c, view)
	}
	row, err := a.DB.GetNode(nodeID)
	if err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	return ok(c, fiber.Map{
		"node_id": row.NodeID, "name": row.Name, "address": row.Address, "role": row.Role,
		"status": "offline", "online": false, "version": deref(row.Version),
		"last_seen": deref(row.LastSeen), "created": row.Created, "servers": serversOn,
	})
}

// pingNode re-checks a node right now (used by the UI's "check" button).
func (a *App) pingNode(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	nodeID := c.Params("node_id")
	if nodeID == servers.LocalNodeID {
		return ok(c, fiber.Map{"online": true, "version": version.Version, "local": true})
	}
	started := time.Now()
	ver, err := a.Cluster.Ping(nodeID)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return failMsg(c, 502, "NODE_OFFLINE", err.Error())
	}
	return ok(c, fiber.Map{"online": true, "version": ver, "latency_ms": latency})
}

// deleteNode forgets a node: the credential is revoked and the row removed.
// Nodes that still host servers are refused unless ?force=true, which only
// drops the panel's records (the node keeps its files).
func (a *App) deleteNode(c fiber.Ctx) error {
	p := a.requireSuperuser(c)
	if p == nil {
		return nil
	}
	nodeID := c.Params("node_id")
	if nodeID == servers.LocalNodeID {
		return failMsg(c, 400, "INVALID_NODE", "the panel node cannot be removed")
	}
	var nodeIDPtr *string
	if nodeID != servers.LocalNodeID {
		id := nodeID
		nodeIDPtr = &id
	}
	rows, _ := a.DB.ListServersByNode(nodeIDPtr)
	force := fiber.Query[bool](c, "force", false)
	if len(rows) > 0 && !force {
		names := []string{}
		for _, def := range rows {
			names = append(names, def.ServerName)
		}
		return c.Status(409).JSON(fiber.Map{
			"status": "error", "error": "NODE_HAS_SERVERS",
			"error_data":  fmt.Sprintf("this node still hosts %d server(s)", len(rows)),
			"servers":     names,
			"force_hint":  "pass ?force=true to remove the panel records only",
		})
	}
	for _, def := range rows {
		_ = a.DB.DeleteBackupsForServer(def.ServerID)
		_ = a.DB.DeleteSchedulesForServer(def.ServerID)
		_ = a.DB.DeleteWebhooksForServer(def.ServerID)
		_ = a.DB.DeleteServer(def.ServerID)
	}
	a.Cluster.Forget(nodeID)
	a.Nodes.Forget(nodeID)
	if err := a.DB.DeleteNode(nodeID); err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	a.Log.Audit(p.Username, p.UserID, "", c.IP(), "Removed cluster node "+nodeID)
	return ok(c, fiber.Map{"deleted": nodeID, "servers_removed": len(rows)})
}

// ---------------- manifests ----------------

func (a *App) listManifests(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	rows, err := a.DB.ListDefinitions()
	if err != nil {
		return failMsg(c, 500, "SERVER_ERROR", err.Error())
	}
	out := []map[string]interface{}{}
	for _, d := range rows {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(d.Definition), &m); err != nil || m == nil {
			m = map[string]interface{}{"name": d.Name, "type": d.Type, "invalid": true}
		}
		m["definition_id"] = d.DefinitionID
		m["source"] = d.Source
		out = append(out, m)
	}
	return ok(c, out)
}

func (a *App) addManifest(c fiber.Ctx) error {
	p := a.requireSuperuser(c)
	if p == nil {
		return nil
	}
	if len(c.Body()) > (4<<20)+8192 {
		return failMsg(c, 413, "INVALID_MANIFEST", "manifest exceeds the 4 MiB limit")
	}
	var req struct {
		URL  string `json:"url"`
		JSON string `json:"manifest"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	reg := configedit.NewRegistry()
	var m *configedit.Manifest
	var err error
	if req.URL != "" {
		m, err = reg.FromURL(req.URL)
	} else if req.JSON != "" {
		m, err = reg.ParseManifest([]byte(req.JSON))
	} else {
		return failMsg(c, 400, "INVALID_JSON_SCHEMA", "url or manifest required")
	}
	if err != nil {
		return failMsg(c, 400, "INVALID_MANIFEST", err.Error())
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return failMsg(c, 500, "SERVER_ERROR", err.Error())
	}
	now := databaseNow()
	mver := m.Version
	empty := ""
	source := "inline"
	if req.URL != "" {
		source = "url"
	}
	if err := a.DB.UpsertDefinition(m.ID, m.Name, m.Type, &empty, string(raw), source, &mver, now, now); err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	a.Log.Audit("api", p.UserID, "", c.IP(), fmt.Sprintf("Stored server manifest %s", m.ID))
	return ok(c, m)
}

func (a *App) getManifest(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	d, err := a.DB.GetDefinition(c.Params("definition_id"))
	if err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(d.Definition), &m); err != nil || m == nil {
		return failMsg(c, 500, "INVALID_MANIFEST", "stored definition is invalid")
	}
	return ok(c, m)
}

func (a *App) deleteManifest(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	if err := a.DB.DeleteDefinition(c.Params("definition_id")); err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	return ok(c, fiber.Map{"deleted": c.Params("definition_id")})
}

// applyManifest creates a server from a stored or inline manifest (auto-setup).
func (a *App) applyManifest(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	if !p.CanPanel(authz.PanelServerCreation) {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	if !p.Superuser && p.LimitServers >= 0 && int64(len(p.ServerIDs())) >= p.LimitServers {
		return failMsg(c, 403, "LIMIT_REACHED", "server creation limit reached")
	}
	var req struct {
		DefinitionID string `json:"definition_id"`
		URL          string `json:"url"`
		Name         string `json:"name"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}

	var manifest *configedit.Manifest
	if req.DefinitionID != "" {
		definition, err := a.DB.GetDefinition(req.DefinitionID)
		if err != nil {
			return fail(c, 404, "NOT_FOUND")
		}
		manifest, err = configedit.NewRegistry().ParseManifest([]byte(definition.Definition))
		if err != nil {
			return failMsg(c, 400, "INVALID_MANIFEST", err.Error())
		}
	} else if req.URL != "" {
		var err error
		manifest, err = configedit.NewRegistry().FromURL(req.URL)
		if err != nil {
			return failMsg(c, 400, "INVALID_MANIFEST", err.Error())
		}
	} else {
		return failMsg(c, 400, "INVALID_JSON_SCHEMA", "definition_id or url required")
	}

	preparedFiles, err := stageManifestFiles(*manifest)
	if err != nil {
		return failMsg(c, 400, "INVALID_MANIFEST", err.Error())
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = manifest.Name
	}
	hasPostCreateConfig := len(preparedFiles) > 0 || len(manifest.Config) > 0 || len(manifest.Env) > 0
	deferAutoStart := hasPostCreateConfig && manifest.AutoStart
	cr := creationRequest{
		CreateType: manifest.CreateType, Name: name, ServerType: manifest.Type,
		Port: manifest.Port, JarURL: manifest.JarURL, SteamSlug: manifest.SteamSlug,
		AppID: manifest.AppID, SteamOptions: manifest.SteamOptions, MinMem: manifest.MinMem, MaxMem: manifest.MaxMem,
		StopCommand: manifest.StopCommand, Executable: manifest.Executable,
		ExecutionCommand: manifest.ExecutionCommand, JavaSelection: manifest.JavaSelection,
		Shell: manifest.Shell, ExecutionBackend: manifest.ExecutionBackend,
		DockerImage: manifest.DockerImage, DockerEnv: manifest.DockerEnv,
		DockerPorts: manifest.DockerPorts, DockerBinds: manifest.DockerBinds,
		DockerNetwork: manifest.DockerNetwork, DockerWorkdir: manifest.DockerWorkdir,
		DockerAutoremove: manifest.DockerAutoremove, AutoStart: manifest.AutoStart && !deferAutoStart,
		CrashDetection: manifest.CrashDetection, BackupDirs: manifest.BackupDirs,
	}
	archivePath := ""
	if manifest.CreateType == creation.CreateImportZip {
		archivePath, err = downloadManifestArchive(manifest.ArchiveURL, a.ImportDir)
		if err != nil {
			return failMsg(c, 400, "MANIFEST_DOWNLOAD_FAILED", err.Error())
		}
		cr.ArchivePath = archivePath
	}
	res, err := a.Creation.Create(cr.toCreationRequest(), p.UserID)
	if archivePath != "" {
		_ = os.Remove(archivePath)
	}
	if err != nil {
		return failMsg(c, 400, "SERVER_CREATE_FAILED", err.Error())
	}

	rollback := func(cause error) error {
		if cleanupErr := a.Registry.Delete(res.ServerID, true, p.UserID); cleanupErr != nil {
			a.Log.Get("server").Error("failed to roll back manifest-created server %s: %v", res.ServerID, cleanupErr)
			if dbErr := a.DB.DeleteServer(res.ServerID); dbErr != nil {
				a.Log.Get("server").Error("failed to remove rolled-back server row %s: %v", res.ServerID, dbErr)
			}
			if a.ServersRoot != "" {
				if fileErr := os.RemoveAll(filepath.Join(a.ServersRoot, res.ServerID)); fileErr != nil {
					a.Log.Get("server").Error("failed to remove rolled-back server files %s: %v", res.ServerID, fileErr)
				}
			}
		}
		return failMsg(c, 500, "MANIFEST_APPLY_FAILED", cause.Error())
	}
	if hasPostCreateConfig {
		sup, err := a.Registry.Get(res.ServerID)
		if err != nil {
			return rollback(err)
		}
		guard, err := files.NewGuard(sup.Definition().Path)
		if err != nil {
			return rollback(err)
		}
		for _, file := range preparedFiles {
			if err := a.Files.Write(guard, file.Path, file.Data); err != nil {
				return rollback(err)
			}
		}
		if len(manifest.Config) > 0 {
			doc := configedit.ParseProperties("")
			for key, value := range manifest.Config {
				doc.Set(key, value)
			}
			if err := a.Files.Write(guard, "server.properties", []byte(configedit.RenderProperties(doc))); err != nil {
				return rollback(err)
			}
		}
		if len(manifest.Env) > 0 {
			data, err := json.MarshalIndent(manifest.Env, "", "  ")
			if err != nil {
				return rollback(err)
			}
			if err := a.Files.Write(guard, "env.json", data); err != nil {
				return rollback(err)
			}
		}
	}
	if deferAutoStart {
		if err := a.Registry.UpdateConfig(res.ServerID, func(def *gen.Server) { def.AutoStart = 1 }); err != nil {
			return rollback(err)
		}
		a.Bus.Publish(events.Event{Type: events.EvSendStartReload, ServerID: res.ServerID,
			Data: map[string]interface{}{"server_id": res.ServerID, "created": true}})
	}
	return ok(c, res)
}

type preparedManifestFile struct {
	Path string
	Data []byte
}

func stageManifestFiles(manifest configedit.Manifest) ([]preparedManifestFile, error) {
	files := make([]preparedManifestFile, 0, len(manifest.Files))
	total := int64(0)
	for name, value := range manifest.Files {
		var data []byte
		if strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
			var err error
			data, err = downloadManifestFile(value)
			if err != nil {
				return nil, fmt.Errorf("download file %q: %w", name, err)
			}
		} else {
			data = []byte(value)
		}
		total += int64(len(data))
		if total > 16<<20 {
			return nil, fmt.Errorf("manifest files exceed the 16 MiB total limit")
		}
		files = append(files, preparedManifestFile{Path: filepath.FromSlash(strings.ReplaceAll(name, `\`, "/")), Data: data})
	}
	return files, nil
}

func downloadManifestFile(rawURL string) ([]byte, error) {
	client := safehttp.NewClient(30 * time.Second)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("file exceeds the 4 MiB limit")
	}
	return data, nil
}

func downloadManifestArchive(rawURL, importDir string) (string, error) {
	if err := os.MkdirAll(importDir, 0o755); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(importDir, "manifest-archive-*.zip")
	if err != nil {
		return "", err
	}
	filePath := file.Name()
	cleanup := func(cause error) (string, error) {
		file.Close()
		os.Remove(filePath)
		return "", cause
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		return cleanup(err)
	}
	client := safehttp.NewClient(15 * time.Minute)
	response, err := client.Do(request)
	if err != nil {
		return cleanup(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return cleanup(fmt.Errorf("HTTP status %d", response.StatusCode))
	}
	const maxArchiveSize = int64(2 << 30)
	written, err := io.Copy(file, io.LimitReader(response.Body, maxArchiveSize+1))
	if err != nil {
		return cleanup(err)
	}
	if written > maxArchiveSize {
		return cleanup(fmt.Errorf("archive exceeds the 2 GiB limit"))
	}
	if err := file.Close(); err != nil {
		os.Remove(filePath)
		return "", err
	}
	return filePath, nil
}

// ---------------- audit ----------------

func (a *App) auditLog(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	limit := fiber.Query[int](c, "limit", 100)
	offset := fiber.Query[int](c, "offset", 0)
	rows, _ := a.DB.ListAudit(int64(limit), int64(offset))
	out := []map[string]interface{}{}
	for _, r := range rows {
		out = append(out, map[string]interface{}{
			"audit_id": r.AuditID, "created": r.Created, "user_name": deref(r.UserName),
			"user_id": r.UserID, "server_id": deref(r.ServerID), "source_ip": deref(r.SourceIp),
			"log_msg": r.LogMsg,
		})
	}
	return ok(c, out)
}

// ---------------- setup ----------------

func (a *App) setupStatus(c fiber.Ctx) error {
	count := a.Auth.CountUsers()
	return ok(c, fiber.Map{"needs_setup": count == 0, "users": count})
}

// scheduleExecutor mirrors the engine executor (used by "run now").
func (a *App) scheduleExecutor() func(serverID, action, actionID, command string, userID int64) error {
	return func(serverID, action, actionID, command string, userID int64) error {
		sup, err := a.Registry.Get(serverID)
		if err != nil {
			return err
		}
		switch action {
		case "start":
			return sup.Start(userID)
		case "stop":
			return sup.Stop("schedule", userID)
		case "restart":
			return sup.Restart(userID, nil)
		case "kill":
			return sup.Kill(userID)
		case "backup":
			bid := actionID
			if bid == "" {
				if b, err := a.Backups.DefaultBackup(serverID); err == nil {
					bid = b.BackupID
				}
			}
			if bid == "" {
				return fmt.Errorf("no backup config")
			}
			return a.Backups.Run(bid, userID)
		case "command":
			return sup.SendCommand(command, userID)
		}
		return fmt.Errorf("unknown action %q", action)
	}
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var _ = http.StatusOK
