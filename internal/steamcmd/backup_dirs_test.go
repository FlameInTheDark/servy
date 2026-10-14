package steamcmd

import (
	"reflect"
	"testing"
)

func TestNormalizeBackupDirs(t *testing.T) {
	got := NormalizeBackupDirs([]string{
		" saves/world ", `saves\players`, "saves/world", "./plugins/", "/etc", "../secrets",
		"", ".", "..", "cfg/server.cfg",
	})
	want := []string{"saves/world", "saves/players", "plugins", "cfg/server.cfg"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeBackupDirs = %#v, want %#v", got, want)
	}
}

func TestCleanBackupDir(t *testing.T) {
	cases := map[string]string{
		"saves":            "saves",
		"saves/":           "saves",
		"./saves":          "saves",
		`saves\world`:      "saves/world",
		"saves//world/.":   "saves/world",
		"serverconfig.xml": "serverconfig.xml",
		"":                 "",
		".":                "",
		"..":               "",
		"../outside":       "",
		"/abs/path":        "",
		"a/../b":           "b",
	}
	for in, want := range cases {
		if got := CleanBackupDir(in); got != want {
			t.Errorf("CleanBackupDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeServerBackupDirsRoundTrip(t *testing.T) {
	raw, err := EncodeServerBackupDirs(nil, []string{"saves", `plugins\mods`, "saves"}, "Modded server")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if raw == nil {
		t.Fatal("encode returned nil for a non-empty folder list")
	}
	if got, want := BackupDirsFromDefinitions(raw), []string{"saves", "plugins/mods"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("BackupDirsFromDefinitions = %#v, want %#v", got, want)
	}
	if got := BackupDirsSource(raw); got != "Modded server" {
		t.Fatalf("BackupDirsSource = %q, want %q", got, "Modded server")
	}
}

func TestEncodeServerBackupDirsKeepsGameSnapshot(t *testing.T) {
	snapshot, err := EncodeGameSnapshot(GameSnapshot{Game: Game{
		AppID: 896660, Slug: "valheim", Name: "Valheim", Type: "steam_cmd",
		RunCommand: "./valheim_server.x86_64", Executable: "valheim_server.x86_64",
		Port: 2456, BackupDirs: []string{"saves"},
	}})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	raw, err := EncodeServerBackupDirs(snapshot, nil, "")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if raw == nil {
		t.Fatal("encode dropped an envelope that still holds a game snapshot")
	}
	game, err := GameSnapshotFromDefinitions(raw)
	if err != nil || game == nil {
		t.Fatalf("game snapshot lost: game=%v err=%v", game, err)
	}
	if game.Game.Slug != "valheim" {
		t.Fatalf("game snapshot slug = %q, want valheim", game.Game.Slug)
	}
	// the frozen game manifest wins over the (now empty) recipe list
	if got, want := BackupDirsFromDefinitions(raw), []string{"saves"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("BackupDirsFromDefinitions = %#v, want %#v", got, want)
	}
	if got := BackupDirsSource(raw); got != "Valheim" {
		t.Fatalf("BackupDirsSource = %q, want %q", got, "Valheim")
	}
}

func TestEncodeServerBackupDirsEmpty(t *testing.T) {
	raw, err := EncodeServerBackupDirs(nil, nil, "unused")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if raw != nil {
		t.Fatalf("encode = %q for an empty folder list, want nil", *raw)
	}
}

func TestBackupDirsFromLegacyDefinitions(t *testing.T) {
	legacy := "aGVsbG8gd29ybGQ=" // pre-JSON definitions blob
	if got := BackupDirsFromDefinitions(&legacy); got != nil {
		t.Fatalf("legacy definitions produced %#v, want nil", got)
	}
	if got := BackupDirsSource(&legacy); got != "" {
		t.Fatalf("legacy definitions produced source %q, want empty", got)
	}
}
