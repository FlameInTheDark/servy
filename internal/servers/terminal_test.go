package servers

import (
	"strings"
	"testing"
)

func TestStripANSI(t *testing.T) {
	in := "\x1b[32mGREEN\x1b[0m plain \x1b[1;31mbold-red\x1b[0m"
	out := stripANSI(in)
	if strings.Contains(out, "\x1b") {
		t.Fatalf("ANSI left: %q", out)
	}
	if out != "GREEN plain bold-red" {
		t.Fatalf("strip wrong: %q", out)
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`java -Xmx1G -jar server.jar`, []string{"java", "-Xmx1G", "-jar", "server.jar"}},
		{`./run.sh --msg "hello world" 'single arg'`, []string{"./run.sh", "--msg", "hello world", "single arg"}},
		{`echo a\ b`, []string{"echo", "a b"}},
	}
	for _, c := range cases {
		got := splitCommand(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("split(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("split(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestTerminalRing(t *testing.T) {
	term := NewTerminal(5, nil)
	for i := 0; i < 8; i++ {
		term.Add([]byte("line\n"))
	}
	lines := term.Tail(0)
	if len(lines) != 5 {
		t.Fatalf("ring should cap at 5, has %d", len(lines))
	}
}

func TestTerminalBatching(t *testing.T) {
	term := NewTerminal(70, nil)
	term.Add([]byte("hello world\nsecond line"))
	lines := term.Tail(0)
	if len(lines) != 2 || lines[0] != "hello world" || lines[1] != "second line" {
		t.Fatalf("line split wrong: %v", lines)
	}
}
