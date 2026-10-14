package core

// Acceptance test for backup storage destinations: the panel stores the
// destination (with credentials), the node that owns the server uploads the
// archive itself, the per-cluster namespace keeps archives apart, retention
// rotates the local cache and the remote history, and an archive that only
// exists in the destination can still be downloaded and restored.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FlameInTheDark/servy/internal/cluster"
)

func startPanelAndNode(t *testing.T) (*App, *App, *client, string, string, chan struct{}) {
	t.Helper()
	panelGrpc := freePort(t)
	agentGrpc := freePort(t)
	panelHTTP := freePort(t)

	panel := newTestApp(t, panelGrpc, panelHTTP)
	panelAddr := fmt.Sprintf("127.0.0.1:%d", panelGrpc)
	stop := make(chan struct{})
	go func() { _ = panel.StartGRPC(panelAddr) }()
	waitForPort(t, panelGrpc)

	panelRouter := panel.API.Router()
	go func() { _ = panelRouter.Listen(fmt.Sprintf("127.0.0.1:%d", panelHTTP)) }()
	t.Cleanup(func() {
		_ = panelRouter.Shutdown()
		close(stop)
	})
	waitForPort(t, panelHTTP)

	cl := &client{base: fmt.Sprintf("http://127.0.0.1:%d/api/v1", panelHTTP), http: &http.Client{Timeout: 120 * time.Second}}
	code, env, raw := cl.do("POST", "/auth/setup", map[string]string{"username": "admin", "password": "supersecret"})
	if code != 200 {
		t.Fatalf("setup: %d %s", code, raw)
	}
	cl.token = env["data"].(map[string]interface{})["token"].(string)

	code, env, raw = cl.do("POST", "/panel/nodes/tickets", map[string]string{"note": "storage test"})
	if code != 200 {
		t.Fatalf("ticket: %d %s", code, raw)
	}
	ticket := env["data"].(map[string]interface{})["ticket"].(string)

	node := newTestApp(t, 0, freePort(t))
	creds, err := cluster.Join(cluster.JoinOptions{
		PanelAddr: panelAddr, Ticket: ticket, Name: "node-storage", GRPCPort: agentGrpc, Version: "test",
	})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	startNodeService(t, cluster.NewAgentService(node.ClusterDeps(), creds.Token, creds.Name, panelAddr), agentGrpc)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := panel.Nodes.Get(creds.NodeID); ok && st.Online() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return panel, node, cl, creds.NodeID, panelAddr, stop
}

// destinationFiles lists the objects inside a destination directory.
func destinationFiles(t *testing.T, root string) []string {
	t.Helper()
	out := []string{}
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out
}

func waitForArchiveCount(t *testing.T, dir string, want int, what string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(dir)
		count := 0
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".zip") || strings.HasSuffix(e.Name(), ".snap.json") {
				count++
			}
		}
		if count == want {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	entries, _ := os.ReadDir(dir)
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Fatalf("%s: %s has %d archives (%v), want %d", what, dir, len(names), names, want)
}

