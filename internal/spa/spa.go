// Package spa embeds the built React frontend (frontend/dist → copied here by
// the Makefile: `make frontend`). Serves as the single-file binary's UI.
package spa

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded SPA filesystem rooted at dist.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
