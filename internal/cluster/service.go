package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/FlameInTheDark/servy/internal/backups"
	pb "github.com/FlameInTheDark/servy/internal/cluster/pb"
	"github.com/FlameInTheDark/servy/internal/creation"
	"github.com/FlameInTheDark/servy/internal/database"
	"github.com/FlameInTheDark/servy/internal/events"
	"github.com/FlameInTheDark/servy/internal/files"
	"github.com/FlameInTheDark/servy/internal/logging"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
	"github.com/FlameInTheDark/servy/internal/servers"
)

// Deps is everything a node needs to run remote operations. On the panel only
// the registry/DB are used (the panel serves the membership RPCs); on an agent
// the creation/backup/file services do the actual work.
type Deps struct {
	DB        *database.Queries
	Log       *logging.Manager
	Bus       *events.Bus
	Registry  *servers.Registry
	Creation  *creation.Service
	Backups   *backups.Service
	Files     *files.Manager
	HostStats *servers.HostStats

	DataDir     string // agent data dir (imports, backups)
	ServersRoot string
	ImportsDir  string
	BackupsRoot string

	Version string
}

// Service implements pb.NodeService. The same implementation runs on the panel
// (membership + joining) and on agents (server operations).
type Service struct {
	pb.UnimplementedNodeServiceServer

	deps Deps

	// role is "panel" or "agent".
	role string
	// nodeName is this node's display name (agents).
	nodeName string

	// Panel-side membership. Nil on agents.
	nodes   *NodeRegistry
	tickets *TicketStore

	// Agent-side credential (panel calls are authenticated with it).
	token    string
	panelURL string
}

// NewPanelService builds the panel-side service (membership + joins).
func NewPanelService(deps Deps, nodes *NodeRegistry, tickets *TicketStore) *Service {
	return &Service{deps: deps, role: "panel", nodes: nodes, tickets: tickets}
}

// NewAgentService builds the agent-side service (runs server operations).
func NewAgentService(deps Deps, token, nodeName, panelAddr string) *Service {
	return &Service{deps: deps, role: "agent", token: token, nodeName: nodeName, panelURL: panelAddr}
}

func (s *Service) log() *logging.Manager { return s.deps.Log }

/* --------------------------------- auth ---------------------------------- */

// incomingToken extracts the bearer credential of a call.
func incomingToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, v := range md.Get("authorization") {
		if len(v) > 7 && v[:7] == "Bearer " {
			return v[7:]
		}
	}
	return ""
}

func incomingNodeID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if v := md.Get("node-id"); len(v) > 0 {
		return v[0]
	}
	return ""
}

// authorize validates the caller credential. Panels accept registered agent
// tokens; agents accept their own credential (i.e. only their panel).
func (s *Service) authorize(ctx context.Context, nodeID string) error {
	token := incomingToken(ctx)
	if token == "" {
		return errors.New("missing node credential")
	}
	if s.role == "panel" {
		if s.nodes == nil {
			return errors.New("cluster not initialised")
		}
		authNode, ok := s.nodes.Authenticate(token)
		if !ok {
			return errors.New("unknown node credential")
		}
		if nodeID != "" && nodeID != authNode {
			return fmt.Errorf("credential belongs to node %s", authNode)
		}
		s.nodes.Touch(authNode, "")
		return nil
	}
	// agent role: single known panel credential
	if !ConstantTimeEqual(token, s.token) {
		return errors.New("invalid panel credential")
	}
	return nil
}

/* ------------------------------ membership ------------------------------- */

