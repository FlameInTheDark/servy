package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/FlameInTheDark/servy/internal/authz"
	"github.com/FlameInTheDark/servy/internal/database"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

// ---------------- TOTP ----------------

// CreateTOTP provisions a new TOTP method for a user (unverified until Verify).
func (s *Service) CreateTOTP(userID int64, name string) (id, secret, otpURL string, err error) {
	u, err := s.db.GetUserByID(userID)
	if err != nil {
		return "", "", "", ErrNotFound
	}
	if name == "" {
		name = "TOTP"
	}
	secret, otpURL, err = GenerateTOTPSecret(u.Username)
	if err != nil {
		return "", "", "", err
	}
	id = RandomToken(8)
	row, err := s.db.CreateTOTP(id, name, userID, secret)
	if err != nil {
		return "", "", "", err
	}
	s.log.Audit("user", userID, "", "", fmt.Sprintf("Created TOTP method %q", name))
	return row.ID, row.TotpSecret, otpURL, nil
}

// VerifyTOTP confirms a provisioned TOTP with a live code.
func (s *Service) VerifyTOTP(actor *authz.Principal, totpID, code string) error {
	row, err := s.db.GetTOTP(totpID)
	if err != nil {
		return ErrNotFound
	}
	if !VerifyTOTP(row.TotpSecret, code, s.cfg.Get().EnableOTPSkew) {
		return ErrInvalidMFA
	}
	// provision recovery codes on first successful verify if none exist
	if recs, _ := s.db.ListRecoveryForUser(row.UserID); len(recs) == 0 {
		s.RenewRecovery(row.UserID)
	}
	s.log.Audit(actorUsername(actor), actorUserID(actor), "", "", "Verified TOTP method")
	return nil
}

// ListTOTP returns the user's TOTP methods (secrets excluded).
func (s *Service) ListTOTP(userID int64) []map[string]interface{} {
	rows, _ := s.db.ListTOTPForUser(userID)
	out := []map[string]interface{}{}
	for _, r := range rows {
		out = append(out, map[string]interface{}{"id": r.ID, "name": r.Name, "user_id": r.UserID})
	}
	return out
}

// DeleteTOTP removes a TOTP method.
func (s *Service) DeleteTOTP(actor *authz.Principal, totpID string) error {
	row, err := s.db.GetTOTP(totpID)
	if err != nil {
		return ErrNotFound
	}
	if err := s.db.DeleteTOTP(totpID); err != nil {
		return err
	}
	s.log.Audit(actorUsername(actor), actorUserID(actor), "", "", fmt.Sprintf("Deleted TOTP method %q", row.Name))
	return nil
}

// RenewRecovery regenerates the 6 recovery codes for a user, returning them once.
func (s *Service) RenewRecovery(userID int64) []string {
	s.db.DeleteRecoveryForUser(userID)
	codes := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		b := make([]byte, 5)
		rand.Read(b)
		code := hex.EncodeToString(b)
		s.db.InsertRecovery(RandomToken(8), userID, code)
		codes = append(codes, code)
	}
	return codes
}

// ---------------- API keys ----------------

// APIKeyView is the safe representation; the full token is only shown at creation.
type APIKeyView struct {
	TokenID           int64  `json:"token_id"`
	Name              string `json:"name"`
	Created           string `json:"created"`
	UserID            int64  `json:"user_id"`
	ServerPermissions string `json:"server_permissions"`
	PanelPermissions  string `json:"panel_permissions"`
	FullAccess        bool   `json:"full_access"`
	Token             string `json:"token,omitempty"`
}

// CreateAPIKey issues a new key for a user. The bearer token "id.secret" is
// returned exactly once — only the argon2 hash of the secret is stored.
func (s *Service) CreateAPIKey(actor *authz.Principal, userID int64, name string, fullAccess bool, serverPerms, panelPerms string) (APIKeyView, error) {
	if name == "" {
		return APIKeyView{}, errors.New("INVALID_NAME")
	}
	secret := RandomToken(32)
	hash, err := HashPassword(secret)
	if err != nil {
		return APIKeyView{}, err
	}
	row, err := s.db.CreateAPIKey(name, database.Now(), userID,
		authz.NormalizeServerMask(serverPerms), authz.NormalizePanelMask(panelPerms), b2i(fullAccess), hash, "")
	if err != nil {
		return APIKeyView{}, err
	}
	token := fmt.Sprintf("%d.%s", row.TokenID, secret)
	s.db.UpdateAPIKey(row.Name, row.ServerPermissions, row.PanelPermissions, row.FullAccess, row.TokenID)
	// store short prefix for identification in listings
	s.db.UpdateAPIKey(name, authz.NormalizeServerMask(serverPerms), authz.NormalizePanelMask(panelPerms), b2i(fullAccess), row.TokenID)
	s.log.Audit(actorUsername(actor), actorUserID(actor), "", "", fmt.Sprintf("Created API key %q", name))
	return APIKeyView{
		TokenID: row.TokenID, Name: name, Created: row.Created, UserID: userID,
		ServerPermissions: authz.NormalizeServerMask(serverPerms),
		PanelPermissions:  authz.NormalizePanelMask(panelPerms),
		FullAccess:        fullAccess, Token: token,
	}, nil
}

