package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/FlameInTheDark/servy/internal/authz"
	"github.com/FlameInTheDark/servy/internal/config"
	"github.com/FlameInTheDark/servy/internal/database"
	"github.com/FlameInTheDark/servy/internal/logging"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

// Error taxonomy (matches API error contract).
var (
	ErrBadCredentials  = errors.New("BAD_PASSWORD")
	ErrAccountDisabled = errors.New("ACCOUNT_DISABLED")
	ErrMFARequired     = errors.New("MFA_REQUIRED")
	ErrInvalidMFA      = errors.New("INVALID_MFA")
	ErrLockedOut       = errors.New("LOCKED_OUT")
	ErrNotFound        = errors.New("NOT_FOUND")
	ErrAlreadyExists   = errors.New("ALREADY_EXISTS")
	ErrNotAuthorized   = errors.New("NOT_AUTHORIZED")
	ErrLastSuperuser   = errors.New("CANNOT_DELETE_LAST_SUPERUSER")
	ErrInvalidPassword = errors.New("INVALID_PASSWORD")
)

// Service implements user management, login and principal resolution.
type Service struct {
	db       *database.Queries
	log      *logging.Manager
	cfg      *config.Manager
	secret   func() string
	mu       sync.Mutex
	attempts map[string]*attemptWindow
}

type attemptWindow struct {
	fails    int
	lockedTo time.Time
	lastFail time.Time
}

// NewService builds the auth service. secretFn returns the JWT signing secret.
func NewService(db *database.Queries, log *logging.Manager, cfg *config.Manager, secretFn func() string) *Service {
	return &Service{db: db, log: log, cfg: cfg, secret: secretFn, attempts: map[string]*attemptWindow{}}
}

// ---------------- Login ----------------

// Login authenticates a user with password (+ optional TOTP), issuing a JWT.
func (s *Service) Login(username, password, totpCode, ip string) (token string, userID int64, err error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", 0, ErrBadCredentials
	}
	if locked, wait := s.isLocked(username); locked {
		s.log.Get("auth").Warning("login blocked (locked): user=%s ip=%s wait=%s", username, ip, wait)
		return "", 0, ErrLockedOut
	}
	u, err := s.db.GetUserByUsername(username)
	if err != nil {
		s.recordFail(username)
		return "", 0, ErrBadCredentials
	}
	if u.Enabled != 1 {
		return "", 0, ErrAccountDisabled
	}
	if !VerifyPassword(password, u.Password) {
		s.recordFail(username)
		s.log.Get("auth").Warning("bad password: user=%s ip=%s", username, ip)
		return "", 0, ErrBadCredentials
	}
	// TOTP enforcement (superMFA config forces superusers to have + use MFA)
	totps, _ := s.db.ListTOTPForUser(u.UserID)
	needMFA := len(totps) > 0
	if u.Superuser == 1 && s.cfg.Get().SuperMFA && len(totps) == 0 {
		// superuser without MFA configured: refuse login entirely
		s.log.Get("auth").Error("superMFA enabled but superuser %s has no MFA", username)
		return "", 0, ErrMFARequired
	}
	if needMFA {
		if totpCode == "" {
			return "", 0, ErrMFARequired
		}
		if !s.verifyAnyTOTP(u.UserID, totpCode) {
			s.recordFail(username)
			return "", 0, ErrInvalidMFA
		}
	}
	s.clearFails(username)
	validFrom, _ := time.Parse(time.RFC3339, u.ValidTokensFrom)
	tok, err := IssueToken(s.secret(), u.UserID, u.Username, 0, s.cfg.Get().CookieExpire, validFrom)
	if err != nil {
		return "", 0, err
	}
	now := database.Now()
	s.db.TouchUserLogin(&now, &ip, now, u.UserID)
	s.log.Audit(u.Username, u.UserID, "", ip, "Logged in successfully")
	return tok, u.UserID, nil
}

