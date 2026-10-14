package core

// Integration test for uploading a backup to a real FTP server from the node
// that owns the server. It needs an external FTP server and its on-disk root:
//
//	SERVY_TEST_FTP_ADDR=127.0.0.1:2121 SERVY_TEST_FTP_ROOT=/srv/ftp go test ./internal/core/
//
// Both are left unset in CI, so the test skips there; internal/backupstore has
// the protocol level FTP tests that run without any environment.

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNodeUploadsBackupOverFTP(t *testing.T) {
	addr := os.Getenv("SERVY_TEST_FTP_ADDR")
	if addr == "" {
		t.Skip("no FTP server")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("bad SERVY_TEST_FTP_ADDR: %v", err)
	}
	root := os.Getenv("SERVY_TEST_FTP_ROOT")
	if root == "" || !strings.HasPrefix(root, "/") {
		t.Fatal("SERVY_TEST_FTP_ROOT must be an absolute path")
	}
	panel, node, cl, nodeID, _, _ := startPanelAndNode(t)
	_ = node
	clusterID := panel.Cfg.Get().ClusterID
	prefix := "servy-test"

	code, env, raw := cl.do("POST", "/panel/backup-storages", map[string]interface{}{
		"name": "ftp box", "kind": "ftp", "prefix": prefix, "enabled": true, "default": true,
		"settings": map[string]string{"host": host, "port": port, "username": "servy", "password": "s3cret", "tls": "none"},
	})
	if code != 200 {
		t.Fatalf("create destination: %d %s", code, raw)
	}
	storageID := env["data"].(map[string]interface{})["storage_id"].(string)

	code, _, raw = cl.do("POST", fmt.Sprintf("/panel/backup-storages/%s/test", storageID), nil)
	if code != 200 {
		t.Fatalf("test destination: %d %s", code, raw)
	}

	code, env, raw = cl.do("POST", "/servers", map[string]interface{}{
		"create_type": "raw_exec", "name": "ftpvault", "type": "custom",
		"executable": "run.sh", "execution_command": "/bin/bash run.sh",
		"stop_command": "stop", "port": 25779, "crash_detection": false, "node": nodeID,
	})
	if code != 200 {
		t.Fatalf("create server: %d %s", code, raw)
	}
	serverID := env["data"].(map[string]interface{})["server_id"].(string)
	if code, _, raw = cl.do("PUT", fmt.Sprintf("/servers/%s/files/run.sh", serverID), map[string]string{"content": "#!/bin/bash\nsleep 1\n"}); code != 200 {
		t.Fatalf("write file: %d %s", code, raw)
	}
	// storage_id omitted entirely: the default destination must be picked up
	code, env, raw = cl.do("POST", fmt.Sprintf("/servers/%s/backups", serverID), map[string]interface{}{
		"name": "vault", "max_backups": 5, "compress": true, "enabled": true, "default": true,
	})
	if code != 200 {
		t.Fatalf("create backup config: %d %s", code, raw)
	}
	data := env["data"].(map[string]interface{})
	backupID := data["backup_id"].(string)
	if data["storage_id"] != storageID {
		t.Fatalf("default destination not applied: %v", data["storage_id"])
	}

	if code, _, raw = cl.do("POST", fmt.Sprintf("/backups/backup/%s/run", backupID), nil); code != 200 {
		t.Fatalf("run backup: %d %s", code, raw)
	}

	want := filepath.Join(root, prefix, clusterID, serverID)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if entries, err := os.ReadDir(want); err == nil && len(entries) > 0 {
			t.Logf("uploaded over FTP to %s: %v", want, entries[0].Name())
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	code, _, raw = cl.do("GET", fmt.Sprintf("/backups/backup/%s/archives", backupID), nil)
	t.Fatalf("nothing landed in %s (archives: %d %s)", want, code, raw)
}
