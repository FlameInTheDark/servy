package authz

import "testing"

func TestMasks(t *testing.T) {
	// legacy semantics: index = bit position
	mask := "00000000"
	mask = SetServerPerm(mask, ServerTerminal, true)
	mask = SetServerPerm(mask, ServerBackup, true)
	if !HasServerPerm(mask, ServerTerminal) {
		t.Fatal("TERMINAL should be set")
	}
	if HasServerPerm(mask, ServerCommands) {
		t.Fatal("COMMANDS should not be set")
	}
	if got := MaskToPermList(mask); len(got) != 2 || got[0] != "TERMINAL" || got[1] != "BACKUP" {
		t.Fatalf("perm list wrong: %v", got)
	}
	// panel mask
	cm := SetPanelPerm("000", PanelServerCreation, true)
	if !HasPanelPerm(cm, PanelServerCreation) || HasPanelPerm(cm, PanelUserConfig) {
		t.Fatal("panel mask wrong")
	}
}

func TestMerge(t *testing.T) {
	a := "11000000"
	b := "00110000"
	got := MergeServerMasks(a, b)
	if got != "11110000" {
		t.Fatalf("merge = %s", got)
	}
}

func TestNormalize(t *testing.T) {
	if NormalizeServerMask("") != "00000000" {
		t.Fatal("empty normalize failed")
	}
	if NormalizeServerMask("11") != "11000000" {
		t.Fatal("short normalize failed")
	}
	if NormalizePanelMask("1") != "100" {
		t.Fatal("panel normalize failed")
	}
}

func TestPrincipal(t *testing.T) {
	p := NewPrincipal()
	p.Superuser = false
	p.AddServerPerm("s1", "11000000")
	p.AddServerPerm("s1", "00001100") // sets BACKUP + FILES
	if !p.CanServer("s1", ServerTerminal) || !p.CanServer("s1", ServerBackup) || !p.CanServer("s1", ServerFiles) {
		t.Fatal("union grant failed")
	}
	if p.CanServer("s1", ServerLogs) {
		t.Fatal("LOGS should not be granted")
	}
	if p.CanServer("s2", ServerTerminal) {
		t.Fatal("s2 should not be accessible")
	}
	if p.HasServerAccess("s2") {
		t.Fatal("no access to s2")
	}
	// superuser bypass
	p.Superuser = true
	if !p.CanServer("anything", ServerConfig) {
		t.Fatal("superuser bypass failed")
	}
	// panel
	p.Superuser = false
	if p.CanPanel(PanelUserConfig) {
		t.Fatal("panel mask should not bypass for non-superuser")
	}
	p.PanelPerms = "010"
	if !p.CanPanel(PanelUserConfig) {
		t.Fatal("panel perm failed")
	}
}