func (s *Service) verifyAnyTOTP(userID int64, code string) bool {
	totps, err := s.db.ListTOTPForUser(userID)
	if err != nil {
		return false
	}
	skew := s.cfg.Get().EnableOTPSkew
	for _, t := range totps {
		if VerifyTOTP(t.TotpSecret, code, skew) {
			return true
		}
	}
	// recovery codes are also accepted as one-shot codes
	recs, _ := s.db.ListRecoveryForUser(userID)
	for i, r := range recs {
		if subtleEqual(r.RecoverySecret, code) {
			_ = i
			return true
		}
	}
	return false
}

func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		// constant-ish compare to avoid trivial timing leaks
		for i := 0; i < len(a)+len(b); i++ {
			_ = a[0]
		}
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func (s *Service) isLocked(username string) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.attempts[username]
	if !ok {
		return false, 0
	}
	if w.lockedTo.After(time.Now()) {
		return true, time.Until(w.lockedTo)
	}
	if w.lockedTo.IsZero() && time.Since(w.lastFail) > 10*time.Minute {
		delete(s.attempts, username)
	}
	return false, 0
}

func (s *Service) recordFail(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.attempts[username]
	if !ok || time.Since(w.lastFail) > 10*time.Minute {
		w = &attemptWindow{}
		s.attempts[username] = w
	}
	w.lastFail = time.Now()
	w.fails++
	if w.fails >= s.cfg.Get().MaxLoginAttempts {
		w.lockedTo = time.Now().Add(10 * time.Minute)
		s.log.Get("auth").Warning("account %s locked for 10 minutes after %d failed attempts", username, w.fails)
	}
}

func (s *Service) clearFails(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attempts, username)
}

// ---------------- Principals ----------------

// PrincipalForUser assembles the effective permission set of a user.
func (s *Service) PrincipalForUser(u gen.User) *authz.Principal {
	p := authz.NewPrincipal()
	p.UserID = u.UserID
	p.Username = u.Username
	p.Superuser = u.Superuser == 1
	p.Enabled = u.Enabled == 1
	uc, err := s.db.GetUserPanel(u.UserID)
	if err == nil {
		p.PanelPerms = authz.NormalizePanelMask(uc.Permissions)
		p.LimitServers = uc.LimitServerCreation
		p.LimitUsers = uc.LimitUserCreation
		p.LimitRoles = uc.LimitRoleCreation
	}
	rows, err := s.db.ListUserServerPerms(u.UserID)
	if err == nil {
		for _, r := range rows {
			p.AddServerPerm(r.ServerID, r.Permissions)
		}
	}
	return p
}

// PrincipalForToken resolves a JWT (session or API-key-embedded) to a principal.
func (s *Service) PrincipalForToken(token string) (*authz.Principal, error) {
	claims, err := ParseToken(s.secret(), token)
	if err != nil {
		return nil, errors.New("INVALID_TOKEN")
	}
	if claims.APIKeyID != 0 {
		key, err := s.db.GetAPIKey(claims.APIKeyID)
		if err != nil {
			return nil, errors.New("INVALID_TOKEN")
		}
		return s.principalForKey(key)
	}
	u, err := s.db.GetUserByID(claims.UserID)
	if err != nil {
		return nil, errors.New("INVALID_TOKEN")
	}
	if u.Enabled != 1 {
		return nil, ErrAccountDisabled
	}
	if claims.IssuedAt != nil {
		vtf, _ := time.Parse(time.RFC3339, u.ValidTokensFrom)
		if claims.IssuedAt.Time.Before(vtf) {
			return nil, errors.New("INVALID_TOKEN")
		}
	}
	p := s.PrincipalForUser(u)
	if p.APIKeyID == 0 {
		p.APIKeyID = 0
	}
	return p, nil
}