// Join exchanges a one-time ticket for a permanent per-node credential.
func (s *Service) Join(ctx context.Context, req *pb.JoinRequest) (*pb.JoinResponse, error) {
	if s.role != "panel" {
		return &pb.JoinResponse{Accepted: false, Message: "this node does not accept joins"}, nil
	}
	nodeID := newNodeID()
	note, ok := s.tickets.Redeem(req.Ticket, nodeID)
	if !ok {
		return &pb.JoinResponse{Accepted: false, Message: "join ticket is invalid, used or expired"}, nil
	}
	name := sanitizeNodeName(req.Name)
	if name == "" {
		name = sanitizeNodeName(req.Hostname)
	}
	if name == "" {
		name = nodeID
	}
	address := deriveNodeAddress(req.Advertise, peerIP(ctx), req.GrpcPort)
	token := randomHex(32)
	caps := req.Capabilities
	if len(caps) == 0 {
		caps = []string{"servers", "files", "terminal", "backups", "creation"}
	}
	now := database.Now()
	capsJSON := strings.Join(caps, ",")
	version := req.Version
	if err := s.deps.DB.UpsertNode(nodeID, name, address, "agent", "online", &now, &version, &capsJSON, &token, now); err != nil {
		return &pb.JoinResponse{Accepted: false, Message: err.Error()}, nil
	}
	if s.nodes != nil {
		s.nodes.Register(nodeID, name, address, version, token, caps)
	}
	s.log().Get("server").Info("cluster: node %q joined (%s at %s, ticket %q)", name, nodeID, address, note)
	if s.deps.Bus != nil {
		s.deps.Bus.PublishSimple("node_joined", map[string]interface{}{
			"node_id": nodeID, "name": name, "address": address,
		})
	}
	return &pb.JoinResponse{
		Accepted: true, Message: "welcome " + name, NodeId: nodeID, Token: token,
	}, nil
}

// Hello confirms a registered node (agent → panel).
func (s *Service) Hello(ctx context.Context, req *pb.HelloRequest) (*pb.HelloResponse, error) {
	if err := s.authorize(ctx, req.NodeId); err != nil {
		return &pb.HelloResponse{Accepted: false, Message: err.Error()}, nil
	}
	if s.role == "panel" && req.NodeId != "" {
		s.nodes.Touch(req.NodeId, req.Version)
		if st, ok := s.nodes.Get(req.NodeId); ok {
			name := st.Name
			if req.Name != "" {
				name = sanitizeNodeName(req.Name)
			}
			// a machine can change IP (DHCP, docker network, VPN): keep the
			// dialable port but follow the peer address the node reports from.
			address := st.Address
			if ip := peerIP(ctx); ip != "" {
				if _, port, err := net.SplitHostPort(address); err == nil && port != "" {
					address = net.JoinHostPort(ip, port)
				}
			}
			now := database.Now()
			version := req.Version
			caps := strings.Join(req.Capabilities, ",")
			// refresh the durable row; the credential is carried over as-is so a
			// hello can never invalidate a node's join
			token := s.nodes.Token(st.NodeID)
			_ = s.deps.DB.UpsertNode(st.NodeID, name, address, "agent", "online", &now, &version, &caps, &token, st.Created.Format(time.RFC3339))
			s.nodes.Register(st.NodeID, name, address, version, token, req.Capabilities)
		}
	}
	return &pb.HelloResponse{Accepted: true, Message: "welcome", PanelVersion: s.deps.Version}, nil
}

// Heartbeat stores telemetry and keeps the node marked online.
func (s *Service) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	if err := s.authorize(ctx, req.NodeId); err != nil {
		return &pb.HeartbeatResponse{Ok: false, Message: err.Error()}, nil
	}
	if s.role != "panel" {
		return &pb.HeartbeatResponse{Ok: true}, nil
	}
	s.nodes.ApplyHeartbeat(req.NodeId, req)
	// persist last_seen at most every 15s to keep SQLite writes low
	if st, ok := s.nodes.Get(req.NodeId); ok {
		if time.Since(st.LastSeen) > 20*time.Second || st.LastSeen.IsZero() {
			now := database.Now()
			version := req.Version
			_ = s.deps.DB.UpdateNodeStatus("online", &now, &version, req.NodeId)
		}
	}
	return &pb.HeartbeatResponse{Ok: true}, nil
}

/* ---------------------------- server operations --------------------------- */

func (s *Service) reply(ok bool, msg string) (*pb.ServerActionResponse, error) {
	return &pb.ServerActionResponse{Ok: ok, Message: msg}, nil
}

func (s *Service) local(nodeID string, serverID string) (*servers.Supervisor, error) {
	if s.deps.Registry == nil {
		return nil, errors.New("this node has no server registry")
	}
	return s.deps.Registry.Get(serverID)
}