func TestBackupStorageDestination(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	panel, node, cl, nodeID, _, _ := startPanelAndNode(t)

	destination := t.TempDir()
	clusterID := panel.Cfg.Get().ClusterID
	if clusterID == "" {
		t.Fatal("the panel has no cluster id")
	}

	// ---- retention knobs live in the panel settings ----
	code, _, raw := cl.do("PATCH", "/panel/config", map[string]interface{}{
		"backup_local_keep": 1, "backup_remote_keep": 2,
	})
	if code != 200 {
		t.Fatalf("patch config: %d %s", code, raw)
	}
	code, env, raw := cl.do("GET", "/panel/config", nil)
	if code != 200 || !strings.Contains(raw, "backup_remote_keep") || !strings.Contains(raw, clusterID) {
		t.Fatalf("config view: %d %s", code, raw)
	}

	// ---- a storage destination ----
	code, env, raw = cl.do("POST", "/panel/backup-storages", map[string]interface{}{
		"name": "Local vault", "kind": "local", "prefix": "servy",
		"settings": map[string]string{"path": destination}, "enabled": true, "default": true,
	})
	if code != 200 {
		t.Fatalf("create storage: %d %s", code, raw)
	}
	storage := env["data"].(map[string]interface{})
	storageID := storage["storage_id"].(string)
	if storage["namespace"] != clusterID {
		t.Fatalf("storage namespace = %v, want the cluster id %q", storage["namespace"], clusterID)
	}

	// the destination is reachable and writable
	code, _, raw = cl.do("POST", fmt.Sprintf("/panel/backup-storages/%s/test", storageID), nil)
	if code != 200 {
		t.Fatalf("test storage: %d %s", code, raw)
	}
	// ... and secrets never travel to the browser
	code, _, raw = cl.do("POST", "/panel/backup-storages", map[string]interface{}{
		"name": "FTP box", "kind": "ftp", "prefix": "",
		"settings": map[string]string{"host": "ftp.example.com", "username": "u", "password": "topsecret"},
		"enabled":  true,
	})
	if code != 200 {
		t.Fatalf("create ftp storage: %d %s", code, raw)
	}
	if strings.Contains(raw, "topsecret") {
		t.Fatalf("storage secrets leaked to the API: %s", raw)
	}

	// ---- a server on the node with a destination-bound backup config ----
	code, env, raw = cl.do("POST", "/servers", map[string]interface{}{
		"create_type": "raw_exec", "name": "vaulted", "type": "custom",
		"executable": "run.sh", "execution_command": "/bin/bash run.sh",
		"stop_command": "stop", "port": 25777, "crash_detection": false,
		"node": nodeID,
	})
	if code != 200 {
		t.Fatalf("create server: %d %s", code, raw)
	}
	serverID := env["data"].(map[string]interface{})["server_id"].(string)
	code, _, raw = cl.do("PUT", fmt.Sprintf("/servers/%s/files/run.sh", serverID), map[string]string{
		"content": "#!/bin/bash\nsleep 1\n",
	})
	if code != 200 {
		t.Fatalf("write file: %d %s", code, raw)
	}
	code, env, raw = cl.do("POST", fmt.Sprintf("/servers/%s/backups", serverID), map[string]interface{}{
		"name": "vault", "max_backups": 5, "compress": true, "enabled": true, "default": true,
		"storage_id": storageID,
	})
	if code != 200 {
		t.Fatalf("create backup config: %d %s", code, raw)
	}
	backupID := env["data"].(map[string]interface{})["backup_id"].(string)
	if got := env["data"].(map[string]interface{})["storage_id"]; got != storageID {
		t.Fatalf("backup config storage_id = %v, want %q", got, storageID)
	}

	nodeDir := filepath.Join(node.Paths.BackupsDir, "vaulted")
	var archiveNames []string
	for i := 0; i < 3; i++ {
		code, _, raw = cl.do("POST", fmt.Sprintf("/backups/backup/%s/run", backupID), nil)
		if code != 200 {
			t.Fatalf("run backup %d: %d %s", i, code, raw)
		}
		time.Sleep(1100 * time.Millisecond) // distinct timestamps
	}

	// ---- the node uploaded every archive, the local disk keeps one ----
	code, env, raw = cl.do("GET", fmt.Sprintf("/backups/backup/%s/archives", backupID), nil)
	if code != 200 {
		t.Fatalf("archives: %d %s", code, raw)
	}
	archives := env["data"].([]interface{})
	sources := map[string]int{}
	for _, item := range archives {
		entry := item.(map[string]interface{})
		sources[fmt.Sprint(entry["source"])]++
		archiveNames = append(archiveNames, entry["name"].(string))
	}
	if len(archives) != 2 {
		t.Fatalf("archives = %v, want the two newest (one local, one remote only)", archives)
	}
	if sources["both"] != 1 || sources["remote"] != 1 {
		t.Fatalf("archive sources = %v, want one local+remote and one remote only", sources)
	}
	waitForArchiveCount(t, nodeDir, 1, "node cache")

	// namespace of the destination: <prefix>/<cluster>/<server>/<archive>
	clusterPrefix := filepath.Join(destination, "servy", clusterID, serverID)
	objects := destinationFiles(t, clusterPrefix)
	if len(objects) != 2 {
		t.Fatalf("destination holds %v, want 2 archives under %s", objects, clusterPrefix)
	}
	for _, obj := range objects {
		if strings.Count(obj, "/") != 0 {
			t.Fatalf("unexpected object layout: %q", obj)
		}
	}
	// nothing of the destination is outside the cluster namespace
	if outside := destinationFiles(t, filepath.Join(destination, "servy")); len(outside) != 2 {
		t.Fatalf("archives landed outside the cluster namespace: %v", outside)
	}

	// ---- the server never sees the archives of another cluster ----
	otherCluster := filepath.Join(destination, "servy", "panel-other", serverID)
	if _, err := os.Stat(otherCluster); !os.IsNotExist(err) {
		t.Fatalf("another cluster must not have archives here: %v", err)
	}

	// ---- an archive that only lives in the destination can be restored ----
	remoteOnly := ""
	local := ""
	for _, item := range archives {
		entry := item.(map[string]interface{})
		if entry["source"] == "remote" {
			remoteOnly = entry["name"].(string)
		} else {
			local = entry["name"].(string)
		}
	}
	if remoteOnly == "" || local == "" {
		t.Fatalf("expected one local and one remote-only archive, got %s", raw)
	}
	code, _, raw = cl.do("GET", fmt.Sprintf("/backups/backup/%s/download/%s", backupID, remoteOnly), nil)
	if code != 200 {
		t.Fatalf("download remote-only archive: %d %s", code, raw)
	}
	// the download pulled it into the cache, swapping which one is remote only
	waitForArchiveCount(t, nodeDir, 2, "node cache after download")

	code, _, raw = cl.do("POST", fmt.Sprintf("/backups/backup/%s/restore/%s", backupID, remoteOnly), nil)
	if code != 200 {
		t.Fatalf("restore remote-only archive: %d %s", code, raw)
	}
	if _, err := os.Stat(filepath.Join(nodeDir, remoteOnly)); err != nil {
		t.Fatalf("restored archive is not in the node cache: %v", err)
	}

	// ---- a destination that is still in use cannot be dropped silently ----
	code, _, raw = cl.do("DELETE", fmt.Sprintf("/panel/backup-storages/%s", storageID), nil)
	if code != 409 {
		t.Fatalf("deleting an in-use destination = %d %s, want 409", code, raw)
	}

	// ---- deleting the config with its files clears both sides ----
	code, _, raw = cl.do("DELETE", fmt.Sprintf("/backups/backup/%s?files=true", backupID), nil)
	if code != 200 {
		t.Fatalf("delete backup: %d %s", code, raw)
	}
	if left := destinationFiles(t, clusterPrefix); len(left) != 0 {
		t.Fatalf("remote archives survived the delete: %v", left)
	}
	if _, err := os.Stat(nodeDir); !os.IsNotExist(err) {
		t.Fatalf("local archives survived the delete: %v", err)
	}
	// now that nothing points at it, the destination can go
	code, _, raw = cl.do("DELETE", fmt.Sprintf("/panel/backup-storages/%s", storageID), nil)
	if code != 200 {
		t.Fatalf("delete unused destination: %d %s", code, raw)
	}
}