// PrincipalForBearer accepts either a session JWT or a raw API key (id.secret).
func (s *Service) PrincipalForBearer(token string) (*authz.Principal, error) {
	// try raw api key first: "<int>.<secret>"
	if i := strings.IndexByte(token, '.'); i > 0 && i < 12 {
		var id int64
		if _, err := fmt.Sscanf(token[:i], "%d", &id); err == nil && id > 0 {
			key, err := s.db.GetAPIKey(id)
			if err == nil && VerifyPassword(token[i+1:], key.TokenHash) {
				return s.principalForKey(key)
			}
		}
	}
	return s.PrincipalForToken(token)
}

// principalForKey intersects key masks with the owning user's effective perms.
func (s *Service) principalForKey(key gen.ApiKey) (*authz.Principal, error) {
	u, err := s.db.GetUserByID(key.UserID)
	if err != nil {
		return nil, errors.New("INVALID_TOKEN")
	}
	if u.Enabled != 1 {
		return nil, ErrAccountDisabled
	}
	p := s.PrincipalForUser(u)
	if key.FullAccess == 1 {
		p.APIKeyID = key.TokenID
		p.APIKeyFull = true
		return p, nil
	}
	// key path: server perms = key mask ∩ user grants; panel perms = key mask ∩ user mask
	p.APIKeyID = key.TokenID
	p.APIKeyFull = false
	keyServer := authz.NormalizeServerMask(key.ServerPermissions)
	p.PanelPerms = "000"
	if authz.HasPanelPerm(authz.NormalizePanelMask(key.PanelPermissions), authz.PanelServerCreation) && p.CanPanel(authz.PanelServerCreation) {
		p.PanelPerms = authz.SetPanelPerm(p.PanelPerms, authz.PanelServerCreation, true)
	}
	if authz.HasPanelPerm(authz.NormalizePanelMask(key.PanelPermissions), authz.PanelUserConfig) && p.CanPanel(authz.PanelUserConfig) {
		p.PanelPerms = authz.SetPanelPerm(p.PanelPerms, authz.PanelUserConfig, true)
	}
	if authz.HasPanelPerm(authz.NormalizePanelMask(key.PanelPermissions), authz.PanelRolesConfig) && p.CanPanel(authz.PanelRolesConfig) {
		p.PanelPerms = authz.SetPanelPerm(p.PanelPerms, authz.PanelRolesConfig, true)
	}
	// rebuild server perms: only servers the user can access AND key grants
	filtered := authz.NewPrincipal()
	filtered.UserID = p.UserID
	filtered.Username = p.Username
	filtered.Superuser = false
	filtered.Enabled = p.Enabled
	filtered.APIKeyID = key.TokenID
	filtered.PanelPerms = p.PanelPerms
	filtered.LimitServers = 0
	filtered.LimitUsers = 0
	filtered.LimitRoles = 0
	for _, sid := range p.ServerIDs() {
		if !p.HasServerAccess(sid) {
			continue
		}
		mask := p.ServerMaskFor(sid)
		for bit := 0; bit < 8; bit++ {
			if !authz.HasServerPerm(keyServer, bit) {
				mask = authz.SetServerPerm(mask, bit, false)
			}
		}
		filtered.AddServerPerm(sid, mask)
	}
	return filtered, nil
}

// ---------------- Users CRUD ----------------

// UserView is the API-safe user representation.
type UserView struct {
	UserID              int64   `json:"user_id"`
	Created             string  `json:"created"`
	LastLogin           string  `json:"last_login,omitempty"`
	LastUpdate          string  `json:"last_update"`
	LastIP              string  `json:"last_ip,omitempty"`
	Username            string  `json:"username"`
	Email               string  `json:"email,omitempty"`
	Enabled             bool    `json:"enabled"`
	Superuser           bool    `json:"superuser"`
	Lang                string  `json:"lang"`
	ValidTokensFrom     string  `json:"valid_tokens_from"`
	ServerOrder         string  `json:"server_order,omitempty"`
	Preparing           bool    `json:"preparing"`
	Pfp                 string  `json:"pfp,omitempty"`
	Theme               string  `json:"theme,omitempty"`
	ClearedNotifs       string  `json:"cleared_notifs,omitempty"`
	DisablePasswordAuth bool    `json:"disable_password_auth"`
	DashboardColumns    int     `json:"dashboard_columns"`
	Roles               []int64 `json:"roles"`
	// effective permissions
	PanelPerms   string            `json:"panel_permissions"`
	LimitServers int64             `json:"limit_server_creation"`
	LimitUsers   int64             `json:"limit_user_creation"`
	LimitRoles   int64             `json:"limit_role_creation"`
	ServerPerms  map[string]string `json:"server_permissions"`
}

