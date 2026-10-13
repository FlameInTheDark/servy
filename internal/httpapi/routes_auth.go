package httpapi

import (
	"github.com/gofiber/fiber/v3"

	"github.com/FlameInTheDark/servy/internal/version"
)

// mountAuth: login, logout, invalidate, setup status, version.
func (a *App) mountAuth(api fiber.Router) {
	g := api.Group("/auth")

	g.Post("/login", a.handleLogin)
	g.Post("/setup", a.handleSetup)
	g.Post("/logout", a.handleLogout)
	g.Post("/invalidate_tokens", a.handleInvalidateTokens)
	g.Post("/validate", a.handleValidateToken)
}

func (a *App) handleLogin(c fiber.Ctx) error {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TOTP     string `json:"totp_code,omitempty"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	ip := c.IP()
	token, userID, err := a.Auth.Login(req.Username, req.Password, req.TOTP, ip)
	if err != nil {
		switch err.Error() {
		case "MFA_REQUIRED":
			return fail(c, 401, "MFA_REQUIRED")
		case "INVALID_MFA":
			return fail(c, 401, "INVALID_MFA")
		case "LOCKED_OUT":
			return fail(c, 429, "LOCKED_OUT")
		case "ACCOUNT_DISABLED":
			return fail(c, 403, "ACCOUNT_DISABLED")
		default:
			return fail(c, 401, "BAD_PASSWORD")
		}
	}
	// set cookie for browser sessions
	cfg := a.Cfg.Get()
	c.Cookie(&fiber.Cookie{
		Name: "token", Value: token, Path: "/",
		MaxAge: cfg.CookieExpire * 86400, HTTPOnly: true,
	})
	u, err := a.DB.GetUserByID(userID)
	if err != nil {
		return fail(c, 500, "SERVER_ERROR")
	}
	view := a.Auth.View(u)
	p := a.Auth.PrincipalForUser(u)
	return ok(c, fiber.Map{"token": token, "user": view, "servers": p.ServerIDs()})
}

// handleSetup creates the first superuser (wizard) and signs them in.
func (a *App) handleSetup(c fiber.Ctx) error {
	count := a.Auth.CountUsers()
	if count > 0 {
		return failMsg(c, 400, "ALREADY_EXISTS", "panel already initialized")
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Lang     string `json:"lang"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if req.Lang == "" {
		req.Lang = "en_EN"
	}
	u, err := a.Auth.SeedFirstSetup(req.Username, req.Password, req.Lang)
	if err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	token, _, err := a.Auth.Login(req.Username, req.Password, "", c.IP())
	if err != nil {
		return failMsg(c, 500, err.Error(), "user created but login failed")
	}
	cfg := a.Cfg.Get()
	c.Cookie(&fiber.Cookie{Name: "token", Value: token, Path: "/", MaxAge: cfg.CookieExpire * 86400, HTTPOnly: true})
	return ok(c, fiber.Map{"token": token, "user": a.Auth.View(u)})
}

func (a *App) handleLogout(c fiber.Ctx) error {
	c.ClearCookie("token")
	return ok(c, fiber.Map{"logged_out": true})
}

func (a *App) handleInvalidateTokens(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	if err := a.Auth.InvalidateTokens(p, p.UserID); err != nil {
		return fail(c, 500, "SERVER_ERROR")
	}
	c.ClearCookie("token")
	return ok(c, fiber.Map{"invalidated": true})
}

func (a *App) handleValidateToken(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	return ok(c, fiber.Map{"valid": true, "user_id": p.UserID, "username": p.Username})
}

// handlePublicStatus: version + running servers (no auth — uptime widgets).
func (a *App) handlePublicStatus(c fiber.Ctx) error {
	running := 0
	for _, sup := range a.Registry.All() {
		if sup.State() == "running" {
			running++
		}
	}
	return c.JSON(fiber.Map{
		"status":          "ok",
		"version":         version.Version,
		"running_servers": running,
	})
}

