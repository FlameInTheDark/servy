package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/FlameInTheDark/servy/internal/backups"
	pb "github.com/FlameInTheDark/servy/internal/cluster/pb"
	"github.com/FlameInTheDark/servy/internal/logging"
)

// Call timeouts. Control-plane calls are short; transfers and backup jobs get
// generous budgets because they move real data.
const (
	opTimeout      = 20 * time.Second
	transferChunk  = 512 << 10
	syncInterval   = 5 * time.Second
	sweepInterval  = 15 * time.Second
	longOpTimeout  = 60 * time.Minute
	transferBudget = 2 * time.Hour
)

// ErrNodeOffline is returned when a node cannot be reached.
var ErrNodeOffline = errors.New("node is offline")

// Client routes panel operations to remote nodes.
type Client struct {
	registry *NodeRegistry
	log      *logging.Manager

	mu    sync.Mutex
	conns map[string]*nodeConn

	stopOnce sync.Once
	stop     chan struct{}
	wg       sync.WaitGroup
}

type nodeConn struct {
	addr string
	cc   *grpc.ClientConn
	c    pb.NodeServiceClient
}

// NewClient builds the router and starts the background sync loops.
func NewClient(registry *NodeRegistry, log *logging.Manager) *Client {
	return &Client{registry: registry, log: log, conns: map[string]*nodeConn{}, stop: make(chan struct{})}
}

// Start runs node polling (statuses) and the offline sweeper.
func (c *Client) Start() {
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		tick := time.NewTicker(syncInterval)
		defer tick.Stop()
		for {
			select {
			case <-c.stop:
				return
			case <-tick.C:
				c.SyncNodes()
			}
		}
	}()
	go func() {
		defer c.wg.Done()
		tick := time.NewTicker(sweepInterval)
		defer tick.Stop()
		for {
			select {
			case <-c.stop:
				return
			case <-tick.C:
				for _, id := range c.registry.Sweep() {
					c.log.Get("server").Warning("cluster: node %s went offline (no heartbeat)", id)
				}
			}
		}
	}()
}

// Close stops the loops and drops connections.
func (c *Client) Close() {
	c.stopOnce.Do(func() { close(c.stop) })
	c.wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.conns {
		_ = conn.cc.Close()
	}
	c.conns = map[string]*nodeConn{}
}

// SyncNodes refreshes the cached server statuses of every node, marking nodes
// unreachable when the transport fails.
func (c *Client) SyncNodes() {
	for _, id := range c.registry.IDs() {
		c.SyncNode(id)
	}
}

// SyncNode refreshes one node (used right before list/detail responses).
func (c *Client) SyncNode(nodeID string) {
	cl, addr, err := c.clientFor(nodeID)
	if err != nil {
		c.registry.MarkUnreachable(nodeID, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	ctx = c.authCtx(ctx, nodeID)
	res, err := cl.ListServers(ctx, &pb.ListServersRequest{})
	if err != nil {
		c.markTransportError(nodeID, addr, err)
		return
	}
	c.registry.ApplyServers(nodeID, res.Servers)
}

func (c *Client) markTransportError(nodeID, addr string, err error) {
	c.log.Get("server").Warning("cluster: node %s (%s) unreachable: %v", nodeID, addr, err)
	c.registry.MarkUnreachable(nodeID, err.Error())
}

func (c *Client) clientFor(nodeID string) (pb.NodeServiceClient, string, error) {
	addr, ok := c.registry.Address(nodeID)
	if !ok {
		return nil, "", fmt.Errorf("unknown node %s", nodeID)
	}
	token := c.registry.Token(nodeID)
	if token == "" {
		return nil, addr, fmt.Errorf("node %s has no credential; re-join it", nodeID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if conn, ok := c.conns[nodeID]; ok && conn.addr == addr {
		return conn.c, addr, nil
	}
	if old, ok := c.conns[nodeID]; ok {
		_ = old.cc.Close()
		delete(c.conns, nodeID)
	}
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, addr, err
	}
	conn := &nodeConn{addr: addr, cc: cc, c: pb.NewNodeServiceClient(cc)}
	c.conns[nodeID] = conn
	return conn.c, addr, nil
}

// Forget drops the cached connection of a node.
func (c *Client) Forget(nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if conn, ok := c.conns[nodeID]; ok {
		_ = conn.cc.Close()
		delete(c.conns, nodeID)
	}
}

// authCtx attaches this node's credential to an outgoing context.
func (c *Client) authCtx(ctx context.Context, nodeID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.registry.Token(nodeID), "node-id", nodeID)
}

func (c *Client) ctx(nodeID string) (context.Context, context.CancelFunc, pb.NodeServiceClient, error) {
	cl, _, err := c.clientFor(nodeID)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	return c.authCtx(ctx, nodeID), cancel, cl, nil
}

func (c *Client) longCtx(nodeID string) (context.Context, context.CancelFunc, pb.NodeServiceClient, error) {
	cl, _, err := c.clientFor(nodeID)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), longOpTimeout)
	return c.authCtx(ctx, nodeID), cancel, cl, nil
}

