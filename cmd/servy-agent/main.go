// Command servy-agent is the headless cluster agent binary (thin wrapper
// around `servy agent`).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/FlameInTheDark/servy/internal/cli"
)

func main() {
	// translate: servy-agent --panel X --token Y [data flags] → servy agent --panel X --token Y
	args := []string{"servy", "agent"}
	passed := map[string]bool{}
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		switch {
		case strings.HasPrefix(a, "--data") || strings.HasPrefix(a, "-d"):
			// forward to root: needs to be positioned before subcommand
			passed["data"] = true
		}
	}
	_ = passed
	args = append(args, os.Args[1:]...)
	if err := cli.Root().Run(context.Background(), args); err != nil {
		fmt.Fprintln(os.Stderr, "servy-agent:", err)
		os.Exit(1)
	}
}
