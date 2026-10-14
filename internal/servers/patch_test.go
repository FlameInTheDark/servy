package servers

import (
	"testing"

	"github.com/FlameInTheDark/servy/internal/repo/gen"
	"github.com/FlameInTheDark/servy/internal/steamcmd"
)

// steamDef builds a server row whose frozen manifest declares one editable
// password option and one automatic option (the game port).
func steamDef(t *testing.T, values map[string]string) gen.Server {
	t.Helper()
	game := steamcmd.Game{
		AppID:       896660,
		Name:        "Valheim",
		Slug:        "valheim",
		Type:        "steam_cmd",
		RunCommand:  "./valheim_server.x86_64 -name {SERVER_NAME} -port {PORT}",
		Executable:  "valheim_server.x86_64",
		StopCommand: "quit",
		Port:        2456,
		LaunchOptions: []steamcmd.LaunchOption{
			{Key: "name", Label: "Server name", Type: "text", Flag: "-name", Source: "server_name"},
			{Key: "port", Label: "Port", Type: "number", Flag: "-port", Source: "port"},
			{
				Key: "password", Label: "Server password", Type: "password", Flag: "-password",
				Required: true, MinLength: intPtr(5), MaxLength: intPtr(64),
			},
			{Key: "public", Label: "Public", Type: "boolean", Flag: "-public", Default: "true", TrueValue: "1", FalseValue: "0"},
			{Key: "savename", Label: "Save name", Type: "text", Flag: "-savename"},
		},
	}
	definitions, err := steamcmd.EncodeGameSnapshot(steamcmd.GameSnapshot{Game: game, LaunchOptions: values})
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	return gen.Server{
		ServerID: "srv-1", ServerName: "viking", Path: "/srv/servers/srv-1",
		ServerPort: 2456, Type: "steam_cmd", Definitions: definitions,
		ExecutionCommand: game.RunCommand, StopCommand: "quit",
	}
}

func intPtr(v int) *int { return &v }

func snapshotValues(t *testing.T, def gen.Server) map[string]string {
	t.Helper()
	snapshot, err := steamcmd.GameSnapshotFromDefinitions(def.Definitions)
	if err != nil || snapshot == nil {
		t.Fatalf("snapshot: %v", err)
	}
	return snapshot.LaunchOptions
}

func TestApplyServerPatchSteamOptions(t *testing.T) {
	def := steamDef(t, map[string]string{"password": "old-password"})

	err := ApplyServerPatch(&def, map[string]interface{}{
		"steam_options": map[string]interface{}{"password": "new-password", "public": "false"},
	})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	values := snapshotValues(t, def)
	if values["password"] != "new-password" {
		t.Fatalf("password = %q, want new-password", values["password"])
	}
	if values["public"] != "false" {
		t.Fatalf("public = %q, want false (boolean options keep their raw value)", values["public"])
	}
	if _, ok := values["port"]; ok {
		t.Fatalf("automatic options must not be stored: %v", values)
	}
}

func TestApplyServerPatchSteamOptionValidation(t *testing.T) {
	cases := []struct {
		name  string
		patch map[string]interface{}
	}{
		{"too short", map[string]interface{}{"steam_options": map[string]interface{}{"password": "abc"}}},
		{"unknown key", map[string]interface{}{"steam_options": map[string]interface{}{"nope": "x"}}},
		{"automatic key", map[string]interface{}{"steam_options": map[string]interface{}{"port": "9999"}}},
		{"not an object", map[string]interface{}{"steam_options": "password=abc"}},
		{"cleared required option", map[string]interface{}{"steam_options": map[string]interface{}{"password": ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := steamDef(t, map[string]string{"password": "old-password"})
			if err := ApplyServerPatch(&def, tc.patch); err == nil {
				t.Fatal("expected an error")
			}
			if got := snapshotValues(t, def)["password"]; got != "old-password" {
				t.Fatalf("rejected patch changed the stored values: %q", got)
			}
		})
	}
}

func TestApplyServerPatchClearsSteamOption(t *testing.T) {
	def := steamDef(t, map[string]string{"savename": "midgard", "password": "old-password"})
	if err := ApplyServerPatch(&def, map[string]interface{}{
		"steam_options": map[string]interface{}{"savename": ""},
	}); err != nil {
		t.Fatalf("clearing an optional value must fall back to the manifest default: %v", err)
	}
	if _, ok := snapshotValues(t, def)["savename"]; ok {
		t.Fatal("cleared value is still stored")
	}
}

func TestApplyServerPatchMemory(t *testing.T) {
	def := gen.Server{
		ServerID: "srv-2", ServerName: "mc", Type: "minecraft-java", ServerPort: 25565,
		ExecutionCommand: "java -Xms512M -Xmx1024M -jar server.jar nogui",
	}
	if err := ApplyServerPatch(&def, map[string]interface{}{"min_mem": "2G", "max_mem": "4G"}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	want := "java -Xms2G -Xmx4G -jar server.jar nogui"
	if def.ExecutionCommand != want {
		t.Fatalf("command = %q, want %q", def.ExecutionCommand, want)
	}
	// memory values are validated
	for _, bad := range []string{"512MB", "-1", "0", "lots"} {
		broken := def
		if err := ApplyServerPatch(&broken, map[string]interface{}{"min_mem": bad}); err == nil {
			t.Fatalf("min_mem %q was accepted", bad)
		}
	}
	// commands without the flag get a clear error instead of a silent no-op
	script := gen.Server{ServerID: "srv-3", ExecutionCommand: "./start.sh"}
	if err := ApplyServerPatch(&script, map[string]interface{}{"min_mem": "512M"}); err == nil {
		t.Fatal("expected an error for a command without -Xms")
	}
	if script.ExecutionCommand != "./start.sh" {
		t.Fatalf("command was changed: %q", script.ExecutionCommand)
	}
}
