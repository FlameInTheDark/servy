// Command servy is the single Servy binary: the panel (API + websocket +
// embedded React UI + scheduler + cluster) and the headless node agent
// (`servy agent …` or `servy --agent …` on a machine that runs servers for a
// panel on another host).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/FlameInTheDark/servy/internal/cli"
	"github.com/FlameInTheDark/servy/internal/httpapi"
	"github.com/FlameInTheDark/servy/internal/spa"
)

func main() {
	httpapi.SPAFiles = spa.FS()
	if err := cli.Root().Run(context.Background(), cli.AgentModeArgs(os.Args)); err != nil {
		fmt.Fprintln(os.Stderr, "servy:", err)
		os.Exit(1)
	}
}