// CreateServer provisions a server on this node from a wizard request.
func (s *Service) CreateServer(ctx context.Context, req *pb.CreateServerRequest) (*pb.ServerActionResponse, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return s.reply(false, err.Error())
	}
	if s.deps.Creation == nil {
		return s.reply(false, "this node cannot create servers")
	}
	var createReq creation.Request
	if err := json.Unmarshal([]byte(req.Definition), &createReq); err != nil {
		return s.reply(false, "invalid creation request: "+err.Error())
	}
	res, err := s.deps.Creation.Create(createReq, 0)
	if err != nil {
		return s.reply(false, err.Error())
	}
	def, err := s.deps.DB.GetServer(res.ServerID)
	if err != nil {
		return s.reply(false, "server created but not readable: "+err.Error())
	}
	payload, _ := json.Marshal(map[string]interface{}{"result": res, "def": def})
	return &pb.ServerActionResponse{Ok: true, Message: res.ServerID, Payload: string(payload)}, nil
}

// DeleteServer removes a server from this node.
func (s *Service) DeleteServer(ctx context.Context, req *pb.DeleteServerRequest) (*pb.ServerActionResponse, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return s.reply(false, err.Error())
	}
	if s.deps.Registry == nil {
		return s.reply(false, "no registry on this node")
	}
	if err := s.deps.Registry.Delete(req.ServerId, req.RemoveFiles, 0); err != nil {
		return s.reply(false, err.Error())
	}
	return s.reply(true, "deleted")
}

func (s *Service) StartServer(ctx context.Context, req *pb.ServerRequest) (*pb.ServerActionResponse, error) {
	return s.withLocal(ctx, req.ServerId, func(sup *servers.Supervisor) error { return sup.Start(0) })
}

func (s *Service) StopServer(ctx context.Context, req *pb.ServerRequest) (*pb.ServerActionResponse, error) {
	return s.withLocal(ctx, req.ServerId, func(sup *servers.Supervisor) error { return sup.Stop("cluster", 0) })
}

func (s *Service) RestartServer(ctx context.Context, req *pb.ServerRequest) (*pb.ServerActionResponse, error) {
	return s.withLocal(ctx, req.ServerId, func(sup *servers.Supervisor) error { return sup.Restart(0, nil) })
}

func (s *Service) KillServer(ctx context.Context, req *pb.ServerRequest) (*pb.ServerActionResponse, error) {
	return s.withLocal(ctx, req.ServerId, func(sup *servers.Supervisor) error { return sup.Kill(0) })
}

func (s *Service) withLocal(ctx context.Context, serverID string, fn func(*servers.Supervisor) error) (*pb.ServerActionResponse, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return s.reply(false, err.Error())
	}
	sup, err := s.local("", serverID)
	if err != nil {
		return s.reply(false, err.Error())
	}
	if err := fn(sup); err != nil {
		return s.reply(false, err.Error())
	}
	return s.reply(true, "ok")
}

// ServerStatus returns live status for one server.
func (s *Service) ServerStatus(ctx context.Context, req *pb.ServerRequest) (*pb.ServerStatusResponse, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return nil, err
	}
	sup, err := s.local("", req.ServerId)
	if err != nil {
		return nil, err
	}
	return statusOf(sup), nil
}

// ListServers lists this node's servers with status.
func (s *Service) ListServers(ctx context.Context, _ *pb.ListServersRequest) (*pb.ListServersResponse, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return nil, err
	}
	out := &pb.ListServersResponse{Servers: []*pb.ServerStatusResponse{}}
	if s.deps.Registry == nil {
		return out, nil
	}
	for _, sup := range s.deps.Registry.All() {
		out.Servers = append(out.Servers, statusOf(sup))
	}
	return out, nil
}