// View converts a DB user row to an API view with roles & permissions.
func (s *Service) View(u gen.User) UserView {
	v := UserView{
		UserID: u.UserID, Created: u.Created, LastUpdate: u.LastUpdate,
		Username: u.Username, Enabled: u.Enabled == 1, Superuser: u.Superuser == 1,
		Lang: u.Lang, ValidTokensFrom: u.ValidTokensFrom, Preparing: u.Preparing == 1, DashboardColumns: derefInt(u.DashboardColumns, 4),
		DisablePasswordAuth: u.DisablePasswordAuth == 1,
		Roles:               []int64{}, ServerPerms: map[string]string{},
	}
	if u.LastLogin != nil {
		v.LastLogin = *u.LastLogin
	}
	if u.LastIp != nil {
		v.LastIP = *u.LastIp
	}
	if u.Email != nil {
		v.Email = *u.Email
	}
	if u.ServerOrder != nil {
		v.ServerOrder = *u.ServerOrder
	}
	if u.Pfp != nil {
		v.Pfp = *u.Pfp
	}
	if u.Theme != nil {
		v.Theme = *u.Theme
	}
	if u.ClearedNotifs != nil {
		v.ClearedNotifs = *u.ClearedNotifs
	}
	ids, _ := s.db.GetUserRoleIDs(u.UserID)
	v.Roles = ids
	uc, err := s.db.GetUserPanel(u.UserID)
	if err == nil {
		v.PanelPerms = authz.NormalizePanelMask(uc.Permissions)
		v.LimitServers = uc.LimitServerCreation
		v.LimitUsers = uc.LimitUserCreation
		v.LimitRoles = uc.LimitRoleCreation
	}
	rows, _ := s.db.ListUserServerPerms(u.UserID)
	for _, r := range rows {
		if cur, ok := v.ServerPerms[r.ServerID]; ok {
			v.ServerPerms[r.ServerID] = authz.MergeServerMasks(cur, r.Permissions)
		} else {
			v.ServerPerms[r.ServerID] = authz.NormalizeServerMask(r.Permissions)
		}
	}
	return v
}

// CreateUser validates and inserts a user. Enforces creator quantity limits.
func (s *Service) CreateUser(actor *authz.Principal, username, password, email string, superuser, enabled bool, lang string) (gen.User, error) {
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(username) > 20 {
		return gen.User{}, errors.New("INVALID_USERNAME")
	}
	if len(password) < 6 {
		return gen.User{}, ErrInvalidPassword
	}
	if existing, err := s.db.GetUserByUsername(username); err == nil && existing.UserID != 0 {
		return gen.User{}, ErrAlreadyExists
	}
	if actor != nil && !actor.Superuser {
		if !actor.CanPanel(authz.PanelServerCreation) {
			// non-superuser creators need USER_CONFIG
		}
		if !actor.CanPanel(authz.PanelUserConfig) {
			return gen.User{}, ErrNotAuthorized
		}
		if actor.LimitUsers >= 0 {
			if int64(len(s.usersManagedBy(actor.UserID))) >= actor.LimitUsers {
				return gen.User{}, errors.New("LIMIT_REACHED")
			}
		}
		superuser = false
	}
	hash, err := HashPassword(password)
	if err != nil {
		return gen.User{}, err
	}
	now := database.Now()
	u, err := s.db.CreateUser(now, now, username, hash, strPtr(email), b2i(enabled), b2i(superuser && (actor == nil || actor.Superuser)), lang, "2000-01-01T00:00:00Z", nil, 0, nil, nil, nil, nil, 0, int64Ptr(4), nil)
	if err != nil {
		return gen.User{}, err
	}
	// default panel perms row
	s.db.UpsertUserPanel(u.UserID, "000", -1, 0, 0)
	s.log.Audit(actorUsername(actor), actorUserID(actor), "", "", fmt.Sprintf("Created user %s (superuser=%v)", username, superuser))
	return u, nil
}