/* ----------------------------- server actions ----------------------------- */

func (c *Client) action(nodeID, serverID string, fn func(ctx context.Context, cl pb.NodeServiceClient) (*pb.ServerActionResponse, error)) error {
	ctx, cancel, cl, err := c.ctx(nodeID)
	if err != nil {
		return err
	}
	defer cancel()
	res, err := fn(ctx, cl)
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return err
	}
	if !res.Ok {
		return errors.New(res.Message)
	}
	return nil
}

// StartServer starts a remote server.
func (c *Client) StartServer(nodeID, serverID string) error {
	return c.action(nodeID, serverID, func(ctx context.Context, cl pb.NodeServiceClient) (*pb.ServerActionResponse, error) {
		return cl.StartServer(ctx, &pb.ServerRequest{ServerId: serverID})
	})
}

// StopServer stops a remote server.
func (c *Client) StopServer(nodeID, serverID string) error {
	return c.action(nodeID, serverID, func(ctx context.Context, cl pb.NodeServiceClient) (*pb.ServerActionResponse, error) {
		return cl.StopServer(ctx, &pb.ServerRequest{ServerId: serverID})
	})
}

// RestartServer restarts a remote server.
func (c *Client) RestartServer(nodeID, serverID string) error {
	return c.action(nodeID, serverID, func(ctx context.Context, cl pb.NodeServiceClient) (*pb.ServerActionResponse, error) {
		return cl.RestartServer(ctx, &pb.ServerRequest{ServerId: serverID})
	})
}

// KillServer force-kills a remote server.
func (c *Client) KillServer(nodeID, serverID string) error {
	return c.action(nodeID, serverID, func(ctx context.Context, cl pb.NodeServiceClient) (*pb.ServerActionResponse, error) {
		return cl.KillServer(ctx, &pb.ServerRequest{ServerId: serverID})
	})
}

// SendCommand forwards a console command.
func (c *Client) SendCommand(nodeID, serverID, command string) error {
	ctx, cancel, cl, err := c.ctx(nodeID)
	if err != nil {
		return err
	}
	defer cancel()
	ack, err := cl.SendStdin(ctx, &pb.StdinRequest{ServerId: serverID, Command: command})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return err
	}
	if !ack.Ok {
		return errors.New(ack.Message)
	}
	return nil
}

// Status fetches one server's live status.
func (c *Client) Status(nodeID, serverID string) (*pb.ServerStatusResponse, error) {
	ctx, cancel, cl, err := c.ctx(nodeID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	st, err := cl.ServerStatus(ctx, &pb.ServerRequest{ServerId: serverID})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return nil, err
	}
	return st, nil
}

// Terminal returns buffered console lines of a remote server.
func (c *Client) Terminal(nodeID, serverID string, lines int) ([]string, error) {
	ctx, cancel, cl, err := c.ctx(nodeID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	res, err := cl.ReadTerminal(ctx, &pb.TerminalRequest{ServerId: serverID, Lines: int32(lines)})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return nil, err
	}
	return res.Lines, nil
}

// History returns stored stat points of a remote server.
func (c *Client) History(nodeID, serverID string, hours int) (json.RawMessage, error) {
	ctx, cancel, cl, err := c.ctx(nodeID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	reply, err := cl.ServerHistory(ctx, &pb.HistoryRequest{ServerId: serverID, Hours: int32(hours)})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return nil, err
	}
	if !reply.Ok {
		return nil, errors.New(reply.Message)
	}
	return json.RawMessage(reply.Payload), nil
}