func statusOf(sup *servers.Supervisor) *pb.ServerStatusResponse {
	rt := sup.Runtime()
	players := rt.Players
	if players == nil {
		players = []string{}
	}
	return &pb.ServerStatusResponse{
		ServerId: sup.Definition().ServerID, State: string(sup.State()),
		Cpu: rt.CPU, Mem: int64(rt.Mem), Online: int32(rt.Online), Max: int32(rt.Max),
		Players: players, Version: rt.Version, Desc: rt.Desc,
		WorldSize: int64(rt.WorldSize), EulaPending: rt.EULAPending, PingMs: rt.PingMS,
	}
}

// SendStdin forwards a console command.
func (s *Service) SendStdin(ctx context.Context, req *pb.StdinRequest) (*pb.Ack, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}
	sup, err := s.local("", req.ServerId)
	if err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}
	if err := sup.SendCommand(req.Command, 0); err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}
	return &pb.Ack{Ok: true}, nil
}

// ReadTerminal returns buffered console lines.
func (s *Service) ReadTerminal(ctx context.Context, req *pb.TerminalRequest) (*pb.TerminalChunk, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return nil, err
	}
	sup, err := s.local("", req.ServerId)
	if err != nil {
		return nil, err
	}
	return &pb.TerminalChunk{Lines: sup.Terminal().Tail(int(req.Lines))}, nil
}

// StreamEvents ingests the agent event firehose into the panel bus.
func (s *Service) StreamEvents(stream pb.NodeService_StreamEventsServer) error {
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&pb.Ack{Ok: true})
		}
		if err != nil {
			return err
		}
		if s.role == "panel" {
			if err := s.authorize(stream.Context(), ev.NodeId); err != nil {
				return err
			}
			s.nodes.Touch(ev.NodeId, "")
			var payload interface{}
			if ev.Payload != "" {
				_ = json.Unmarshal([]byte(ev.Payload), &payload)
			}
			if s.deps.Bus != nil {
				s.deps.Bus.Publish(events.Event{Type: ev.Type, ServerID: ev.ServerId, Data: payload})
			}
			// lifecycle events also refresh the cached state
			if state, ok := serverStateForEvent(ev.Type); ok {
				s.nodes.UpdateServerStatus(ev.NodeId, ev.ServerId, state)
			}
		}
	}
}

func serverStateForEvent(typ string) (string, bool) {
	switch typ {
	case events.EvServerStart:
		return "running", true
	case events.EvServerStop:
		return "stopped", true
	case events.EvServerKill:
		return "stopped", true
	case events.EvServerCrash:
		return "crashed", true
	case events.EvSendStartReload:
		return "starting", true
	default:
		return "", false
	}
}

// UpdateServerConfig applies a settings patch on this node and returns the
// stored definition.
func (s *Service) UpdateServerConfig(ctx context.Context, req *pb.NodeRequest) (*pb.NodeReply, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	if s.deps.Registry == nil {
		return &pb.NodeReply{Ok: false, Message: "no registry on this node"}, nil
	}
	var patch map[string]interface{}
	if err := json.Unmarshal([]byte(req.Payload), &patch); err != nil {
		return &pb.NodeReply{Ok: false, Message: "invalid patch: " + err.Error()}, nil
	}
	def, err := s.deps.DB.GetServer(req.ServerId)
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: "server not found: " + err.Error()}, nil
	}
	// Validate on a copy first: a rejected launch parameter (bad password
	// length, unknown option, unusable memory value) must not be
	// half-persisted on the node.
	if err := servers.ApplyServerPatch(&def, patch); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	if err := s.deps.Registry.UpdateConfig(req.ServerId, func(current *gen.Server) {
		*current = def
	}); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	raw, _ := json.Marshal(def)
	return &pb.NodeReply{Ok: true, Payload: string(raw)}, nil
}

// ServerHistory returns stored stat points for a server.
func (s *Service) ServerHistory(ctx context.Context, req *pb.HistoryRequest) (*pb.NodeReply, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	deps := servers.Deps{DB: s.deps.DB, Log: s.deps.Log, Bus: s.deps.Bus}
	points := servers.History(deps, req.ServerId, int(req.Hours))
	raw, _ := json.Marshal(points)
	return &pb.NodeReply{Ok: true, Payload: string(raw)}, nil
}

