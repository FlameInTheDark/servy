package core

// Cluster acceptance test: a second node joins the panel with a single-use
// ticket, then the panel creates, starts, reads files from, backs up and
// deletes that server over gRPC — exactly the flows the UI drives.

import (
	"archive/zip"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/FlameInTheDark/servy/internal/cluster"
	pb "github.com/FlameInTheDark/servy/internal/cluster/pb"
	"github.com/FlameInTheDark/servy/internal/config"
)

// freePort grabs a currently unused TCP port.
func freePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	return lis.Addr().(*net.TCPAddr).Port
}

// newTestApp boots a full application on a temp data dir.
func newTestApp(t *testing.T, grpcPort int, httpPort int) *App {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("http_host: 127.0.0.1\nhttps_port: %d\ngrpc_enabled: true\ngrpc_port: %d\nstats_update_frequency_seconds: 5\n", httpPort, grpcPort)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Stop)
	return app
}

// startNodeService serves the node API of an app on a fixed port.
func startNodeService(t *testing.T, svc *cluster.Service, port int) {
	t.Helper()
	go func() {
		if err := cluster.ServeGRPC(svc, fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
			t.Logf("node service stopped: %v", err)
		}
	}()
	waitForPort(t, port)
}

func waitForPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("port %d never opened", port)
}

