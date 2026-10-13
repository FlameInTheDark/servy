package steamcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validGameManifest = `app_id: 999999
slug: example-game
name: Example Game
run_command: ./example_server --port {PORT} --name "{SERVER_NAME}"
executable: example_server
stop_command: quit
port: 27015
expose:
  - proto: udp
  - offset: 1
    proto: udp
    optional: true
config_files:
  - config/server.ini
save_dirs:
  - saves
backup_dirs:
  - saves
`

func TestParseGameManifest(t *testing.T) {
	game, err := ParseGameManifest([]byte(validGameManifest))
	if err != nil {
		t.Fatalf("ParseGameManifest: %v", err)
	}
	if game.Type != "steam_cmd" || game.AppID != 999999 || game.Slug != "example-game" {
		t.Fatalf("parsed game = %+v", game)
	}
	if got := game.Expose[1].Resolve(28000); got != 28001 {
		t.Fatalf("offset port = %d, want 28001", got)
	}
}

func TestParseGameManifestRejectsInvalidAndUnsafeValues(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"unknown field", strings.Replace(validGameManifest, "port: 27015", "port: 27015\nporrt: 7", 1), "field porrt not found"},
		{"bad slug", strings.Replace(validGameManifest, "slug: example-game", "slug: ../outside", 1), "slug"},
		{"missing command", strings.Replace(validGameManifest, "run_command: ./example_server --port {PORT} --name \"{SERVER_NAME}\"\n", "", 1), "run_command"},
		{"traversal config path", strings.Replace(validGameManifest, "config/server.ini", "../outside.ini", 1), "server directory"},
		{"invalid transport", strings.Replace(validGameManifest, "proto: udp", "proto: sctp", 1), "tcp or udp"},
		{"query port overflow", strings.Replace(validGameManifest, "port: 27015", "port: 65535", 1), "query port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseGameManifest([]byte(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestCatalogCustomManifestPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "steamgames.json")
	catalog, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	game, err := ParseGameManifest([]byte(validGameManifest))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Upsert(game); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !catalog.IsCustom(game.Slug) {
		t.Fatal("added manifest is not marked custom")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("overlay was not written: %v", err)
	}

	reloaded, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := reloaded.BySlug(game.Slug)
	if !ok || got.AppID != game.AppID || got.Name != game.Name {
		t.Fatalf("reloaded game = %+v, found=%v", got, ok)
	}
	if got := reloaded.CustomAll(); len(got) != 1 || got[0].Slug != game.Slug {
		t.Fatalf("custom catalog = %+v", got)
	}

	if err := reloaded.DeleteCustom(game.Slug); err != nil {
		t.Fatalf("DeleteCustom: %v", err)
	}
	if _, ok := reloaded.BySlug(game.Slug); ok {
		t.Fatalf("deleted game %q is still present", game.Slug)
	}
	if got := reloaded.CustomAll(); len(got) != 0 {
		t.Fatalf("custom catalog after delete = %+v", got)
	}
}

func TestLoadCatalogRejectsCorruptOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "steamgames.json")
	if err := os.WriteFile(path, []byte(`[{"slug":"missing-fields"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(path); err == nil {
		t.Fatal("invalid overlay should fail explicitly")
	}
}
