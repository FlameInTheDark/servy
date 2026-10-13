package spa

import (
	"io/fs"
	"regexp"
	"testing"
)

// assetRefRe matches hashed asset references emitted by the frontend build,
// e.g. <script src="/assets/index-q_AddP_V.js"> or <link href="/assets/index-Bx_kFAZh.css">.
var assetRefRe = regexp.MustCompile(`(?:src|href)="/(assets/[A-Za-z0-9._-]+)"`)

// TestEmbeddedUIComplete guards against shipping a binary whose embedded UI is
// only the HTML shell: every hashed asset referenced by the embedded index.html
// must exist in the embedded filesystem. This is the regression test for the
// "every asset request returns index.html" bug caused by building from a tree
// where internal/spa/dist was not fully checked in.
func TestEmbeddedUIComplete(t *testing.T) {
	fsys := FS()

	idx, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		t.Fatalf("embedded UI has no index.html: %v (run `make frontend` to build it)", err)
	}
	refs := assetRefRe.FindAllStringSubmatch(string(idx), -1)
	if len(refs) == 0 {
		t.Fatal("embedded index.html references no /assets/* files — the frontend build is missing or incomplete")
	}
	seen := map[string]bool{}
	for _, r := range refs {
		ref := r[1]
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if _, err := fs.Stat(fsys, ref); err != nil {
			t.Errorf("embedded UI is missing %q referenced by index.html — assets not embedded (broken dist checkout?)", ref)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no unique asset references resolved")
	}
}
