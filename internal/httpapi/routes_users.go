package httpapi

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/FlameInTheDark/servy/internal/authz"
)

// mountUsers: users, roles, api keys, totp, passkey stubs.
func (a *App) mountUsers(api fiber.Router) {
	api.Get("/users", a.listUsers)
	api.Post("/users", a.createUser)
	api.Get("/users/:user_id", a.getUser)
	api.Patch("/users/:user_id", a.patchUser)
	api.Delete("/users/:user_id", a.deleteUser)
	api.Post("/users/:user_id/password", a.setUserPassword)
	api.Post("/users/:user_id/invalidate_tokens", a.invalidateUserTokens)

	// API keys
	api.Get("/users/:user_id/key", a.listAPIKeys)
	api.Post("/users/:user_id/key", a.createAPIKey)
	api.Get("/users/:user_id/key/:token_id", a.getAPIKey)
	api.Patch("/users/:user_id/key/:token_id", a.patchAPIKey)
	api.Delete("/users/:user_id/key/:token_id", a.deleteAPIKey)

	// TOTP
	api.Get("/users/:user_id/totp", a.listTOTP)
	api.Post("/users/:user_id/totp", a.createTOTP)
	api.Post("/totp/:totp_id/verify", a.verifyTOTP)
	api.Delete("/totp/:totp_id", a.deleteTOTP)
	api.Get("/totp/recovery/renew", a.renewRecovery)

	// permissions
	api.Get("/users/:user_id/permissions", a.userPermissions)

	// roles
	api.Get("/roles", a.listRoles)
	api.Post("/roles", a.createRole)
	api.Get("/roles/:role_id", a.getRole)
	api.Patch("/roles/:role_id", a.patchRole)
	api.Delete("/roles/:role_id", a.deleteRole)
	api.Get("/roles/:role_id/servers", a.roleServers)
	api.Get("/roles/:role_id/users", a.roleUsers)
}

// resolveUserParam maps "@me" to the caller id.
func (a *App) resolveUserParam(c fiber.Ctx, p *authz.Principal) (int64, bool) {
	raw := c.Params("user_id")
	if raw == "@me" {
		return p.UserID, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		_ = fail(c, 400, "NOT_FOUND")
		return 0, false
	}
	return id, true
}

func (a *App) listUsers(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	users, _ := a.DB.ListUsers()
	out := []map[string]interface{}{}
	for _, u := range users {
		out = append(out, toMap(a.Auth.View(u)))
	}
	return ok(c, out)
}

func (a *App) createUser(c fiber.Ctx) error {
	actor := a.requireSuperuser(c)
	if actor == nil {
		return nil
	}
	var req struct {
		Username   string  `json:"username"`
		Password   string  `json:"password"`
		Email      string  `json:"email"`
		Superuser  bool    `json:"superuser"`
		Enabled    bool    `json:"enabled"`
		Lang       string  `json:"lang"`
		Roles      []int64 `json:"roles"`
		PanelPerms string  `json:"panel_permissions"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if req.Lang == "" {
		req.Lang = "en_EN"
	}
	newUser, err := a.Auth.CreateUser(actor, req.Username, req.Password, req.Email, req.Superuser, req.Enabled, req.Lang)
	if err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	if req.PanelPerms != "" {
		a.DB.UpsertUserPanel(newUser.UserID, authz.NormalizePanelMask(req.PanelPerms), -1, 0, 0)
	}
	for _, r := range req.Roles {
		a.DB.AddUserRole(newUser.UserID, r)
	}
	return ok(c, toMap(a.Auth.View(newUser)))
}

func (a *App) getUser(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	uid, valid := a.resolveUserParam(c, p)
	if !valid {
		return nil
	}
	if uid != p.UserID && !p.Superuser {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	targetUser, err := a.DB.GetUserByID(uid)
	if err != nil {
		return fail(c, 404, "USER_NOT_FOUND")
	}
	return ok(c, toMap(a.Auth.View(targetUser)))
}

func (a *App) patchUser(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	uid, valid := a.resolveUserParam(c, p)
	if !valid {
		return nil
	}
	if uid != p.UserID && !p.Superuser {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	var patch map[string]interface{}
	if err := parseBody(c, &patch); err != nil {
		return err
	}
	// non-superusers may only change a safe subset
	if !p.Superuser {
		safe := map[string]interface{}{}
		for _, k := range []string{"lang", "theme", "pfp", "server_order", "dashboard_columns", "cleared_notifs", "email", "preferences"} {
			if v, ok := patch[k]; ok {
				safe[k] = v
			}
		}
		patch = safe
	}
	updated, err := a.Auth.UpdateUser(p, uid, patch)
	if err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, toMap(a.Auth.View(updated)))
}

func (a *App) deleteUser(c fiber.Ctx) error {
	p := a.requireSuperuser(c)
	if p == nil {
		return nil
	}
	uid, _ := a.resolveUserParam(c, p)
	if err := a.Auth.DeleteUser(p, uid); err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, fiber.Map{"deleted": uid})
}

func (a *App) setUserPassword(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	uid, valid := a.resolveUserParam(c, p)
	if !valid {
		return nil
	}
	if uid != p.UserID && !p.Superuser {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if err := a.Auth.SetPassword(p, uid, req.Password); err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, fiber.Map{"password_changed": true})
}

func (a *App) invalidateUserTokens(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	uid, valid := a.resolveUserParam(c, p)
	if !valid {
		return nil
	}
	if uid != p.UserID && !p.Superuser {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	if err := a.Auth.InvalidateTokens(p, uid); err != nil {
		return fail(c, 500, "SERVER_ERROR")
	}
	return ok(c, fiber.Map{"invalidated": true})
}

// ---------------- API keys ----------------

func (a *App) listAPIKeys(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	uid, valid := a.resolveUserParam(c, p)
	if !valid {
		return nil
	}
	if uid != p.UserID && !p.Superuser {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	return ok(c, a.Auth.ListAPIKeys(uid))
}

func (a *App) createAPIKey(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	uid, valid := a.resolveUserParam(c, p)
	if !valid {
		return nil
	}
	if uid != p.UserID && !p.Superuser {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	var req struct {
		Name        string `json:"name"`
		FullAccess  bool   `json:"full_access"`
		ServerPerms string `json:"server_permissions"`
		PanelPerms  string `json:"panel_permissions"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	key, err := a.Auth.CreateAPIKey(p, uid, req.Name, req.FullAccess, req.ServerPerms, req.PanelPerms)
	if err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, key)
}

