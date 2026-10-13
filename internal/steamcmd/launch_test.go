package steamcmd

import (
	"reflect"
	"strings"
	"testing"
)

func TestLaunchOptionsResolveTypedAndDerivedValues(t *testing.T) {
	game, err := ParseGameManifest([]byte(`app_id: 999991
slug: launch-options-test
name: Launch Options Test
run_command: ./server -savedir {SAVE_DIR}
executable: server
port: 2456
launch_options:
  - key: server_name
    label: Server name
    type: text
    source: server_name
    flag: -name
  - key: port
    label: Port
    type: number
    source: port
    flag: -port
    min: 1
    max: 65534
  - key: world
    label: World
    type: text
    flag: -world
    default: Dedicated
  - key: password
    label: Password
    type: password
    flag: -password
    required: true
    min_length: 5
    max_length: 64
  - key: public
    label: Public listing
    type: boolean
    flag: -public
    default: "true"
    true_value: "1"
    false_value: "0"
config_editors:
  - path: saves/adminlist.txt
    label: Admin list
    description: One player ID per line.
    editor: lines
`))
	if err != nil {
		t.Fatalf("ParseGameManifest: %v", err)
	}
	if len(game.ConfigEditors) != 1 || game.ConfigEditors[0].Path != "saves/adminlist.txt" || len(game.LaunchOptions) != 5 {
		t.Fatalf("parsed descriptors/options = %+v / %+v", game.ConfigEditors, game.LaunchOptions)
	}

	args, values, err := ResolveLaunchOptions(game.LaunchOptions, map[string]string{
		"password": `S3cret; $HOME "quoted"`,
		"public":   "false",
	}, LaunchContext{ServerName: `My server; $(echo unsafe)`, Port: 2456, QueryPort: 2457, SaveDir: "/srv/servy/server data/saves"})
	if err != nil {
		t.Fatalf("ResolveLaunchOptions: %v", err)
	}
	want := []string{
		"-name", `My server; $(echo unsafe)`,
		"-port", "2456",
		"-world", "Dedicated",
		"-password", `S3cret; $HOME "quoted"`,
		"-public", "0",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("argv = %#v, want %#v", args, want)
	}
	if values["world"] != "Dedicated" || values["password"] != `S3cret; $HOME "quoted"` || values["server_name"] != "" {
		t.Fatalf("stored option values = %#v", values)
	}

	snapshotJSON, err := EncodeGameSnapshot(GameSnapshot{Game: game, LaunchOptions: values})
	if err != nil {
		t.Fatalf("EncodeGameSnapshot: %v", err)
	}
	snapshot, err := GameSnapshotFromDefinitions(snapshotJSON)
	if err != nil {
		t.Fatalf("GameSnapshotFromDefinitions: %v", err)
	}
	if snapshot == nil || snapshot.Game.Slug != game.Slug || snapshot.LaunchOptions["password"] != `S3cret; $HOME "quoted"` {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestResolveLaunchOptionsRejectsMissingUnknownAndSpoofedValues(t *testing.T) {
	options := []LaunchOption{
		{Key: "port", Label: "Port", Type: "number", Source: "port", Flag: "-port", Min: intPtr(1), Max: intPtr(65534)},
		{Key: "password", Label: "Password", Type: "password", Flag: "-password", Required: true, MinLength: intPtr(5)},
		{Key: "mode", Label: "Mode", Type: "select", Options: []LaunchOptionValue{{Value: "survival", Label: "Survival"}, {Value: "creative", Label: "Creative"}}},
	}
	context := LaunchContext{ServerName: "Example", Port: 2456, QueryPort: 2457, SaveDir: "/data/saves"}
	cases := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"missing required", map[string]string{}, "required"},
		{"short password", map[string]string{"password": "1234"}, "at least 5"},
		{"unknown key", map[string]string{"password": "secret", "extra": "x"}, "unknown launch option"},
		{"spoof source", map[string]string{"port": "1234", "password": "secret"}, "supplied by Servy"},
		{"bad select", map[string]string{"password": "secret", "mode": "admin"}, "declared choices"},
		{"control character", map[string]string{"password": "sec\nret"}, "control characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ResolveLaunchOptions(options, tc.values, context)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestGameManifestRejectsInvalidLaunchAndEditorMetadata(t *testing.T) {
	base := `app_id: 999992
slug: invalid-options-test
name: Invalid Options Test
run_command: ./server
executable: server
port: 27015
`
	cases := []struct {
		name string
		body string
		want string
	}{
		{"duplicate option", "launch_options:\n  - key: name\n    label: Name\n    type: text\n  - key: name\n    label: Other\n    type: text\n", "duplicated"},
		{"unsafe flag", "launch_options:\n  - key: name\n    label: Name\n    type: text\n    flag: '-name --inject'\n", "single argument"},
		{"unknown editor", "config_editors:\n  - path: adminlist.txt\n    editor: shell\n", "must be auto, raw, or lines"},
		{"traversal editor path", "config_editors:\n  - path: ../adminlist.txt\n", "server directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseGameManifest([]byte(base + tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func intPtr(value int) *int { return &value }
