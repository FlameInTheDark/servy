package steamcmd

import (
	"reflect"
	"testing"
)

func TestBuildSteamCmdArgs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "install dir token must appear exactly once",
			in:   []string{"+force_install_dir", "/data", "+app_update", "2857200", "validate"},
			want: []string{"+force_install_dir", "/data", "+login", "anonymous", "+app_update", "2857200", "validate"},
		},
		{
			name: "no force_install_dir prepends login",
			in:   []string{"+app_update", "730", "validate"},
			want: []string{"+login", "anonymous", "+app_update", "730", "validate"},
		},
		{
			name: "workshop download item",
			in:   []string{"+force_install_dir", "/data", "+workshop_download_item", "108600", "2890903216"},
			want: []string{"+force_install_dir", "/data", "+login", "anonymous", "+workshop_download_item", "108600", "2890903216"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSteamCmdArgs(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("buildSteamCmdArgs(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestCatalogBuiltinGames validates every embedded game definition: unique
// slugs/app ids, non-empty commands, and windows_only entries must carry a
// Wine-capable docker image (never the plain steamcmd image).
func TestCatalogBuiltinGames(t *testing.T) {
	c := NewCatalog("/nonexistent/overlay.json")
	games := c.All()
	if len(games) < 10 {
		t.Fatalf("expected the builtin catalog to load, got %d games", len(games))
	}
	slugs := map[string]bool{}
	appIDs := map[int]string{}
	for _, g := range games {
		if g.Slug == "" || g.Name == "" || g.AppID <= 0 {
			t.Fatalf("game with empty slug/name/app_id: %+v", g)
		}
		if slugs[g.Slug] {
			t.Fatalf("duplicate slug %q", g.Slug)
		}
		slugs[g.Slug] = true
		if prev, ok := appIDs[g.AppID]; ok {
			t.Fatalf("duplicate app id %d (%s vs %s)", g.AppID, prev, g.Name)
		}
		appIDs[g.AppID] = g.Name
		if g.RunCommand == "" || g.Executable == "" {
			t.Fatalf("game %s missing run_command/executable", g.Slug)
		}
		if g.WindowsOnly && g.Platform != "windows" {
			t.Fatalf("windows_only game %s must set platform: windows", g.Slug)
		}
		if g.Platform == "windows" && g.DockerImage != "" &&
			(g.DockerImage == "steamcmd/steamcmd:latest" || g.DockerImage == "cm2network/steamcmd:latest") {
			t.Fatalf("windows-only game %s ships the plain steamcmd image (no Wine): %s", g.Slug, g.DockerImage)
		}
	}
	// entries the user's workflow depends on
	for _, slug := range []string{"abiotic-factor", "v-rising", "enshrouded", "project-zomboid", "valheim", "palworld"} {
		if _, ok := c.BySlug(slug); !ok {
			t.Fatalf("expected catalog entry %q to exist", slug)
		}
	}
}

// TestExposedPortResolve covers the remappable game port: entries WITHOUT an
// explicit port follow the configured game port; explicit ports win.
func TestExposedPortResolve(t *testing.T) {
	c := NewCatalog("/nonexistent/overlay.json")
	af, ok := c.BySlug("abiotic-factor")
	if !ok {
		t.Fatal("abiotic-factor missing")
	}
	// default game port 7777: expose resolves to 7777 + explicit 27015
	var got []int
	for _, e := range af.Expose {
		got = append(got, e.Resolve(7777))
	}
	if len(got) != 2 || got[0] != 7777 || got[1] != 27015 {
		t.Fatalf("abiotic expose on default port = %v, want [7777 27015]", got)
	}
	// remapped game port 8000: the game port must follow (not stay 7777)
	for _, e := range af.Expose {
		if e.Offset == 0 && e.Port == 7777 {
			t.Fatalf("abiotic game-port exposure is pinned to 7777 — remapped ports publish the wrong port")
		}
	}
}