func (a *App) getAPIKey(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	uid, valid := a.resolveUserParam(c, p)
	if !valid {
		return nil
	}
	tid, err := strconv.ParseInt(c.Params("token_id"), 10, 64)
	if err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	if uid != p.UserID && !p.Superuser {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	key, err := a.Auth.GetAPIKey(uid, tid)
	if err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	return ok(c, key)
}

func (a *App) patchAPIKey(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	uid, valid := a.resolveUserParam(c, p)
	if !valid {
		return nil
	}
	if uid != p.UserID && !p.Superuser {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	tid, err := strconv.ParseInt(c.Params("token_id"), 10, 64)
	if err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	var patch map[string]interface{}
	if err := parseBody(c, &patch); err != nil {
		return err
	}
	key, err := a.Auth.UpdateAPIKey(p, tid, patch)
	if err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, key)
}

func (a *App) deleteAPIKey(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	uid, valid := a.resolveUserParam(c, p)
	if !valid {
		return nil
	}
	if uid != p.UserID && !p.Superuser {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	tid, err := strconv.ParseInt(c.Params("token_id"), 10, 64)
	if err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	if err := a.Auth.DeleteAPIKey(p, tid); err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	return ok(c, fiber.Map{"deleted": tid})
}

// ---------------- TOTP ----------------

func (a *App) listTOTP(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	uid, valid := a.resolveUserParam(c, p)
	if !valid {
		return nil
	}
	if uid != p.UserID && !p.Superuser {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	return ok(c, a.Auth.ListTOTP(uid))
}

func (a *App) createTOTP(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	uid, valid := a.resolveUserParam(c, p)
	if !valid {
		return nil
	}
	if uid != p.UserID && !p.Superuser {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = parseBody(c, &req)
	id, secret, url, err := a.Auth.CreateTOTP(uid, req.Name)
	if err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, fiber.Map{"id": id, "secret": secret, "otpauth_url": url})
}

func (a *App) verifyTOTP(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if err := a.Auth.VerifyTOTP(p, c.Params("totp_id"), req.Code); err != nil {
		return fail(c, 401, "INVALID_MFA")
	}
	return ok(c, fiber.Map{"verified": true})
}

func (a *App) deleteTOTP(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	if err := a.Auth.DeleteTOTP(p, c.Params("totp_id")); err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	return ok(c, fiber.Map{"deleted": true})
}

func (a *App) renewRecovery(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	codes := a.Auth.RenewRecovery(p.UserID)
	return ok(c, fiber.Map{"recovery_codes": codes})
}

// ---------------- permissions ----------------

func (a *App) userPermissions(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	uid, valid := a.resolveUserParam(c, p)
	if !valid {
		return nil
	}
	if uid != p.UserID && !p.Superuser {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	u, err := a.DB.GetUserByID(uid)
	if err != nil {
		return fail(c, 404, "USER_NOT_FOUND")
	}
	view := a.Auth.View(u)
	return ok(c, fiber.Map{
		"panel_permissions":     view.PanelPerms,
		"server_permissions":    view.ServerPerms,
		"limit_server_creation": view.LimitServers,
		"limit_user_creation":   view.LimitUsers,
		"limit_role_creation":   view.LimitRoles,
	})
}

// ---------------- roles ----------------

func (a *App) listRoles(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	roles, _ := a.DB.ListRoles()
	out := []map[string]interface{}{}
	for _, r := range roles {
		out = append(out, a.roleView(r))
	}
	return ok(c, out)
}

func (a *App) roleView(r interface{}) map[string]interface{} {
	type roleRow struct {
		RoleID      int64
		Created     string
		LastUpdate  string
		RoleName    string
		Manager     *int64
		MfaRequired int64
	}
	rr := r.(roleRow)
	return map[string]interface{}{
		"role_id": rr.RoleID, "created": rr.Created, "last_update": rr.LastUpdate,
		"role_name": rr.RoleName, "manager": rr.Manager, "mfa_required": rr.MfaRequired == 1,
	}
}

func (a *App) createRole(c fiber.Ctx) error {
	p := a.requireSuperuser(c)
	if p == nil {
		return nil
	}
	var req struct {
		Name        string            `json:"name"`
		Servers     map[string]string `json:"servers,omitempty"` // serverID -> mask
		MfaRequired bool              `json:"mfa_required"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" {
		return failMsg(c, 400, "INVALID_NAME", "name required")
	}
	if p.LimitRoles >= 0 && !p.Superuser {
		return failMsg(c, 403, "LIMIT_REACHED", "role creation limit reached")
	}
	role, err := a.DB.CreateRole(nowStamp(), nowStamp(), req.Name, nil, b2l(req.MfaRequired))
	if err != nil {
		return failMsg(c, 400, "ALREADY_EXISTS", "role exists")
	}
	for sid, mask := range req.Servers {
		a.DB.SetRoleServer(role.RoleID, sid, mask)
	}
	a.Log.Audit(p.Username, p.UserID, "", "", "Created role "+req.Name)
	return ok(c, a.roleViewFull(role.RoleID))
}

func (a *App) roleViewFull(roleID int64) map[string]interface{} {
	r, err := a.DB.GetRole(roleID)
	if err != nil {
		return map[string]interface{}{}
	}
	view := map[string]interface{}{
		"role_id": r.RoleID, "created": r.Created, "last_update": r.LastUpdate,
		"role_name": r.RoleName, "manager": r.Manager, "mfa_required": r.MfaRequired == 1,
		"servers": map[string]string{},
	}
	servers := map[string]string{}
	rs, _ := a.DB.ListRoleServers(roleID)
	for _, row := range rs {
		servers[row.ServerID] = row.Permissions
	}
	view["servers"] = servers
	return view
}

func (a *App) getRole(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	rid, err := strconv.ParseInt(c.Params("role_id"), 10, 64)
	if err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	if _, err := a.DB.GetRole(rid); err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	return ok(c, a.roleViewFull(rid))
}

func (a *App) patchRole(c fiber.Ctx) error {
	p := a.requireSuperuser(c)
	if p == nil {
		return nil
	}
	rid, err := strconv.ParseInt(c.Params("role_id"), 10, 64)
	if err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	if _, err := a.DB.GetRole(rid); err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	var req struct {
		Name        string            `json:"name,omitempty"`
		Servers     map[string]string `json:"servers,omitempty"`
		MfaRequired *bool             `json:"mfa_required,omitempty"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if req.Name != "" {
		mfa := int64(0)
		if r, err := a.DB.GetRole(rid); err == nil {
			mfa = r.MfaRequired
		}
		if req.MfaRequired != nil {
			mfa = b2l(*req.MfaRequired)
		}
		a.DB.UpdateRole(nowStamp(), req.Name, nil, mfa, rid)
	} else if req.MfaRequired != nil {
		r, _ := a.DB.GetRole(rid)
		a.DB.UpdateRole(nowStamp(), r.RoleName, nil, b2l(*req.MfaRequired), rid)
	}
	if req.Servers != nil {
		a.DB.ClearRoleServers(rid)
		for sid, mask := range req.Servers {
			a.DB.SetRoleServer(rid, sid, mask)
		}
	}
	a.Log.Audit(p.Username, p.UserID, "", "", "Updated role "+c.Params("role_id"))
	return ok(c, a.roleViewFull(rid))
}

func (a *App) deleteRole(c fiber.Ctx) error {
	p := a.requireSuperuser(c)
	if p == nil {
		return nil
	}
	rid, err := strconv.ParseInt(c.Params("role_id"), 10, 64)
	if err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	if err := a.DB.DeleteRole(rid); err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	a.Log.Audit(p.Username, p.UserID, "", "", "Deleted role "+c.Params("role_id"))
	return ok(c, fiber.Map{"deleted": rid})
}

func (a *App) roleServers(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	rid, err := strconv.ParseInt(c.Params("role_id"), 10, 64)
	if err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	rs, _ := a.DB.ListRoleServers(rid)
	out := map[string]string{}
	for _, row := range rs {
		out[row.ServerID] = row.Permissions
	}
	return ok(c, out)
}

func (a *App) roleUsers(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	rid, err := strconv.ParseInt(c.Params("role_id"), 10, 64)
	if err != nil {
		return fail(c, 404, "NOT_FOUND")
	}
	users, _ := a.DB.ListUsersWithRole(rid)
	out := []map[string]interface{}{}
	for _, u := range users {
		out = append(out, map[string]interface{}{"user_id": u.UserID, "username": u.Username})
	}
	return ok(c, out)
}

// toMap converts a UserView into a generic map (response builder).
func toMap(v interface{}) map[string]interface{} {
	b, _ := jsonMarshal(v)
	var m map[string]interface{}
	jsonUnmarshal(b, &m)
	return m
}

func nowStamp() string { return databaseNow() }
