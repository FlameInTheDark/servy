package files

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadURLRejectsLoopbackAndLeavesNoPartialFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("should not be reached"))
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "server.jar")
	if _, err := DownloadURL(server.URL, dest); err == nil {
		t.Fatal("loopback download URL should be rejected")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("partial file should not be left behind: stat error = %v", err)
	}
}