// TestBackupStorageRejectsInvalidConfig covers the validation the UI relies on.
func TestBackupStorageRejectsInvalidConfig(t *testing.T) {
	panelGrpc := freePort(t)
	panelHTTP := freePort(t)
	panel := newTestApp(t, panelGrpc, panelHTTP)
	panelRouter := panel.API.Router()
	go func() { _ = panelRouter.Listen(fmt.Sprintf("127.0.0.1:%d", panelHTTP)) }()
	defer panelRouter.Shutdown()
	waitForPort(t, panelHTTP)

	cl := &client{base: fmt.Sprintf("http://127.0.0.1:%d/api/v1", panelHTTP), http: &http.Client{Timeout: 30 * time.Second}}
	code, env, raw := cl.do("POST", "/auth/setup", map[string]string{"username": "admin", "password": "supersecret"})
	if code != 200 {
		t.Fatalf("setup: %d %s", code, raw)
	}
	cl.token = env["data"].(map[string]interface{})["token"].(string)

	cases := []map[string]interface{}{
		{"name": "", "kind": "local", "settings": map[string]string{"path": "/tmp/x"}},
		{"name": "x", "kind": "carrier-pigeon", "settings": map[string]string{}},
		{"name": "x", "kind": "s3", "settings": map[string]string{"bucket": "b", "access_key": "a", "secret_key": "s", "endpoint": "minio:9000"}},
		{"name": "x", "kind": "ftp", "settings": map[string]string{"host": "h", "username": "u", "tls": "sometimes"}},
		{"name": "x", "kind": "s3", "settings": map[string]string{"bucket": "b", "access_key": "a", "secret_key": "s", "typo": "1"}},
	}
	for i, body := range cases {
		code, _, raw := cl.do("POST", "/panel/backup-storages", body)
		if code == 200 {
			t.Fatalf("case %d was accepted: %s", i, raw)
		}
	}

	// a valid destination works and can be tested
	code, env, raw = cl.do("POST", "/panel/backup-storages", map[string]interface{}{
		"name": "ok", "kind": "local", "settings": map[string]string{"path": t.TempDir()},
	})
	if code != 200 {
		t.Fatalf("valid destination rejected: %d %s", code, raw)
	}
	storageID := env["data"].(map[string]interface{})["storage_id"].(string)
	code, _, raw = cl.do("POST", fmt.Sprintf("/panel/backup-storages/%s/test", storageID), map[string]interface{}{
		"settings": map[string]string{"path": filepath.Join(t.TempDir(), "missing-dir")},
	})
	if code != 200 {
		t.Fatalf("test with an overridden path failed: %d %s", code, raw)
	}
	code, _, raw = cl.do("DELETE", fmt.Sprintf("/panel/backup-storages/%s", storageID), nil)
	if code != 200 {
		t.Fatalf("delete destination: %d %s", code, raw)
	}
	code, env, raw = cl.do("GET", "/panel/backup-storages", nil)
	if code != 200 {
		t.Fatalf("list destinations: %d %s", code, raw)
	}
	if list := env["data"].([]interface{}); len(list) != 0 {
		t.Fatalf("destination survived the delete: %s", raw)
	}
}