// wsUpgrade authenticates then upgrades to the realtime hub.
func (a *App) wsUpgrade(c fiber.Ctx) error {
	if c.IsWebSocket() {
		p := a.currentUser(c)
		if p == nil || !p.Enabled {
			return fail(c, 401, "INVALID_TOKEN")
		}
		c.Locals("principal", p)
		c.Locals("lang", "en")
		return c.Next()
	}
	return fiber.ErrUpgradeRequired
}

// handleOpenAPI serves a minimal OpenAPI document (route inventory).
func (a *App) handleOpenAPI(c fiber.Ctx) error {
	doc := map[string]interface{}{
		"openapi": "3.1.0",
		"info": map[string]string{
			"title":   "Servy API",
			"version": version.Version,
		},
		"paths": map[string]interface{}{
			"/api/v1/auth/login":             map[string]string{"post": "Login with username/password/totp"},
			"/api/v1/auth/invalidate_tokens": map[string]string{"post": "Invalidate all session tokens"},
			"/api/v1/users":                  map[string]string{"get": "List users", "post": "Create user"},
			"/api/v1/users/{user_id}":        map[string]string{"get": "Get user", "patch": "Update user", "delete": "Delete user"},
			"/api/v1/users/{user_id}/key":    map[string]string{"get": "List API keys", "post": "Create API key"},
			"/api/v1/users/{user_id}/totp":   map[string]string{"get": "List TOTP", "post": "Create TOTP"},
			"/api/v1/roles":                  map[string]string{"get": "List roles", "post": "Create role"},
			"/api/v1/servers":                map[string]string{"get": "List servers", "post": "Create server (wizard)"},
			"/api/v1/servers/{id}":           map[string]string{"get": "Get server", "patch": "Update config", "delete": "Delete server"},
			"/api/v1/servers/{id}/start":     map[string]string{"post": "Start"},
			"/api/v1/servers/{id}/stop":      map[string]string{"post": "Stop"},
			"/api/v1/servers/{id}/restart":   map[string]string{"post": "Restart"},
			"/api/v1/servers/{id}/kill":      map[string]string{"post": "Kill"},
			"/api/v1/servers/{id}/stdin":     map[string]string{"post": "Send command"},
			"/api/v1/servers/{id}/files":     map[string]string{"get": "List files", "post": "Write file"},
			"/api/v1/servers/{id}/backups":   map[string]string{"get": "List backups", "post": "Create config"},
			"/api/v1/servers/{id}/tasks":     map[string]string{"get": "List schedules", "post": "Create schedule"},
			"/api/v1/panel/config":           map[string]string{"get": "Get panel config", "patch": "Patch config"},
			"/api/v1/panel/logs/{type}":      map[string]string{"get": "Read panel logs"},
			"/api/v1/panel/stats":            map[string]string{"get": "Host stats"},
			"/api/v1/panel/nodes":            map[string]string{"get": "List cluster nodes", "post": "Add node"},
			"/api/v1/panel/steam/catalog":    map[string]string{"get": "SteamCMD game catalog"},
			"/api/v1/panel/steam/catalog/custom":          map[string]string{"get": "List custom SteamCMD manifests", "post": "Add or update a SteamCMD manifest"},
			"/api/v1/panel/steam/catalog/custom/validate": map[string]string{"post": "Validate a SteamCMD manifest"},
			"/api/v1/panel/steam/catalog/custom/{slug}":   map[string]string{"delete": "Remove a custom SteamCMD manifest"},
			"/api/v1/panel/manifests":                     map[string]string{"get": "List server-creation manifests", "post": "Import a server-creation manifest"},
			"/api/v1/panel/manifests/apply":               map[string]string{"post": "Create a server from a manifest"},
			"/api/v1/panel/manifests/{definition_id}":     map[string]string{"get": "Get a server-creation manifest", "delete": "Delete a server-creation manifest"},
			"/ws":                            map[string]string{"get": "WebSocket (subscribe/topics protocol)"},
		},
	}
	return c.JSON(doc)
}