// UpdateConfig applies a settings patch on the node and returns the stored row.
func (c *Client) UpdateConfig(nodeID, serverID string, patch map[string]interface{}) (json.RawMessage, error) {
	raw, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	ctx, cancel, cl, err := c.ctx(nodeID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	reply, err := cl.UpdateServerConfig(ctx, &pb.NodeRequest{ServerId: serverID, Payload: string(raw)})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return nil, err
	}
	if !reply.Ok {
		return nil, errors.New(reply.Message)
	}
	return json.RawMessage(reply.Payload), nil
}

// AgreeEula accepts the EULA on the node.
func (c *Client) AgreeEula(nodeID, serverID string) error {
	return c.simpleReply(nodeID, func(ctx context.Context, cl pb.NodeServiceClient) (*pb.NodeReply, error) {
		return cl.AgreeEula(ctx, &pb.ServerRequest{ServerId: serverID})
	})
}

// UpdateExecutable re-downloads the server files on the node.
func (c *Client) UpdateExecutable(nodeID, serverID string) error {
	ctx, cancel, cl, err := c.longCtx(nodeID)
	if err != nil {
		return err
	}
	defer cancel()
	reply, err := cl.UpdateExecutable(ctx, &pb.ServerRequest{ServerId: serverID})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return err
	}
	if !reply.Ok {
		return errors.New(reply.Message)
	}
	return nil
}

// Clone duplicates a server on the node and returns the new definition.
func (c *Client) Clone(nodeID, serverID, newID, name string) (json.RawMessage, error) {
	ctx, cancel, cl, err := c.longCtx(nodeID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	reply, err := cl.CloneServer(ctx, &pb.CloneServerRequest{ServerId: serverID, NewServerId: newID, Name: name})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return nil, err
	}
	if !reply.Ok {
		return nil, errors.New(reply.Message)
	}
	return json.RawMessage(reply.Payload), nil
}

/* --------------------------------- server --------------------------------- */

// CreateResult is what a node reports after provisioning a server.
type CreateResult struct {
	Result json.RawMessage `json:"result"`
	Def    json.RawMessage `json:"def"`
}

// Create provisions a server on a node using the node's own creation pipeline.
func (c *Client) Create(nodeID, serverID string, req interface{}) (*CreateResult, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel, cl, err := c.longCtx(nodeID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	res, err := cl.CreateServer(ctx, &pb.CreateServerRequest{ServerId: serverID, Definition: string(raw)})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return nil, err
	}
	if !res.Ok {
		return nil, errors.New(res.Message)
	}
	out := &CreateResult{}
	if res.Payload != "" {
		if err := json.Unmarshal([]byte(res.Payload), out); err != nil {
			return nil, fmt.Errorf("node returned an unexpected create payload: %w", err)
		}
	}
	return out, nil
}

// Delete removes a server from a node.
func (c *Client) Delete(nodeID, serverID string, removeFiles bool) error {
	ctx, cancel, cl, err := c.longCtx(nodeID)
	if err != nil {
		return err
	}
	defer cancel()
	res, err := cl.DeleteServer(ctx, &pb.DeleteServerRequest{ServerId: serverID, RemoveFiles: removeFiles})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return err
	}
	if !res.Ok {
		return errors.New(res.Message)
	}
	return nil
}

/* ---------------------------------- files --------------------------------- */

// FileEntry is one directory entry of a remote server.
type FileEntry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Dir      bool   `json:"dir"`
	Modified string `json:"modified"`
}

// ListFiles lists a remote directory.
func (c *Client) ListFiles(nodeID, serverID, path string) ([]FileEntry, error) {
	ctx, cancel, cl, err := c.ctx(nodeID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	res, err := cl.ListFiles(ctx, &pb.FileRequest{ServerId: serverID, Path: path})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return nil, err
	}
	out := make([]FileEntry, 0, len(res.Entries))
	for _, e := range res.Entries {
		out = append(out, FileEntry{Name: e.Name, Size: e.Size, Dir: e.Dir, Modified: e.Modified})
	}
	return out, nil
}