func (s *Service) usersManagedBy(managerID int64) []gen.User {
	out := []gen.User{}
	users, _ := s.db.ListUsers()
	for _, u := range users {
		if u.Manager != nil && *u.Manager == managerID {
			out = append(out, u)
		}
	}
	return out
}

// UpdateUser applies a partial update from the API.
func (s *Service) UpdateUser(actor *authz.Principal, userID int64, patch map[string]interface{}) (gen.User, error) {
	u, err := s.db.GetUserByID(userID)
	if err != nil {
		return gen.User{}, ErrNotFound
	}
	if v, ok := patch["username"].(string); ok && strings.TrimSpace(v) != "" && v != u.Username {
		if other, err := s.db.GetUserByUsername(v); err == nil && other.UserID != u.UserID {
			return gen.User{}, ErrAlreadyExists
		}
		u.Username = strings.TrimSpace(v)
	}
	if v, ok := patch["email"]; ok {
		u.Email = strPtrOrNil(v)
	}
	if v, ok := patch["enabled"]; ok {
		u.Enabled = b2i(truthyI(v))
	}
	if v, ok := patch["superuser"]; ok && actor != nil && actor.Superuser {
		u.Superuser = b2i(truthyI(v))
	}
	if v, ok := patch["lang"].(string); ok && v != "" {
		u.Lang = v
	}
	if v, ok := patch["server_order"]; ok {
		u.ServerOrder = strPtrOrNil(v)
	}
	if v, ok := patch["pfp"]; ok {
		u.Pfp = strPtrOrNil(v)
	}
	if v, ok := patch["theme"].(string); ok {
		u.Theme = &v
	}
	if v, ok := patch["preparing"]; ok {
		u.Preparing = b2i(truthyI(v))
	}
	if v, ok := patch["disable_password_auth"]; ok {
		u.DisablePasswordAuth = b2i(truthyI(v))
	}
	if v, ok := patch["dashboard_columns"]; ok {
		if f, ok := v.(float64); ok {
			u.DashboardColumns = int64Ptr(int64(f))
		}
	}
	if v, ok := patch["cleared_notifs"]; ok {
		u.ClearedNotifs = strPtrOrNil(v)
	}
	u.LastUpdate = database.Now()
	if err := s.db.UpdateUser(u.LastUpdate, u.Username, u.Email, u.Enabled, u.Superuser, u.Lang, u.ServerOrder,
		u.Preparing, u.Manager, u.Pfp, u.Theme, u.ClearedNotifs, u.DisablePasswordAuth, u.DashboardColumns, u.Preferences, u.UserID); err != nil {
		return gen.User{}, err
	}
	// panel permissions + limits
	if actor != nil && actor.Superuser {
		if pm, ok := patch["panel_permissions"].(string); ok {
			ls, lu, lr := s.existingLimits(userID)
			s.db.UpsertUserPanel(userID, authz.NormalizePanelMask(pm), ls, lu, lr)
		}
		for key, bit := range map[string]int{"limit_server_creation": 0, "limit_user_creation": 1, "limit_role_creation": 2} {
			if v, ok := patch[key]; ok {
				ls, lu, lr := s.existingLimits(userID)
				limits := []int64{ls, lu, lr}
				n := int64(0)
				switch x := v.(type) {
				case float64:
					n = int64(x)
				case int:
					n = int64(x)
				case int64:
					n = x
				}
				limits[bit] = n
				pm, _ := s.db.GetUserPanel(userID)
				s.db.UpsertUserPanel(userID, pm.Permissions, limits[0], limits[1], limits[2])
			}
		}
	}
	// roles
	if rolesRaw, ok := patch["roles"]; ok {
		if ids, ok := toInt64Slice(rolesRaw); ok {
			s.db.ClearUserRoles(userID)
			for _, rid := range ids {
				s.db.AddUserRole(userID, rid)
			}
		}
	}
	s.log.Audit(actorUsername(actor), actorUserID(actor), "", "", fmt.Sprintf("Updated user %s", u.Username))
	return s.db.GetUserByID(userID)
}

