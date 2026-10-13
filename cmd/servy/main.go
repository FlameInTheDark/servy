// Command servy is the Servy panel binary (single file:
// API + websocket + embedded React UI + scheduler + cluster).
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
	if err := cli.Root().Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "servy:", err)
		os.Exit(1)
	}
}
