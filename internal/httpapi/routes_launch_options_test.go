package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/FlameInTheDark/servy/internal/repo/gen"
	"github.com/FlameInTheDark/servy/internal/steamcmd"
)

func steamServerRow(t *testing.T) gen.Server {
	t.Helper()
	game := steamcmd.Game{
		AppID: 896660, Name: "Valheim", Slug: "valheim", Type: "steam_cmd",
		RunCommand: "./valheim_server.x86_64 -name {SERVER_NAME} -port {PORT}",
		Executable: "valheim_server.x86_64", StopCommand: "quit", Port: 2456,
		LaunchOptions: []steamcmd.LaunchOption{
			{Key: "name", Label: "Server name", Type: "text", Flag: "-name", Source: "server_name"},
			{Key: "port", Label: "Port", Type: "number", Flag: "-port", Source: "port"},
			{Key: "world", Label: "World", Type: "text", Flag: "-world", Default: "Dedicated"},
			{Key: "password", Label: "Server password", Type: "password", Flag: "-password", Required: true},
		},
	}
	// stored values: a password plus no explicit world, so the manifest default
	// has to show up in the editor
	definitions, err := steamcmd.EncodeGameSnapshot(steamcmd.GameSnapshot{
		Game: game, LaunchOptions: map[string]string{"password": "mead-hall-42"},
	})
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	return gen.Server{
		ServerID: "srv-steam", ServerName: "viking", Path: "/srv/servers/srv-steam",
		ServerPort: 2456, Type: "steam_cmd", Definitions: definitions,
		ExecutionCommand: game.RunCommand, StopCommand: "quit",
	}
}

// The server view reads this payload to render Settings → Startup parameters.
func TestBuildLaunchOptionsSteam(t *testing.T) {
	view := buildLaunchOptions(steamServerRow(t))
	if view["editable"] != true || view["kind"] != "steam" {
		t.Fatalf("editable/kind = %v/%v", view["editable"], view["kind"])
	}
	steam, ok := view["steam"].(fiber.Map)
	if !ok {
		t.Fatalf("steam block missing: %T", view["steam"])
	}
	options, ok := steam["options"].([]launchOptionView)
	if !ok || len(options) != 4 {
		t.Fatalf("options = %#v", steam["options"])
	}
	byKey := map[string]launchOptionView{}
	for _, option := range options {
		byKey[option.Key] = option
	}
	if got := byKey["password"]; got.Value != "mead-hall-42" || got.Type != "password" || !got.Required || got.UsingDefault {
		t.Errorf("password option = %+v", got)
	}
	if got := byKey["world"]; got.Value != "Dedicated" || !got.UsingDefault {
		t.Errorf("world option should fall back to the manifest default: %+v", got)
	}
	if got := byKey["port"]; got.Automatic != "port" || got.Value != "2456" {
		t.Errorf("port option should be automatic with the resolved value: %+v", got)
	}
	argv, ok := steam["argv"].([]string)
	if !ok || len(argv) == 0 {
		t.Fatalf("argv = %#v", steam["argv"])
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-name", "viking", "-port", "2456", "-password", "mead-hall-42"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q is missing %q", joined, want)
		}
	}
	// the payload the UI receives must not contain the snapshot envelope itself
	if _, err := json.Marshal(view); err != nil {
		t.Fatalf("payload is not JSON-serialisable: %v", err)
	}
}

func TestBuildLaunchOptionsMemoryAndEmpty(t *testing.T) {
	view := buildLaunchOptions(gen.Server{
		ServerID: "srv-mc", Type: "minecraft-java",
		ExecutionCommand: "java -Xms512M -Xmx1024M -jar server.jar nogui",
	})
	memory, ok := view["memory"].(fiber.Map)
	if !ok || memory["min"] != "512M" || memory["max"] != "1024M" {
		t.Fatalf("memory block = %#v", view["memory"])
	}
	if view["editable"] != true {
		t.Fatalf("memory-only servers must be editable: %v", view["editable"])
	}

	// a shell script has nothing to expose — the card stays hidden
	empty := buildLaunchOptions(gen.Server{ServerID: "srv-sh", Type: "custom", ExecutionCommand: "./start.sh"})
	if empty["editable"] != false {
		t.Fatalf("editable = %v, want false", empty["editable"])
	}
	if _, ok := empty["steam"]; ok {
		t.Fatal("unexpected steam block")
	}
	if _, ok := empty["memory"]; ok {
		t.Fatal("unexpected memory block")
	}
}

func TestMemoryFlagsOf(t *testing.T) {
	cases := []struct {
		command string
		min     string
		max     string
		ok      bool
	}{
		{"java -Xms512M -Xmx1024M -jar server.jar nogui", "512M", "1024M", true},
		{"java -Xmx4G -jar server.jar", "", "4G", true},
		{"java -xms512m -xmx2g -jar server.jar", "512m", "2g", true},
		{"./bedrock_server", "", "", false},
	}
	for _, tc := range cases {
		min, max, ok := memoryFlagsOf(tc.command)
		if ok != tc.ok || min != tc.min || max != tc.max {
			t.Errorf("memoryFlagsOf(%q) = %q,%q,%v; want %q,%q,%v", tc.command, min, max, ok, tc.min, tc.max, tc.ok)
		}
	}
}