// AgreeEula accepts the EULA of a Minecraft server.
func (s *Service) AgreeEula(ctx context.Context, req *pb.ServerRequest) (*pb.NodeReply, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	if s.deps.Registry == nil {
		return &pb.NodeReply{Ok: false, Message: "no registry on this node"}, nil
	}
	if err := s.deps.Registry.AgreeEULA(req.ServerId, 0); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	return &pb.NodeReply{Ok: true}, nil
}

// UpdateExecutable re-downloads/updates the server executable.
func (s *Service) UpdateExecutable(ctx context.Context, req *pb.ServerRequest) (*pb.NodeReply, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	if s.deps.Registry == nil {
		return &pb.NodeReply{Ok: false, Message: "no registry on this node"}, nil
	}
	updater := servers.NewUpdateManager(s.serverDeps(), s.deps.Registry)
	runBackup := func(serverID string) error {
		if s.deps.Backups == nil {
			return nil
		}
		if b, err := s.deps.Backups.DefaultBackup(serverID); err == nil {
			return s.deps.Backups.Run(b.BackupID, 0)
		}
		return nil
	}
	if err := updater.Update(req.ServerId, 0, runBackup); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	return &pb.NodeReply{Ok: true}, nil
}

// CloneServer duplicates a server on this node.
func (s *Service) CloneServer(ctx context.Context, req *pb.CloneServerRequest) (*pb.NodeReply, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	if s.deps.Registry == nil {
		return &pb.NodeReply{Ok: false, Message: "no registry on this node"}, nil
	}
	if s.deps.Creation == nil {
		return &pb.NodeReply{Ok: false, Message: "this node cannot create servers"}, nil
	}
	src, err := s.deps.Registry.Get(req.ServerId)
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	def := src.Definition()
	name := req.Name
	if name == "" {
		name = def.ServerName + "-clone"
	}
	res, err := s.deps.Creation.Create(creation.Request{
		CreateType: creation.CreateRawExec, Name: name, ServerType: def.Type,
		Executable: def.Executable, ExecutionCommand: def.ExecutionCommand,
		StopCommand: def.StopCommand, Port: int(def.ServerPort),
	}, 0)
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	if dst, err := s.deps.Registry.Get(res.ServerID); err == nil && s.deps.Files != nil {
		_ = s.deps.Files.Copy(files.NewGuardOrPanic(def.Path), ".", dst.Definition().Path)
	}
	cloned, err := s.deps.DB.GetServer(res.ServerID)
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	raw, _ := json.Marshal(cloned)
	return &pb.NodeReply{Ok: true, Payload: string(raw)}, nil
}

/* ---------------------------------- files --------------------------------- */

func (s *Service) guardFor(serverID string) (*files.Guard, error) {
	sup, err := s.local("", serverID)
	if err != nil {
		return nil, err
	}
	return files.NewGuard(sup.Definition().Path)
}

// ListFiles lists a directory inside a server.
func (s *Service) ListFiles(ctx context.Context, req *pb.FileRequest) (*pb.DirListing, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return nil, err
	}
	guard, err := s.guardFor(req.ServerId)
	if err != nil {
		return nil, err
	}
	entries, err := filesList(guard, req.Path)
	if err != nil {
		return nil, err
	}
	out := &pb.DirListing{Entries: []*pb.DirEntry{}}
	for _, e := range entries {
		out.Entries = append(out.Entries, &pb.DirEntry{
			Name: e.Name, Size: e.Size, Dir: e.Dir, Modified: e.Modified.Format(time.RFC3339),
		})
	}
	return out, nil
}

// ReadFile streams a file (used by the editor, downloads and log views).
func (s *Service) ReadFile(req *pb.FileStreamRequest, stream pb.NodeService_ReadFileServer) error {
	if err := s.authorize(stream.Context(), ""); err != nil {
		return err
	}
	guard, err := s.guardFor(req.ServerId)
	if err != nil {
		return err
	}
	limit := req.MaxBytes
	if limit <= 0 {
		limit = 64 << 20
	}
	full, err := guard.Validate(req.Path)
	if err != nil {
		return err
	}
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 256<<10)
	var offset int64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if offset+int64(n) > limit {
				n = int(limit - offset)
			}
			if n > 0 {
				if err := stream.Send(&pb.FileChunk{
					ServerId: req.ServerId, Path: req.Path, Content: buf[:n], Offset: offset,
				}); err != nil {
					return err
				}
				offset += int64(n)
			}
			if offset >= limit {
				break
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return stream.Send(&pb.FileChunk{ServerId: req.ServerId, Path: req.Path, Offset: offset, Eof: true})
}