// ReadFile downloads a remote file into memory (bounded by maxBytes).
func (c *Client) ReadFile(nodeID, serverID, path string, maxBytes int64) ([]byte, error) {
	ctx, cancel, cl, err := c.longCtx(nodeID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	stream, err := cl.ReadFile(ctx, &pb.FileStreamRequest{ServerId: serverID, Path: path, MaxBytes: maxBytes})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return nil, err
	}
	var buf []byte
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if chunk.Error != "" {
			return nil, errors.New(chunk.Error)
		}
		buf = append(buf, chunk.Content...)
		if chunk.Eof {
			break
		}
	}
	return buf, nil
}

// WriteFile uploads a local file to a remote server path (streamed).
func (c *Client) WriteFile(nodeID, serverID, path, localPath string) error {
	src, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer src.Close()
	return c.WriteFileFrom(nodeID, serverID, path, src)
}

// WriteFileFrom streams an arbitrary reader into a remote file.
func (c *Client) WriteFileFrom(nodeID, serverID, path string, src io.Reader) error {
	ctx, cancel, cl, err := c.longCtx(nodeID)
	if err != nil {
		return err
	}
	defer cancel()
	stream, err := cl.WriteFile(ctx)
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return err
	}
	buf := make([]byte, transferChunk)
	first := true
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			chunk := &pb.FileChunk{ServerId: serverID, Path: path, Content: buf[:n]}
			if first {
				chunk.Offset = 0
				first = false
			}
			if err := stream.Send(chunk); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	ack, err := stream.CloseAndRecv()
	if err != nil {
		return err
	}
	if !ack.Ok {
		return errors.New(ack.Message)
	}
	return nil
}

// WriteFileBytes writes an in-memory payload to a remote file.
func (c *Client) WriteFileBytes(nodeID, serverID, path string, data []byte) error {
	return c.WriteFileFrom(nodeID, serverID, path, bytesReader(data))
}

// FileOp runs one of the file-manager operations remotely.
func (c *Client) FileOp(nodeID, serverID, op, path, dst string, paths []string) (json.RawMessage, error) {
	ctx, cancel, cl, err := c.ctx(nodeID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	reply, err := cl.FileOp(ctx, &pb.FileOpRequest{ServerId: serverID, Op: op, Path: path, Dst: dst, Paths: paths})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return nil, err
	}
	if !reply.Ok {
		return nil, errors.New(reply.Message)
	}
	return json.RawMessage(reply.Payload), nil
}

/* --------------------------------- backups -------------------------------- */

// BackupRun triggers a backup job on the node and returns the updated metadata.
// The bundle carries the storage destination and retention counts, so the node
// uploads the archive itself.
func (c *Client) BackupRun(nodeID, serverID, backupID string, bundle backups.Bundle) (json.RawMessage, error) {
	payload, err := backups.EncodeBundle(bundle)
	if err != nil {
		return nil, err
	}
	raw := []byte(payload)
	ctx, cancel, cl, err := c.longCtx(nodeID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	reply, err := cl.BackupRun(ctx, &pb.BackupRunRequest{ServerId: serverID, BackupId: backupID, Payload: string(raw)})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return nil, err
	}
	if !reply.Ok {
		return nil, errors.New(reply.Message)
	}
	return json.RawMessage(reply.Payload), nil
}

// BackupRestore restores a backup on the node. The bundle lets the node pull an
// archive back from the storage destination when its local cache rotated it away.
func (c *Client) BackupRestore(nodeID, serverID, backupID, archive string, bundle backups.Bundle) error {
	payload, err := backups.EncodeBundle(bundle)
	if err != nil {
		return err
	}
	ctx, cancel, cl, err := c.longCtx(nodeID)
	if err != nil {
		return err
	}
	defer cancel()
	reply, err := cl.BackupRestore(ctx, &pb.BackupRef{ServerId: serverID, BackupId: backupID, Archive: archive, Payload: payload})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return err
	}
	if !reply.Ok {
		return errors.New(reply.Message)
	}
	return nil
}

