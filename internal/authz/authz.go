// Package authz implements the permission model: per-server 8-bit masks,
// panel-level 3-bit masks and per-user quantity limits. Bit order and semantics
// are identical to the legacy panel model (EnumPermissionsServer / EnumPermissionsPanel).
package authz

import (
	"strings"
)

// Server permission bits (index = bit position in the 8-char mask).
const (
	ServerCommands = iota
	ServerTerminal
	ServerLogs
	ServerSchedule
	ServerBackup
	ServerFiles
	ServerConfig
	ServerPlayers
)

// Panel permission bits (3-char mask).
const (
	PanelServerCreation = iota
	PanelUserConfig
	PanelRolesConfig
)

// ServerPermNames maps API names to bit indices (stable order).
var ServerPermNames = []string{
	"COMMANDS", "TERMINAL", "LOGS", "SCHEDULE", "BACKUP", "FILES", "CONFIG", "PLAYERS",
}

// PanelPermNames maps API names to bit indices.
var PanelPermNames = []string{"SERVER_CREATION", "USER_CONFIG", "ROLES_CONFIG"}

// NormalizeServerMask returns a canonical 8-char mask, accepting
// '0100...' strings or JSON boolean-dict form on a best-effort basis.
func NormalizeServerMask(mask string) string {
	if len(mask) < 8 {
		if len(mask) == 0 {
			return "00000000"
		}
		mask = mask + strings.Repeat("0", 8-len(mask))
	}
	return mask[:8]
}

// NormalizePanelMask returns a canonical 3-char mask.
func NormalizePanelMask(mask string) string {
	if len(mask) < 3 {
		if len(mask) == 0 {
			return "000"
		}
		return mask + strings.Repeat("0", 3-len(mask))
	}
	return mask[:3]
}

// HasServerPerm reports whether the mask grants the given server permission bit.
func HasServerPerm(mask string, bit int) bool {
	mask = NormalizeServerMask(mask)
	return mask[bit] == '1'
}

// HasPanelPerm reports whether the panel mask grants the given bit.
func HasPanelPerm(mask string, bit int) bool {
	mask = NormalizePanelMask(mask)
	return mask[bit] == '1'
}

// SetServerPerm sets/clears one bit, returning the new mask.
func SetServerPerm(mask string, bit int, on bool) string {
	mask = NormalizeServerMask(mask)
	b := mask[:bit]
	if on {
		b += "1"
	} else {
		b += "0"
	}
	return b + mask[bit+1:]
}

// SetPanelPerm sets/clears one panel bit, returning the new mask.
func SetPanelPerm(mask string, bit int, on bool) string {
	mask = NormalizePanelMask(mask)
	b := mask[:bit]
	if on {
		b += "1"
	} else {
		b += "0"
	}
	return b + mask[bit+1:]
}

// MergeServerMasks ORs any number of masks (union of grants).
func MergeServerMasks(masks ...string) string {
	out := "00000000"
	for _, m := range masks {
		m = NormalizeServerMask(m)
		for i := 0; i < 8; i++ {
			if m[i] == '1' {
				out = out[:i] + "1" + out[i+1:]
			}
		}
	}
	return out
}

// MaskToPermList returns the human list of granted server permission names.
func MaskToPermList(mask string) []string {
	out := []string{}
	mask = NormalizeServerMask(mask)
	for i, n := range ServerPermNames {
		if mask[i] == '1' {
			out = append(out, n)
		}
	}
	return out
}

// PanelMaskToPermList returns granted panel permission names.
func PanelMaskToPermList(mask string) []string {
	out := []string{}
	mask = NormalizePanelMask(mask)
	for i, n := range PanelPermNames {
		if mask[i] == '1' {
			out = append(out, n)
		}
	}
	return out
}

// Principal is the authenticated identity a request acts as: either a user
// (browser session / superuser) or an API key with intersected masks.
type Principal struct {
	UserID       int64
	Username     string
	Superuser    bool
	Enabled      bool
	APIKeyID     int64  // 0 = interactive user, otherwise the key row id
	APIKeyFull   bool   // full access key acts like its user
	PanelPerms   string // effective panel mask (already intersected for keys)
	LimitServers int64  // -1 = unlimited
	LimitUsers   int64
	LimitRoles   int64
	// serverPerms is the union of grants from roles (user) or key mask ∩ role grants (key).
	serverPerms map[string]string
}

// NewPrincipal builds a principal with no server grants.
func NewPrincipal() *Principal {
	return &Principal{serverPerms: map[string]string{}, PanelPerms: "000", LimitServers: 0, LimitUsers: 0, LimitRoles: 0}
}

// AddServerPerm records a server grant (mask union).
func (p *Principal) AddServerPerm(serverID, mask string) {
	if p.serverPerms == nil {
		p.serverPerms = map[string]string{}
	}
	cur, ok := p.serverPerms[serverID]
	if !ok {
		p.serverPerms[serverID] = NormalizeServerMask(mask)
		return
	}
	p.serverPerms[serverID] = MergeServerMasks(cur, mask)
}

// ServerIDs lists granted servers.
func (p *Principal) ServerIDs() []string {
	out := make([]string, 0, len(p.serverPerms))
	for id := range p.serverPerms {
		out = append(out, id)
	}
	return out
}

// HasServerAccess reports whether the principal may see the server at all.
func (p *Principal) HasServerAccess(serverID string) bool {
	if p.Superuser || (p.APIKeyID != 0 && p.APIKeyFull) {
		return true
	}
	_, ok := p.serverPerms[serverID]
	return ok
}

// CanServer reports whether the principal holds the given permission bit on a server.
func (p *Principal) CanServer(serverID string, bit int) bool {
	if p.Superuser || (p.APIKeyID != 0 && p.APIKeyFull) {
		return true
	}
	mask, ok := p.serverPerms[serverID]
	if !ok {
		return false
	}
	return HasServerPerm(mask, bit)
}

// CanPanel reports whether the principal holds a panel-level permission.
func (p *Principal) CanPanel(bit int) bool {
	if p.Superuser || (p.APIKeyID != 0 && p.APIKeyFull) {
		return true
	}
	return HasPanelPerm(p.PanelPerms, bit)
}

// ServerMaskFor returns the granted mask for a server (zero mask if none).
func (p *Principal) ServerMaskFor(serverID string) string {
	if p.serverPerms == nil {
		return "00000000"
	}
	return NormalizeServerMask(p.serverPerms[serverID])
}