// WriteFile receives a streamed file write.
func (s *Service) WriteFile(stream pb.NodeService_WriteFileServer) error {
	var (
		guard *files.Guard
		def   gen.Server
		rel   string
		f     *os.File
	)
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&pb.Ack{Ok: true})
		}
		if err != nil {
			return err
		}
		if f == nil {
			if err := s.authorize(stream.Context(), ""); err != nil {
				return err
			}
			g, err := s.guardFor(chunk.ServerId)
			if err != nil {
				return err
			}
			guard = g
			sup, err := s.local("", chunk.ServerId)
			if err != nil {
				return err
			}
			def = sup.Definition()
			rel = chunk.Path
			full, err := guard.Validate(rel)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			f, err = os.Create(full)
			if err != nil {
				return err
			}
		}
		if len(chunk.Content) > 0 {
			if _, err := f.Write(chunk.Content); err != nil {
				return err
			}
		}
		_ = def
		_ = guard
	}
}

// FileOp covers mkdir/create/delete/move/copy/zip/unzip/stat.
func (s *Service) FileOp(ctx context.Context, req *pb.FileOpRequest) (*pb.NodeReply, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	guard, err := s.guardFor(req.ServerId)
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	mgr := files.New()
	switch req.Op {
	case "mkdir":
		err = mgr.CreateDir(guard, req.Path)
	case "create":
		err = mgr.CreateFile(guard, req.Path)
	case "delete":
		err = mgr.Delete(guard, req.Path)
	case "move":
		err = mgr.Move(guard, req.Path, req.Dst)
	case "copy":
		err = mgr.Copy(guard, req.Path, req.Dst)
	case "zip":
		out := "archive.zip"
		if req.Dst != "" {
			out = req.Dst
		}
		err = mgr.Zip(guard, req.Paths, out)
	case "unzip":
		dest := "."
		if req.Dst != "" {
			dest = req.Dst
		}
		err = mgr.Unzip(guard, req.Path, dest)
	case "stat":
		var entry files.Entry
		entry, err = mgr.Stat(guard, req.Path)
		if err == nil {
			raw, _ := json.Marshal(map[string]interface{}{
				"name": entry.Name, "size": entry.Size, "dir": entry.Dir,
				"modified": entry.Modified.Format(time.RFC3339),
			})
			return &pb.NodeReply{Ok: true, Payload: string(raw)}, nil
		}
	default:
		return &pb.NodeReply{Ok: false, Message: "unknown file op " + req.Op}, nil
	}
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	return &pb.NodeReply{Ok: true}, nil
}

/* --------------------------------- backups -------------------------------- */

// ensureBackupRow stores the panel's backup metadata on the node so the local
// backup engine can run against it.
// backupDirFor resolves where this node stores the backups of a server.
func (s *Service) backupDirFor(serverID string) (string, error) {
	if serverID == "" {
		return "", errors.New("server id required")
	}
	def, err := s.deps.DB.GetServer(serverID)
	if err != nil {
		return "", errors.New("unknown server on this node")
	}
	root := s.deps.BackupsRoot
	if root == "" {
		root = filepath.Join(s.deps.DataDir, "backups")
	}
	return filepath.Join(root, servers.SanitizeServerName(def.ServerName)), nil
}