// heartbeat sends one agent heartbeat to the panel, keeping the node fresh.
func heartbeat(t *testing.T, panelAddr, nodeID, token string) {
	t.Helper()
	cc, err := grpc.NewClient(panelAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token, "node-id", nodeID)
	if _, err := pb.NewNodeServiceClient(cc).Heartbeat(ctx, &pb.HeartbeatRequest{
		NodeId: nodeID, CpuUsage: 21.5, MemPercent: 42, TotalServers: 1, RunningServers: 1,
	}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
}

func TestClusterNodeJoinAndRemoteServerLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}

	panelGrpc := freePort(t)
	agentGrpc := freePort(t)
	panelHTTP := freePort(t)

	// ---- panel ----
	panel := newTestApp(t, panelGrpc, panelHTTP)
	panelAddr := fmt.Sprintf("127.0.0.1:%d", panelGrpc)
	go func() { _ = panel.StartGRPC(panelAddr) }()
	waitForPort(t, panelGrpc)

	panelRouter := panel.API.Router()
	go func() { _ = panelRouter.Listen(fmt.Sprintf("127.0.0.1:%d", panelHTTP)) }()
	defer panelRouter.Shutdown()
	waitForPort(t, panelHTTP)

	cl := &client{base: fmt.Sprintf("http://127.0.0.1:%d/api/v1", panelHTTP), http: &http.Client{Timeout: 60 * time.Second}}
	code, env, raw := cl.do("POST", "/auth/setup", map[string]string{"username": "admin", "password": "supersecret"})
	if code != 200 {
		t.Fatalf("setup: %d %s", code, raw)
	}
	cl.token = env["data"].(map[string]interface{})["token"].(string)

	// ---- the panel advertises itself and can issue tickets ----
	code, env, raw = cl.do("GET", "/panel/nodes", nil)
	if code != 200 {
		t.Fatalf("list nodes: %d %s", code, raw)
	}
	nodesData := env["data"].(map[string]interface{})
	if len(nodesData["nodes"].([]interface{})) != 1 {
		t.Fatalf("expected only the local node before joining, got %s", raw)
	}

	code, env, raw = cl.do("POST", "/panel/nodes/tickets", map[string]string{"note": "integration"})
	if code != 200 {
		t.Fatalf("issue ticket: %d %s", code, raw)
	}
	ticket := env["data"].(map[string]interface{})["ticket"].(string)

	// ---- node: fresh app + join with the ticket ----
	node := newTestApp(t, 0, freePort(t))
	creds, err := cluster.Join(cluster.JoinOptions{
		PanelAddr: panelAddr, Ticket: ticket, Name: "node-a", GRPCPort: agentGrpc, Version: "test",
	})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if creds.NodeID == "" || creds.Token == "" {
		t.Fatalf("join returned empty credentials: %+v", creds)
	}
	startNodeService(t, cluster.NewAgentService(node.ClusterDeps(), creds.Token, creds.Name, panelAddr), agentGrpc)

	// tickets are single use
	if _, err := cluster.Join(cluster.JoinOptions{PanelAddr: panelAddr, Ticket: ticket, Name: "replay", GRPCPort: agentGrpc}); err == nil {
		t.Fatal("a redeemed ticket must not be reusable")
	}

	// ---- the panel sees the node online and can dial it ----
	deadline := time.Now().Add(10 * time.Second)
	var online bool
	for time.Now().Before(deadline) {
		if st, ok := panel.Nodes.Get(creds.NodeID); ok && st.Online() {
			online = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !online {
		t.Fatal("joined node never appeared online in the panel registry")
	}
	st, _ := panel.Nodes.Get(creds.NodeID)
	if st.Address != fmt.Sprintf("127.0.0.1:%d", agentGrpc) {
		t.Fatalf("node address = %q, want the address the panel can dial", st.Address)
	}
	if _, err := panel.Cluster.Ping(creds.NodeID); err != nil {
		t.Fatalf("panel cannot dial the node: %v", err)
	}

	heartbeat(t, panelAddr, creds.NodeID, creds.Token)
	if st, _ := panel.Nodes.Get(creds.NodeID); st.CPU != 21.5 || st.MemPct != 42 {
		t.Fatalf("heartbeat telemetry not applied: %+v", st)
	}

	// the join flow must never expose a shared secret again
	code, env, raw = cl.do("GET", "/panel/nodes", nil)
	if code != 200 {
		t.Fatalf("list nodes after join: %d %s", code, raw)
	}
	nodes := env["data"].(map[string]interface{})["nodes"].([]interface{})
	found := false
	for _, n := range nodes {
		item := n.(map[string]interface{})
		if item["node_id"] == creds.NodeID {
			found = true
			if item["online"] != true {
				t.Fatalf("joined node reported offline: %s", raw)
			}
		}
	}
	if !found {
		t.Fatalf("joined node missing from /panel/nodes: %s", raw)
	}

	// ---- create a server on the node (wizard path) ----
	code, env, raw = cl.do("POST", "/servers", map[string]interface{}{
		"create_type": "raw_exec", "name": "remote-fake", "type": "custom",
		"executable": "run.sh", "execution_command": "/bin/bash run.sh",
		"stop_command": "stop", "port": 25599, "crash_detection": false,
		"node": creds.NodeID,
	})
	if code != 200 {
		t.Fatalf("create on node: %d %s", code, raw)
	}
	created := env["data"].(map[string]interface{})
	if created["remote"] != true || created["node_id"] != creds.NodeID {
		t.Fatalf("create response missing node placement: %s", raw)
	}
	serverID := created["server_id"].(string)

	// the panel mirrors the row: it shows up in the list with its node
	code, env, raw = cl.do("GET", "/servers", nil)
	if code != 200 || !strings.Contains(raw, serverID) || !strings.Contains(raw, creds.NodeID) {
		t.Fatalf("panel does not list the remote server: %d %s", code, raw)
	}

	// ... and it is really on the node
	list, err := node.DB.ListServers()
	if err != nil || len(list) != 1 {
		t.Fatalf("node has %d servers (err %v), want 1", len(list), err)
	}

	// ---- files: write the run script through the panel ----
	script := "#!/bin/bash\necho SERVER_STARTED\nwhile IFS= read -r line; do\n  if [ \"$line\" = \"stop\" ]; then echo STOPPING; exit 0; fi\n  echo \"ECHO: $line\"\ndone\n"
	code, _, raw = cl.do("PUT", fmt.Sprintf("/servers/%s/files/run.sh", serverID), map[string]string{"content": script})
	if code != 200 {
		t.Fatalf("write remote file: %d %s", code, raw)
	}
	code, _, raw = cl.do("GET", fmt.Sprintf("/servers/%s/files?path=", serverID), nil)
	if code != 200 || !strings.Contains(raw, "run.sh") {
		t.Fatalf("list remote files: %d %s", code, raw)
	}

	// ---- lifecycle: start, console, stdin ----
	code, _, raw = cl.do("POST", fmt.Sprintf("/servers/%s/start", serverID), nil)
	if code != 200 {
		t.Fatalf("remote start: %d %s", code, raw)
	}
	time.Sleep(1500 * time.Millisecond)
	code, _, raw = cl.do("GET", fmt.Sprintf("/servers/%s/terminal", serverID), nil)
	if code != 200 || !strings.Contains(raw, "SERVER_STARTED") {
		t.Fatalf("remote terminal missing startup line: %d %s", code, raw)
	}
	code, _, raw = cl.do("POST", fmt.Sprintf("/servers/%s/stdin", serverID), map[string]string{"command": "hello node"})
	if code != 200 {
		t.Fatalf("remote stdin: %d %s", code, raw)
	}
	time.Sleep(800 * time.Millisecond)
	code, _, raw = cl.do("GET", fmt.Sprintf("/servers/%s/terminal", serverID), nil)
	if !strings.Contains(raw, "ECHO: hello node") {
		t.Fatalf("remote stdin echo missing: %s", raw)
	}
	// the panel learns the new state from the node (cached status refresh)
	stateDeadline := time.Now().Add(15 * time.Second)
	var state interface{}
	for time.Now().Before(stateDeadline) {
		code, env, raw = cl.do("GET", fmt.Sprintf("/servers/%s", serverID), nil)
		if code != 200 {
			t.Fatalf("remote status: %d %s", code, raw)
		}
		state = env["data"].(map[string]interface{})["state"]
		if state == "running" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if state != "running" {
		st, _ := panel.Nodes.Get(creds.NodeID)
		t.Fatalf("remote server state = %v, want running (node status=%s err=%q, %s)", state, st.Status, st.LastError, raw)
	}

	// ---- config editor: server.properties round-trip on the node ----
	code, _, raw = cl.do("PUT", fmt.Sprintf("/servers/%s/config-file?path=server.properties", serverID),
		map[string]string{"mode": "raw", "content": "motd=hello-node\n"})
	if code != 200 {
		t.Fatalf("remote config write: %d %s", code, raw)
	}
	code, _, raw = cl.do("GET", fmt.Sprintf("/servers/%s/config-file?path=server.properties", serverID), nil)
	if code != 200 || !strings.Contains(raw, "hello-node") {
		t.Fatalf("remote config read: %d %s", code, raw)
	}

	// ---- settings patch is applied on the node and mirrored back ----
	code, _, raw = cl.do("PATCH", fmt.Sprintf("/servers/%s", serverID), map[string]interface{}{"server_name": "remote-renamed"})
	if code != 200 {
		t.Fatalf("remote patch: %d %s", code, raw)
	}
	code, env, raw = cl.do("GET", fmt.Sprintf("/servers/%s", serverID), nil)
	if code != 200 || !strings.Contains(raw, "remote-renamed") {
		t.Fatalf("patch not mirrored: %d %s", code, raw)
	}
	local, _ := node.DB.GetServer(serverID)
	if local.ServerName != "remote-renamed" {
		t.Fatalf("node row not renamed: %q", local.ServerName)
	}

	// ---- backups: config on the panel, archive on the node ----
	code, env, raw = cl.do("POST", fmt.Sprintf("/servers/%s/backups", serverID), map[string]interface{}{
		"name": "manual", "max_backups": 3, "compress": true, "enabled": true, "default": true,
	})
	if code != 200 {
		t.Fatalf("create backup config: %d %s", code, raw)
	}
	backupID := env["data"].(map[string]interface{})["backup_id"].(string)

	code, _, raw = cl.do("POST", fmt.Sprintf("/backups/backup/%s/run", backupID), nil)
	if code != 200 {
		t.Fatalf("run remote backup: %d %s", code, raw)
	}
	code, env, raw = cl.do("GET", fmt.Sprintf("/backups/backup/%s/archives", backupID), nil)
	if code != 200 {
		t.Fatalf("remote archives: %d %s", code, raw)
	}
	archives := env["data"].([]interface{})
	if len(archives) != 1 {
		t.Fatalf("expected exactly one archive on the node, got %s", raw)
	}
	archiveName := archives[0].(map[string]interface{})["name"].(string)

	code, _, raw = cl.do("GET", fmt.Sprintf("/backups/backup/%s/files/%s", backupID, archiveName), nil)
	if code != 200 || !strings.Contains(raw, "run.sh") {
		t.Fatalf("archive member listing: %d %s", code, raw)
	}

	// the archive really lives on the node, not the panel
	_, panelStatErr := os.Stat(filepath.Join(panel.Paths.BackupsDir, "remote-renamed", archiveName))
	if panelStatErr == nil {
		t.Fatal("the archive was written on the panel, it must live on the node")
	}
	nodeArchive := filepath.Join(node.Paths.BackupsDir, "remote-renamed", archiveName)
	if _, err := os.Stat(nodeArchive); err != nil {
		t.Fatalf("archive missing on the node: %v", err)
	}

	// ---- remote delete removes both sides ----
	code, _, raw = cl.do("POST", fmt.Sprintf("/servers/%s/stop", serverID), nil)
	if code != 200 {
		t.Fatalf("remote stop: %d %s", code, raw)
	}
	code, _, raw = cl.do("DELETE", fmt.Sprintf("/servers/%s?files=true", serverID), nil)
	if code != 200 {
		t.Fatalf("remote delete: %d %s", code, raw)
	}
	if rows, _ := node.DB.ListServers(); len(rows) != 0 {
		t.Fatalf("node still has %d servers after delete", len(rows))
	}
	if _, err := panel.DB.GetServer(serverID); err == nil {
		t.Fatal("panel row survived the delete")
	}
	if _, err := panel.DB.GetBackup(backupID); err == nil {
		t.Fatal("panel backup config survived the server delete")
	}

	// ---- wizard zip import runs on the node too ----
	zipPath := filepath.Join(panel.Paths.ImportDir, "sample.zip")
	if err := os.MkdirAll(panel.Paths.ImportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTestZip(zipPath); err != nil {
		t.Fatal(err)
	}
	code, env, raw = cl.do("POST", "/servers", map[string]interface{}{
		"create_type": "import_zip", "name": "imported-remote", "type": "minecraft-java",
		"archive_path": zipPath, "port": 25602, "node": creds.NodeID,
	})
	if code != 200 {
		t.Fatalf("remote zip import: %d %s", code, raw)
	}
	importedID := env["data"].(map[string]interface{})["server_id"].(string)
	// the archive was streamed to the node, extracted there and cleaned up
	nodeImported, err := node.DB.GetServer(importedID)
	if err != nil {
		t.Fatalf("node does not have the imported server: %v", err)
	}
	if nodeImported.ServerName != "imported-remote" {
		t.Fatalf("node row name = %q", nodeImported.ServerName)
	}
	if _, err := os.Stat(filepath.Join(nodeImported.Path, "server.jar")); err != nil {
		t.Fatalf("archive contents missing on the node: %v", err)
	}
	nodeSpool, _ := os.ReadDir(node.Paths.ImportDir)
	for _, e := range nodeSpool {
		if strings.HasSuffix(e.Name(), ".zip") {
			t.Fatalf("node kept the uploaded archive %s", e.Name())
		}
	}
	// and the panel lists it as a node server
	code, _, raw = cl.do("GET", "/servers", nil)
	if code != 200 || !strings.Contains(raw, importedID) {
		t.Fatalf("imported remote server missing from the panel list: %d %s", code, raw)
	}

	// ---- removing a node is guarded while it hosts servers ----
	code, env, raw = cl.do("POST", "/servers", map[string]interface{}{
		"create_type": "raw_exec", "name": "keep-me", "type": "custom",
		"executable": "run.sh", "execution_command": "/bin/bash run.sh",
		"stop_command": "stop", "port": 25598, "node": creds.NodeID,
	})
	if code != 200 {
		t.Fatalf("second remote create: %d %s", code, raw)
	}
	code, env, raw = cl.do("DELETE", "/panel/nodes/"+creds.NodeID, nil)
	if code != 409 {
		t.Fatalf("deleting a node with servers should be refused, got %d %s", code, raw)
	}
	if env["error"] != "NODE_HAS_SERVERS" {
		t.Fatalf("unexpected guard error: %s", raw)
	}
	code, _, raw = cl.do("DELETE", "/panel/nodes/"+creds.NodeID+"?force=true", nil)
	if code != 200 {
		t.Fatalf("forced node removal: %d %s", code, raw)
	}
	if _, ok := panel.Nodes.Get(creds.NodeID); ok {
		t.Fatal("node still in the registry after removal")
	}
	// revoked credentials can no longer talk to the panel
	cc, err := grpc.NewClient(panelAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+creds.Token, "node-id", creds.NodeID)
	revoked, err := pb.NewNodeServiceClient(cc).Heartbeat(ctx, &pb.HeartbeatRequest{NodeId: creds.NodeID})
	if err != nil {
		t.Fatalf("heartbeat rpc error: %v", err)
	}
	if revoked.Ok {
		t.Fatal("a removed node's credential still authenticates")
	}
}

// TestClusterRejectsBadCredentials guards the two open-endpoint bugs of the
// previous implementation: no token and no matching node must both fail.
func TestClusterRejectsBadCredentials(t *testing.T) {
	panelGrpc := freePort(t)
	panel := newTestApp(t, panelGrpc, freePort(t))
	panelAddr := fmt.Sprintf("127.0.0.1:%d", panelGrpc)
	go func() { _ = panel.StartGRPC(panelAddr) }()
	waitForPort(t, panelGrpc)

	cc, err := grpc.NewClient(panelAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	node := pb.NewNodeServiceClient(cc)

	// no credential at all
	res, err := node.Heartbeat(context.Background(), &pb.HeartbeatRequest{NodeId: "nope"})
	if err != nil {
		t.Fatalf("heartbeat rpc error: %v", err)
	}
	if res.Ok {
		t.Fatal("heartbeat without a credential was accepted")
	}
	// wrong credential
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer deadbeef", "node-id", "nope")
	res, err = node.Heartbeat(ctx, &pb.HeartbeatRequest{NodeId: "nope"})
	if err != nil {
		t.Fatalf("heartbeat rpc error: %v", err)
	}
	if res.Ok {
		t.Fatal("heartbeat with an unknown credential was accepted")
	}
	// join with a bogus ticket
	joinRes, err := node.Join(ctx, &pb.JoinRequest{Ticket: "not-a-ticket", Name: "intruder", GrpcPort: 50052})
	if err != nil {
		t.Fatalf("join rpc error: %v", err)
	}
	if joinRes.Accepted {
		t.Fatal("join with a bogus ticket was accepted")
	}
}

// writeTestZip creates a minimal importable server archive.
func writeTestZip(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	entry, err := zw.Create("server.jar")
	if err != nil {
		return err
	}
	if _, err := entry.Write([]byte("jar")); err != nil {
		return err
	}
	return zw.Close()
}