// BackupDelete removes a backup (optionally its files) on the node, including
// the archives the bundle's destination holds for that server.
func (c *Client) BackupDelete(nodeID, serverID, backupID string, removeFiles bool, bundle backups.Bundle) error {
	payload, err := backups.EncodeBundle(bundle)
	if err != nil {
		return err
	}
	ctx, cancel, cl, err := c.longCtx(nodeID)
	if err != nil {
		return err
	}
	defer cancel()
	reply, err := cl.BackupDelete(ctx, &pb.BackupRef{ServerId: serverID, BackupId: backupID, RemoveFiles: removeFiles, Payload: payload})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return err
	}
	if !reply.Ok {
		return errors.New(reply.Message)
	}
	return nil
}

// BackupFiles lists the archives stored on the node; when archive is set it
// lists the members inside that archive instead.
func (c *Client) BackupFiles(nodeID, serverID, backupID, archive string, bundle backups.Bundle) (json.RawMessage, error) {
	payload, err := backups.EncodeBundle(bundle)
	if err != nil {
		return nil, err
	}
	ctx, cancel, cl, err := c.ctx(nodeID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	reply, err := cl.BackupFiles(ctx, &pb.BackupRef{ServerId: serverID, BackupId: backupID, Archive: archive, Payload: payload})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return nil, err
	}
	if !reply.Ok {
		return nil, errors.New(reply.Message)
	}
	return json.RawMessage(reply.Payload), nil
}

// BackupRead downloads an archive file from the node. When the archive only
// exists in the storage destination the node fetches it first.
func (c *Client) BackupRead(nodeID, serverID, backupID, path string, bundle backups.Bundle) ([]byte, error) {
	payload, err := backups.EncodeBundle(bundle)
	if err != nil {
		return nil, err
	}
	ctx, cancel, cl, err := c.longCtx(nodeID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	stream, err := cl.BackupRead(ctx, &pb.BackupReadRequest{ServerId: serverID, BackupId: backupID, Path: path, Payload: payload})
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return nil, err
	}
	var buf []byte
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk.Content...)
		if chunk.Eof {
			break
		}
	}
	return buf, nil
}

/* --------------------------- wizard archive upload ------------------------- */

// UploadArchive streams a local archive to a node and returns the spooled path.
func (c *Client) UploadArchive(nodeID, localPath string) (string, error) {
	src, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	return c.UploadArchiveFrom(nodeID, src, filepathBase(localPath))
}

// UploadArchiveFrom streams an arbitrary reader to a node's import dir.
func (c *Client) UploadArchiveFrom(nodeID string, src io.Reader, name string) (string, error) {
	cl, _, err := c.clientFor(nodeID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), transferBudget)
	defer cancel()
	ctx = c.authCtx(ctx, nodeID)

	stream, err := cl.UploadArchive(ctx)
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return "", err
	}
	transferID := randomHex(8)
	buf := make([]byte, transferChunk)
	first := true
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			chunk := &pb.ArchiveChunk{TransferId: transferID, Content: buf[:n]}
			if first {
				chunk.Name = name
				first = false
			}
			if err := stream.Send(chunk); err != nil {
				return "", err
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
	}
	reply, err := stream.CloseAndRecv()
	if err != nil {
		return "", err
	}
	if !reply.Ok {
		return "", errors.New(reply.Message)
	}
	return reply.Payload, nil
}

/* --------------------------------- helpers -------------------------------- */

func (c *Client) simpleReply(nodeID string, fn func(ctx context.Context, cl pb.NodeServiceClient) (*pb.NodeReply, error)) error {
	ctx, cancel, cl, err := c.ctx(nodeID)
	if err != nil {
		return err
	}
	defer cancel()
	reply, err := fn(ctx, cl)
	if err != nil {
		c.markTransportError(nodeID, "", err)
		return err
	}
	if !reply.Ok {
		return errors.New(reply.Message)
	}
	return nil
}

type byteReader struct {
	data []byte
	pos  int
}

func bytesReader(b []byte) *byteReader { return &byteReader{data: b} }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func filepathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

// Ping verifies that a node answers and returns its version.
func (c *Client) Ping(nodeID string) (string, error) {
	ctx, cancel, cl, err := c.ctx(nodeID)
	if err != nil {
		return "", err
	}
	defer cancel()
	if _, err := cl.ListServers(ctx, &pb.ListServersRequest{}); err != nil {
		c.markTransportError(nodeID, "", err)
		return "", err
	}
	st, _ := c.registry.Get(nodeID)
	return st.Version, nil
}
