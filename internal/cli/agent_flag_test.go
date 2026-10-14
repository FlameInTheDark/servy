package cli

import (
	"reflect"
	"testing"

	clipkg "github.com/urfave/cli/v3"
)

// The single servy binary covers both roles: the panel and the headless node
// agent. `--agent` is sugar for the `agent` subcommand, so these cases pin the
// argument rewrite that makes `servy --agent run …` equal to
// `servy agent run …`.
func TestAgentModeArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "flag before the agent subcommand keywords",
			in:   []string{"servy", "--agent", "run", "--ticket", "T"},
			want: []string{"servy", "--agent", "agent", "run", "--ticket", "T"},
		},
		{
			name: "status",
			in:   []string{"servy", "--agent", "status"},
			want: []string{"servy", "--agent", "agent", "status"},
		},
		{
			name: "leave",
			in:   []string{"servy", "--agent", "leave"},
			want: []string{"servy", "--agent", "agent", "leave"},
		},
		{
			name: "short data flag with a value is not mistaken for the subcommand",
			in:   []string{"servy", "-d", "/srv/servy", "--agent", "run"},
			want: []string{"servy", "-d", "/srv/servy", "--agent", "agent", "run"},
		},
		{
			name: "long data flag with a value",
			in:   []string{"servy", "--data", "/srv/servy", "--agent", "status"},
			want: []string{"servy", "--data", "/srv/servy", "--agent", "agent", "status"},
		},
		{
			name: "inline data value",
			in:   []string{"servy", "--data=/srv/servy", "--agent", "status"},
			want: []string{"servy", "--data=/srv/servy", "--agent", "agent", "status"},
		},
		{
			name: "explicit false keeps the panel",
			in:   []string{"servy", "--agent=false", "serve"},
			want: []string{"servy", "--agent=false", "serve"},
		},
		{
			name: "already rewritten",
			in:   []string{"servy", "--agent", "agent", "run"},
			want: []string{"servy", "--agent", "agent", "run"},
		},
		{
			name: "plain panel run is untouched",
			in:   []string{"servy", "serve", "--port", "8443"},
			want: []string{"servy", "serve", "--port", "8443"},
		},
		{
			name: "agent flag after the subcommand is left to the parser",
			in:   []string{"servy", "serve", "--agent"},
			want: []string{"servy", "serve", "--agent"},
		},
		{
			name: "only the program name",
			in:   []string{"servy"},
			want: []string{"servy"},
		},
		{
			name: "version",
			in:   []string{"servy", "version"},
			want: []string{"servy", "version"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AgentModeArgs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("AgentModeArgs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The rewritten line must actually select the agent subcommand.
func TestAgentModeArgsSelectsAgentCommand(t *testing.T) {
	args := AgentModeArgs([]string{"servy", "--agent", "run", "--ticket", "T"})
	cmd, rest := lookupSubcommand(Root(), args[1:])
	if cmd == nil || cmd.Name != "agent" {
		t.Fatalf("expected the `agent` command, got %v (rest %q)", cmd, rest)
	}
}

// lookupSubcommand resolves the first non-flag token against the command tree,
// mirroring how the CLI library picks a subcommand (root --data/-d takes a
// value, everything else is a boolean flag).
func lookupSubcommand(root *clipkg.Command, args []string) (*clipkg.Command, []string) {
	current := root
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if len(arg) > 0 && arg[0] == '-' {
			if arg == "--data" || arg == "-d" {
				i++
			}
			continue
		}
		next := findChild(current, arg)
		if next == nil {
			return current, args[i:]
		}
		current = next
	}
	return current, nil
}

func findChild(cmd *clipkg.Command, name string) *clipkg.Command {
	for _, sub := range cmd.Commands {
		if sub.Name == name || contains(sub.Aliases, name) {
			return sub
		}
	}
	return nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