// ListAPIKeys lists a user's keys.
func (s *Service) ListAPIKeys(userID int64) []APIKeyView {
	rows, _ := s.db.ListAPIKeysForUser(userID)
	out := []APIKeyView{}
	for _, r := range rows {
		out = append(out, APIKeyView{
			TokenID: r.TokenID, Name: r.Name, Created: r.Created, UserID: r.UserID,
			ServerPermissions: r.ServerPermissions, PanelPermissions: r.PanelPermissions,
			FullAccess: r.FullAccess == 1,
		})
	}
	return out
}

// GetAPIKey fetches one key.
func (s *Service) GetAPIKey(userID, tokenID int64) (APIKeyView, error) {
	r, err := s.db.GetAPIKey(tokenID)
	if err != nil || r.UserID != userID {
		return APIKeyView{}, ErrNotFound
	}
	return APIKeyView{
		TokenID: r.TokenID, Name: r.Name, Created: r.Created, UserID: r.UserID,
		ServerPermissions: r.ServerPermissions, PanelPermissions: r.PanelPermissions,
		FullAccess: r.FullAccess == 1,
	}, nil
}

// UpdateAPIKey patches name/masks/full_access.
func (s *Service) UpdateAPIKey(actor *authz.Principal, tokenID int64, patch map[string]interface{}) (APIKeyView, error) {
	r, err := s.db.GetAPIKey(tokenID)
	if err != nil {
		return APIKeyView{}, ErrNotFound
	}
	if v, ok := patch["name"].(string); ok && v != "" {
		r.Name = v
	}
	if v, ok := patch["server_permissions"].(string); ok {
		r.ServerPermissions = authz.NormalizeServerMask(v)
	}
	if v, ok := patch["panel_permissions"].(string); ok {
		r.PanelPermissions = authz.NormalizePanelMask(v)
	}
	if v, ok := patch["full_access"]; ok {
		r.FullAccess = b2i(truthyI(v))
	}
	if err := s.db.UpdateAPIKey(r.Name, r.ServerPermissions, r.PanelPermissions, r.FullAccess, tokenID); err != nil {
		return APIKeyView{}, err
	}
	s.log.Audit(actorUsername(actor), actorUserID(actor), "", "", fmt.Sprintf("Updated API key %q", r.Name))
	return s.GetAPIKey(r.UserID, tokenID)
}

// DeleteAPIKey removes a key.
func (s *Service) DeleteAPIKey(actor *authz.Principal, tokenID int64) error {
	if _, err := s.db.GetAPIKey(tokenID); err != nil {
		return ErrNotFound
	}
	if err := s.db.DeleteAPIKey(tokenID); err != nil {
		return err
	}
	s.log.Audit(actorUsername(actor), actorUserID(actor), "", "", "Deleted API key")
	return nil
}

// ---------------- Anti-lockout ----------------

// AntiLockoutUser creates a temporary superuser (password random, printed to
// default-creds.txt) so an admin can recover a panel with no valid accounts.
// The user is deleted on next successful boot (parity with the legacy panel).
func (s *Service) AntiLockoutUser(dataDir string) (username, password string, err error) {
	username = "anti-lockout-" + RandomToken(3)
	password = RandomToken(12)
	hash, err := HashPassword(password)
	if err != nil {
		return "", "", err
	}
	now := database.Now()
	_, err = s.db.CreateUser(now, now, username, hash, nil, 1, 1, "en_EN", "2000-01-01T00:00:00Z", nil, 1, nil, nil, nil, nil, 0, nil, nil)
	if err != nil {
		return "", "", err
	}
	// persist credentials for the operator
	path := dataDir + "/default-creds.txt"
	_ = writeFileAppend(path, fmt.Sprintf("Anti-lockout user: %s\nPassword: %s\nCreated: %s\n", username, password, now))
	s.log.Get("auth").Warning("ANTI-LOCKOUT USER CREATED: %s (see default-creds.txt; deleted on next boot)", username)
	return username, password, nil
}

// RemoveAntiLockoutUsers deletes any leftover anti-lockout users at boot.
func (s *Service) RemoveAntiLockoutUsers() {
	users, _ := s.db.ListUsers()
	for _, u := range users {
		if u.Preparing == 1 && strings.HasPrefix(u.Username, "anti-lockout-") {
			s.db.DeleteUser(u.UserID)
			s.log.Get("auth").Warning("Removed anti-lockout user %s", u.Username)
		}
	}
}

// HasUsers reports whether any user exists (setup wizard gate).
func (s *Service) HasUsers() bool { return s.CountUsers() > 0 }

func writeFileAppend(path, content string) error {
	f, err := openAppend(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// SeedFirstSetup creates the initial superuser (setup wizard / CLI setup).
func (s *Service) SeedFirstSetup(username, password, lang string) (gen.User, error) {
	p := &authz.Principal{Superuser: true, Username: "setup"}
	u, err := s.CreateUser(p, username, password, "", true, true, lang)
	if err != nil {
		return gen.User{}, err
	}
	// full panel permissions, unlimited limits
	s.db.UpsertUserPanel(u.UserID, "111", -1, -1, -1)
	return s.db.GetUserByID(u.UserID)
}

// TouchUserLogin updates login stamp.
func (s *Service) TouchUserLogin(userID int64, ip string) {
	now := database.Now()
	s.db.TouchUserLogin(&now, &ip, now, userID)
}

var _ = time.Now

// openAppend is split out to keep the auth file tidy.
func openAppend(path string) (*osFile, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// derefInt returns *v or fallback when nil.
func derefInt(v *int64, fallback int) int {
	if v == nil {
		return fallback
	}
	return int(*v)
}