// ensureBackupRow stores the panel's config with a node-local location so the
// node can run it (archives always live next to their server) and returns the
// storage plan the panel shipped with it.
func (s *Service) ensureBackupRow(payload, serverID string) (gen.Backup, backups.StoragePlan, error) {
	var plan backups.StoragePlan
	var row gen.Backup
	if payload != "" {
		bundle, err := backups.DecodeBundle(payload)
		if err != nil {
			return row, plan, err
		}
		row = bundle.Backup
		plan = bundle.Plan
	}
	if s.deps.Backups == nil {
		return row, plan, errors.New("no backup service on this node")
	}
	if row.ServerID == "" {
		row.ServerID = serverID
	}
	if row.BackupID == "" {
		return row, plan, errors.New("backup id required")
	}
	dir, err := s.backupDirFor(row.ServerID)
	if err != nil {
		return row, plan, err
	}
	row, err = s.deps.Backups.EnsureConfig(row, dir)
	return row, plan, err
}

// BackupRun executes a backup on this node.
func (s *Service) BackupRun(ctx context.Context, req *pb.BackupRunRequest) (*pb.NodeReply, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	row, plan, err := s.ensureBackupRow(req.Payload, req.ServerId)
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	if err := s.deps.Backups.RunWithPlan(row.BackupID, 0, plan); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	updated, err := s.deps.DB.GetBackup(row.BackupID)
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	raw, _ := json.Marshal(updated)
	return &pb.NodeReply{Ok: true, Payload: string(raw)}, nil
}

// BackupRestore restores an archive (or the newest one) of a backup.
func (s *Service) BackupRestore(ctx context.Context, req *pb.BackupRef) (*pb.NodeReply, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	if s.deps.Backups == nil {
		return &pb.NodeReply{Ok: false, Message: "no backup service on this node"}, nil
	}
	row, plan, err := s.ensureBackupRow(req.Payload, req.ServerId)
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	dir, err := s.backupDirFor(row.ServerID)
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	target := filepath.Join(dir, filepath.Base(req.Archive))
	if !strings.HasPrefix(target, dir) {
		return &pb.NodeReply{Ok: false, Message: "archive path escapes the backup directory"}, nil
	}
	// The local cache may have rotated the archive away: pull it back from the
	// storage destination before restoring.
	if _, err := os.Stat(target); err != nil && plan.Active() {
		fetched, ferr := s.deps.Backups.FetchRemoteArchive(plan, row.ServerID, filepath.Base(req.Archive), dir)
		if ferr != nil {
			return &pb.NodeReply{Ok: false, Message: ferr.Error()}, nil
		}
		target = fetched
	}
	if err := s.deps.Backups.Restore(req.BackupId, target, 0); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	return &pb.NodeReply{Ok: true}, nil
}

// BackupDelete removes a backup on this node.
func (s *Service) BackupDelete(ctx context.Context, req *pb.BackupRef) (*pb.NodeReply, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	if s.deps.Backups == nil {
		return &pb.NodeReply{Ok: false, Message: "no backup service on this node"}, nil
	}
	_, plan, err := s.ensureBackupRow(req.Payload, req.ServerId)
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	if err := s.deps.Backups.DeleteWithPlan(req.BackupId, req.RemoveFiles, plan); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	return &pb.NodeReply{Ok: true}, nil
}

// BackupFiles lists the archives stored for a backup.
func (s *Service) BackupFiles(ctx context.Context, req *pb.BackupRef) (*pb.NodeReply, error) {
	if err := s.authorize(ctx, ""); err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	if s.deps.Backups == nil {
		return &pb.NodeReply{Ok: false, Message: "no backup service on this node"}, nil
	}
	dir, err := s.backupDirFor(req.ServerId)
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	if req.Archive != "" {
		path := filepath.Join(dir, filepath.Base(req.Archive))
		if _, serr := os.Stat(path); serr != nil {
			if bundle, berr := backups.DecodeBundle(req.Payload); berr == nil && bundle.Plan.Active() {
				if fetched, ferr := s.deps.Backups.FetchRemoteArchive(bundle.Plan, req.ServerId, filepath.Base(req.Archive), dir); ferr == nil {
					path = fetched
				}
			}
		}
		names, err := s.deps.Backups.ListArchiveFiles(path)
		if err != nil {
			return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
		}
		raw, _ := json.Marshal(names)
		return &pb.NodeReply{Ok: true, Payload: string(raw)}, nil
	}
	bundle, _ := backups.DecodeBundle(req.Payload)
	row := bundle.Backup
	if row.BackupID == "" {
		row.BackupID = req.BackupId
	}
	row.ServerID = req.ServerId
	row.BackupLocation = dir
	archives, err := s.deps.Backups.ArchivesWithPlan(row, bundle.Plan)
	if err != nil {
		return &pb.NodeReply{Ok: false, Message: err.Error()}, nil
	}
	raw, _ := json.Marshal(archives)
	return &pb.NodeReply{Ok: true, Payload: string(raw)}, nil
}