func (s *Service) existingLimits(userID int64) (a, b, c int64) {
	uc, err := s.db.GetUserPanel(userID)
	if err != nil {
		return -1, 0, 0
	}
	return uc.LimitServerCreation, uc.LimitUserCreation, uc.LimitRoleCreation
}

// SetPassword changes a user password and invalidates existing tokens.
func (s *Service) SetPassword(actor *authz.Principal, userID int64, newPassword string) error {
	if len(newPassword) < 6 {
		return ErrInvalidPassword
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	now := database.Now()
	if err := s.db.UpdateUserPassword(hash, now, now, userID); err != nil {
		return err
	}
	s.log.Audit(actorUsername(actor), actorUserID(actor), "", "", "Password changed; tokens invalidated")
	return nil
}

// DeleteUser removes a user, guarding the last enabled superuser.
func (s *Service) DeleteUser(actor *authz.Principal, userID int64) error {
	u, err := s.db.GetUserByID(userID)
	if err != nil {
		return ErrNotFound
	}
	if u.Superuser == 1 {
		if n, _ := s.db.CountSuperusers(); n <= 1 {
			return ErrLastSuperuser
		}
	}
	if err := s.db.DeleteUser(userID); err != nil {
		return err
	}
	s.log.Audit(actorUsername(actor), actorUserID(actor), "", "", fmt.Sprintf("Deleted user %s", u.Username))
	return nil
}

// InvalidateTokens bumps valid_tokens_from so all issued JWTs stop working.
func (s *Service) InvalidateTokens(actor *authz.Principal, userID int64) error {
	now := database.Now()
	if err := s.db.SetValidTokensFrom(now, now, userID); err != nil {
		return err
	}
	s.log.Audit(actorUsername(actor), actorUserID(actor), "", "", "Invalidated all user tokens")
	return nil
}

// CountUsers returns total user count.
func (s *Service) CountUsers() int64 {
	n, _ := s.db.CountUsers()
	return n
}

// GetSettings fetches panel_settings singleton (used by httpapi).
func (s *Service) GetSettings() (gen.PanelSetting, error) {
	st, err := s.db.GetSettings()
	if err == nil {
		return st, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return gen.PanelSetting{ID: 1}, nil
	}
	return st, err
}

// --- helpers ---

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
func truthyI(v interface{}) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case int:
		return x != 0
	case string:
		return x == "true" || x == "1"
	}
	return false
}
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func strPtrOrNil(v interface{}) *string {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		return strPtr(s)
	}
	return nil
}
func toInt64Slice(v interface{}) ([]int64, bool) {
	switch x := v.(type) {
	case []int64:
		return x, true
	case []interface{}:
		out := make([]int64, 0, len(x))
		for _, e := range x {
			switch n := e.(type) {
			case float64:
				out = append(out, int64(n))
			case int:
				out = append(out, int64(n))
			case int64:
				out = append(out, n)
			default:
				return nil, false
			}
		}
		return out, true
	}
	return nil, false
}
func actorUsername(p *authz.Principal) string {
	if p == nil {
		return "system"
	}
	return p.Username
}
func actorUserID(p *authz.Principal) int64 {
	if p == nil {
		return 0
	}
	return p.UserID
}

// int64Ptr boxes an int64.
func int64Ptr(v int64) *int64 { return &v }
