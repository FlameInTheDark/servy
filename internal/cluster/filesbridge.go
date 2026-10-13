package cluster

import (
	"github.com/FlameInTheDark/servy/internal/files"
)

// Thin indirections so cluster.go doesn't import files directly with
// clashing names.
func filesNewGuard(root string) (*files.Guard, error) { return files.NewGuard(root) }

func filesRead(g *files.Guard, rel string, max int64) ([]byte, error) {
	return files.New().Read(g, rel, max)
}

func filesWrite(g *files.Guard, rel string, data []byte) error {
	return files.New().Write(g, rel, data)
}

func filesList(g *files.Guard, rel string) ([]files.Entry, error) {
	return files.New().List(g, rel)
}