// BackupRead streams an archive file from the node.
func (s *Service) BackupRead(req *pb.BackupReadRequest, stream pb.NodeService_BackupReadServer) error {
	if err := s.authorize(stream.Context(), ""); err != nil {
		return err
	}
	dir, err := s.backupDirFor(req.ServerId)
	if err != nil {
		return err
	}
	full := filepath.Join(dir, filepath.Clean("/"+req.Path))
	if _, serr := os.Stat(full); serr != nil && s.deps.Backups != nil {
		if bundle, berr := backups.DecodeBundle(req.Payload); berr == nil && bundle.Plan.Active() {
			if fetched, ferr := s.deps.Backups.FetchRemoteArchive(bundle.Plan, req.ServerId, filepath.Base(req.Path), dir); ferr == nil {
				full = fetched
			}
		}
	}
	rel, err := filepath.Rel(dir, full)
	if err != nil || len(rel) > 1 && rel[0] == '.' {
		return errors.New("path escapes the backup directory")
	}
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 256<<10)
	var offset int64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if err := stream.Send(&pb.FileChunk{Path: req.Path, Content: buf[:n], Offset: offset}); err != nil {
				return err
			}
			offset += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return stream.Send(&pb.FileChunk{Path: req.Path, Offset: offset, Eof: true})
}

/* -------------------------- wizard archive transfer ----------------------- */

// UploadArchive receives an import archive and spools it on this node.
func (s *Service) UploadArchive(stream pb.NodeService_UploadArchiveServer) error {
	var (
		f    *os.File
		name string
		path string
		id   string
	)
	cleanup := func() {
		if f != nil {
			f.Close()
		}
	}
	defer cleanup()
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			if f == nil {
				return stream.SendAndClose(&pb.NodeReply{Ok: false, Message: "empty transfer"})
			}
			f.Close()
			f = nil
			return stream.SendAndClose(&pb.NodeReply{Ok: true, Payload: path, Message: name})
		}
		if err != nil {
			if path != "" {
				os.Remove(path)
			}
			return err
		}
		if f == nil {
			if err := s.authorize(stream.Context(), ""); err != nil {
				return err
			}
			id = chunk.TransferId
			name = filepath.Base(chunk.Name)
			if name == "" || name == "." || name == "/" {
				name = "upload.zip"
			}
			dir := s.deps.ImportsDir
			if dir == "" {
				dir = filepath.Join(s.deps.DataDir, "import")
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			path = filepath.Join(dir, id+"-"+name)
			f, err = os.Create(path)
			if err != nil {
				return err
			}
		}
		if len(chunk.Content) > 0 {
			if _, err := f.Write(chunk.Content); err != nil {
				return err
			}
		}
	}
}

// serverDeps builds the servers subsystem dependencies of this node.
func (s *Service) serverDeps() servers.Deps {
	return servers.Deps{DB: s.deps.DB, Log: s.deps.Log, Bus: s.deps.Bus}
}

/* --------------------------------- helpers -------------------------------- */

func sanitizeNodeName(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out = append(out, r)
		case r == ' ':
			out = append(out, '-')
		}
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return string(out)
}

// deriveNodeAddress picks the address the panel should dial. An explicit
// advertise value wins; otherwise the peer IP plus the reported port is used,
// which is what the panel can actually reach.
func deriveNodeAddress(advertise, peer string, port int32) string {
	if advertise != "" {
		return advertise
	}
	if port <= 0 {
		port = 50052
	}
	if peer == "" {
		peer = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", peer, port)
}

// peerIP extracts the caller IP from the gRPC peer info.
func peerIP(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return p.Addr.String()
	}
	return host
}
