package httpapi

import (
	"runtime"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/FlameInTheDark/servy/internal/authz"
	"github.com/FlameInTheDark/servy/internal/config"
	"github.com/FlameInTheDark/servy/internal/configedit"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
	"github.com/FlameInTheDark/servy/internal/servers"
	"github.com/FlameInTheDark/servy/internal/version"
)

func (a *App) mountPanel(api fiber.Router) {
	g := api.Group("/panel")

	g.Get("/check", a.panelCheck)
	g.Get("/stats", a.panelHostStats)
	g.Get("/config", a.getPanelConfig)
	g.Patch("/config", a.patchPanelConfig)
	g.Post("/config/customize", a.customizeBranding)
	g.Patch("/config/embed", a.patchEmbed)
	g.Delete("/config/embed", a.deleteEmbed)
	g.Patch("/config/logos", a.patchLogos)
	g.Delete("/config/logos", a.deleteLogos)
	g.Get("/config/servers_dir", a.getServersDir)
	g.Patch("/config/servers_dir", a.setServersDir)
	g.Get("/logs/:type", a.panelLogs)
	g.Get("/support_logs", a.supportLogs)
	g.Get("/support_logs/status", a.supportLogsStatus)
	g.Get("/JarCache", a.jarCache)
	g.Get("/SteamCache", a.steamCache)
	g.Get("/SteamCatalog", a.steamCatalog)

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

	// cluster nodes
	g.Get("/nodes", a.listNodes)
	g.Post("/nodes", a.addNode)
	g.Get("/nodes/:node_id", a.getNode)
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

// getPanelConfig returns the categorized config + branding.
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
	st, _ := a.Auth.GetSettings()
	branding := map[string]interface{}{
		"login_photo":   deref(st.LoginPhoto),
		"login_opacity": st.LoginOpacity,
		"logo_full":     deref(st.LogoFull),
		"logo_square":   deref(st.LogoSquare),
	}
	embed := map[string]interface{}{
		"og_enabled": st.OgEnabled == 1, "og_title": deref(st.OgTitle),
		"og_description": deref(st.OgDescription), "og_image": deref(st.OgImage),
		"og_color": deref(st.OgColor),
	}
	return ok(c, fiber.Map{"categories": categories, "branding": branding, "embed": embed})
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

func (a *App) customizeBranding(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	var req struct {
		LoginPhoto   *string  `json:"login_photo,omitempty"`
		LoginOpacity *float64 `json:"login_opacity,omitempty"`
		LogoFull     *string  `json:"logo_full,omitempty"`
		LogoSquare   *string  `json:"logo_square,omitempty"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	st, _ := a.Auth.GetSettings()
	photo := st.LoginPhoto
	opacity := st.LoginOpacity
	full := st.LogoFull
	square := st.LogoSquare
	if req.LoginPhoto != nil {
		photo = req.LoginPhoto
	}
	if req.LoginOpacity != nil {
		opacity = *req.LoginOpacity
	}
	if req.LogoFull != nil {
		full = req.LogoFull
	}
	if req.LogoSquare != nil {
		square = req.LogoSquare
	}
	a.DB.UpdateBranding(photo, opacity, full, square)
	return ok(c, fiber.Map{"updated": true})
}

func (a *App) patchEmbed(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	var req struct {
		Enabled *bool   `json:"og_enabled,omitempty"`
		Title   *string `json:"og_title,omitempty"`
		Desc    *string `json:"og_description,omitempty"`
		Image   *string `json:"og_image,omitempty"`
		Color   *string `json:"og_color,omitempty"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	st, _ := a.Auth.GetSettings()
	enabled := st.OgEnabled
	title, desc, image, color := st.OgTitle, st.OgDescription, st.OgImage, st.OgColor
	if req.Enabled != nil {
		enabled = bool2i(*req.Enabled)
	}
	if req.Title != nil {
		title = req.Title
	}
	if req.Desc != nil {
		desc = req.Desc
	}
	if req.Image != nil {
		image = req.Image
	}
	if req.Color != nil {
		color = req.Color
	}
	a.DB.UpdateEmbed(enabled, title, desc, image, color)
	return ok(c, fiber.Map{"updated": true})
}

func (a *App) deleteEmbed(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	var zero *string
	a.DB.UpdateEmbed(0, zero, zero, zero, zero)
	return ok(c, fiber.Map{"deleted": true})
}

func (a *App) patchLogos(c fiber.Ctx) error { return a.customizeBranding(c) }

func (a *App) deleteLogos(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	var zero *string
	a.DB.UpdateBranding(zero, 1.0, zero, zero)
	return ok(c, fiber.Map{"deleted": true})
}

func (a *App) getServersDir(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	return ok(c, fiber.Map{"servers_dir": a.ServersRoot})
}

func (a *App) setServersDir(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	var req struct {
		Dir string `json:"dir"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	abs, err := filepath.Abs(req.Dir)
	if err != nil || !strings.HasPrefix(abs, a.DataDir) {
		return failMsg(c, 400, "INVALID_DIR", "directory must be inside the data dir")
	}
	a.DB.SetMasterServerDir(&abs)
	return ok(c, fiber.Map{"servers_dir": abs})
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

func (a *App) steamCatalog(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	return ok(c, a.Creation.SteamCatalog())
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

func (a *App) listNodes(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	rows, _ := a.DB.ListNodes()
	out := []map[string]interface{}{}
	for _, n := range rows {
		out = append(out, map[string]interface{}{
			"node_id": n.NodeID, "name": n.Name, "address": n.Address, "role": n.Role,
			"status": n.Status, "last_seen": deref(n.LastSeen), "version": deref(n.Version),
			"created": n.Created,
		})
	}
	return ok(c, out)
}

func (a *App) addNode(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	var req struct {
		Name    string `json:"name"`
		Address string `json:"address"` // host:port of the agent's gRPC server
		Token   string `json:"token"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if req.Name == "" || req.Address == "" {
		return failMsg(c, 400, "INVALID_JSON_SCHEMA", "name and address required")
	}
	nodeID := "node-" + newUUID()[:8]
	tokenHash := ""
	if req.Token != "" {
		tokenHash = req.Token // stored for panel→agent dialing reference (documented)
	}
	now := databaseNow()
	if err := a.DB.UpsertNode(nodeID, req.Name, req.Address, "agent", "registered", &now, &version.Version, nil, &tokenHash, now); err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	// say hello
	if err := a.Cluster.Hello(req.Address, nodeID, req.Name, version.Version, "panel", nil); err != nil {
		return ok(c, fiber.Map{"node_id": nodeID, "registered": true, "hello": err.Error()})
	}
	a.DB.UpdateNodeStatus("online", &now, &version.Version, nodeID)
	return ok(c, fiber.Map{"node_id": nodeID, "registered": true, "hello": "ok"})
}

func (a *App) getNode(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	n, err := a.DB.GetNode(c.Params("node_id"))
	if err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	serversOn := []map[string]interface{}{}
	nodeIDP := n.NodeID
	serversRows, _ := a.DB.ListServersByNode(&nodeIDP)
	for _, def := range serversRows {
		serversOn = append(serversOn, map[string]interface{}{"server_id": def.ServerID, "server_name": def.ServerName})
	}
	return ok(c, fiber.Map{
		"node_id": n.NodeID, "name": n.Name, "address": n.Address, "role": n.Role,
		"status": n.Status, "last_seen": deref(n.LastSeen), "version": deref(n.Version),
		"servers": serversOn,
	})
}

func (a *App) deleteNode(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	if err := a.DB.DeleteNode(c.Params("node_id")); err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	return ok(c, fiber.Map{"deleted": c.Params("node_id")})
}

// ---------------- manifests ----------------

func (a *App) listManifests(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	rows, _ := a.DB.ListDefinitions()
	out := []map[string]interface{}{}
	for _, d := range rows {
		var m map[string]interface{}
		json.Unmarshal([]byte(d.Definition), &m)
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
	raw, _ := json.Marshal(m)
	now := databaseNow()
	mver := m.Version
	empty := ""
	emptyP := &empty
	if err := a.DB.UpsertDefinition(m.ID, m.Name, m.Type, emptyP, string(raw), "url", &mver, now, now); err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
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
	json.Unmarshal([]byte(d.Definition), &m)
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
	var req struct {
		DefinitionID string `json:"definition_id"`
		URL          string `json:"url"`
		Name         string `json:"name"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	var raw string
	if req.DefinitionID != "" {
		d, err := a.DB.GetDefinition(req.DefinitionID)
		if err != nil {
			return fail(c, 404, "NOT_FOUND")
		}
		raw = d.Definition
	} else if req.URL != "" {
		m, err := configedit.NewRegistry().FromURL(req.URL)
		if err != nil {
			return failMsg(c, 400, "INVALID_MANIFEST", err.Error())
		}
		b, _ := json.Marshal(m)
		raw = string(b)
	} else {
		return failMsg(c, 400, "INVALID_JSON_SCHEMA", "definition_id or url required")
	}
	var m configedit.Manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return failMsg(c, 400, "INVALID_MANIFEST", err.Error())
	}
	name := req.Name
	if name == "" {
		name = m.Name
	}
	cr := creationRequest{
		CreateType: orStr(m.CreateType, "download_jar"), Name: name, ServerType: m.Type,
		Port: m.Port, JarURL: m.JarURL, SteamSlug: m.SteamSlug, AppID: m.AppID,
		MinMem: m.MinMem, MaxMem: m.MaxMem, StopCommand: m.StopCommand,
		Executable: m.Executable, ExecutionCommand: m.ExecutionCommand,
	}
	res, err := a.Creation.Create(cr.toCreationRequest(), p.UserID)
	if err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	// apply config overrides (server.properties) + env.json + extra files
	if len(m.Config) > 0 || len(m.Env) > 0 {
		if sup, err := a.Registry.Get(res.ServerID); err == nil {
			if len(m.Config) > 0 {
				doc := configedit.ParseProperties("")
				for k, v := range m.Config {
					doc.Set(k, v)
				}
				a.Files.Write(newGuard(sup.Definition().Path), "server.properties", []byte(configedit.RenderProperties(doc)))
			}
			if len(m.Env) > 0 {
				b, _ := json.MarshalIndent(m.Env, "", "  ")
				a.Files.Write(newGuard(sup.Definition().Path), "env.json", b)
			}
		}
	}
	return ok(c, res)
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
