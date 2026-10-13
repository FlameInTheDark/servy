package servers

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/FlameInTheDark/servy/internal/models"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
	"github.com/FlameInTheDark/servy/internal/steamcmd"
)

func TestExecutionArgvSubstitutesPlaceholdersWithoutShellFlattening(t *testing.T) {
	minPasswordLength := 5
	game := steamcmd.Game{
		AppID: 999993, Name: "Argv Test", Slug: "argv-test",
		RunCommand: "./server -name {SERVER_NAME} -port {PORT} -savedir {SAVE_DIR}",
		Executable: "server", Port: 2456,
		LaunchOptions: []steamcmd.LaunchOption{
			{Key: "password", Label: "Password", Type: "password", Flag: "-password", Required: true, MinLength: &minPasswordLength},
		},
	}
	definitions, err := steamcmd.EncodeGameSnapshot(steamcmd.GameSnapshot{
		Game: game, LaunchOptions: map[string]string{"password": `S3cret; $(touch /tmp/nope)`},
	})
	if err != nil {
		t.Fatalf("EncodeGameSnapshot: %v", err)
	}
	serverPath := filepath.Join(t.TempDir(), "server files")
	def := gen.Server{
		Type: models.TypeSteamCmd, Path: serverPath, ServerName: `Name; $(echo still-one-argument)`,
		ServerPort: 2456, ExecutionCommand: game.RunCommand, Definitions: definitions,
	}
	sup := &Supervisor{}
	argv, err := sup.executionArgv(def)
	if err != nil {
		t.Fatalf("executionArgv: %v", err)
	}
	want := []string{
		"./server", "-name", `Name; $(echo still-one-argument)`,
		"-port", "2456", "-savedir", filepath.Join(serverPath, "saves"),
		"-password", `S3cret; $(touch /tmp/nope)`,
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %#v, want %#v", argv, want)
	}
}
