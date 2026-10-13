// Package cluster implements multi-node support: the gRPC NodeService server
// (served by panel AND agents), the panel-side client router for remote
// servers, and the headless agent runtime. Node auth uses a shared cluster
// token (mTLS-ready transport).
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "github.com/FlameInTheDark/servy/internal/cluster/pb"
	"github.com/FlameInTheDark/servy/internal/database"
	"github.com/FlameInTheDark/servy/internal/events"
	"github.com/FlameInTheDark/servy/internal/logging"
	"github.com/FlameInTheDark/servy/internal/models"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
	"github.com/FlameInTheDark/servy/internal/servers"
)

// NodeID is this node's identity.
var NodeID = servers.LocalNodeID

// ---------------- gRPC server (both roles) ----------------

// Service implements pb.NodeService over the local registry.
type Service struct {
	pb.UnimplementedNodeServiceServer
	deps  servers.Deps
	reg   *servers.Registry
	token string
	log   *logging.Manager
	bus   *events.Bus
}

// NewService builds the gRPC service.
func NewService(deps servers.Deps, reg *servers.Registry, token string, log *logging.Manager, bus *events.Bus) *Service {
	return &Service{deps: deps, reg: reg, token: token, log: log, bus: bus}
}

func (s *Service) authorized(ctx context.Context) bool {
	if s.token == "" {
		return true
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}
	for _, v := range md.Get("authorization") {
		if v == "Bearer "+s.token {
			return true
		}
	}
	return false
}

// Hello registers a node.
func (s *Service) Hello(ctx context.Context, req *pb.HelloRequest) (*pb.HelloResponse, error) {
	if !s.authorized(ctx) {
		return &pb.HelloResponse{Accepted: false, Message: "invalid cluster token"}, nil
	}
	// upsert node row (agents register with the panel)
	if req.NodeId != "" && req.Role == "agent" {
		now := database.Now()
		nowP, verP, capP := now, req.Version, marshalCapabilities(req.Capabilities)
		empty := ""
		s.deps.DB.UpsertNode(req.NodeId, req.Name, "agent:"+req.NodeId, req.Role, "online", &nowP, &verP, &capP, &empty, now)
	}
	s.log.Get("server").Info("cluster: node %q (%s v%s) said hello", req.Name, req.NodeId, req.Version)
	return &pb.HelloResponse{Accepted: true, Message: "welcome"}, nil
}

// Heartbeat updates node telemetry.
func (s *Service) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	if !s.authorized(ctx) {
		return &pb.HeartbeatResponse{Ok: false, Message: "invalid cluster token"}, nil
	}
	now := database.Now()
	verP := req.Version
	s.deps.DB.UpdateNodeStatus("online", &now, &verP, req.NodeId)
	return &pb.HeartbeatResponse{Ok: true}, nil
}

// CreateServer creates a server from a JSON definition (agent side).
func (s *Service) CreateServer(ctx context.Context, req *pb.CreateServerRequest) (*pb.ServerActionResponse, error) {
	if !s.authorized(ctx) {
		return &pb.ServerActionResponse{Ok: false, Message: "invalid cluster token"}, nil
	}
	var def gen.Server
	if err := json.Unmarshal([]byte(req.Definition), &def); err != nil {
		return &pb.ServerActionResponse{Ok: false, Message: err.Error()}, nil
	}
	def.ServerID = req.ServerId
	local := servers.LocalNodeID
	def.NodeID = &local
	if _, err := s.reg.Create(def); err != nil {
		return &pb.ServerActionResponse{Ok: false, Message: err.Error()}, nil
	}
	return &pb.ServerActionResponse{Ok: true}, nil
}

// DeleteServer removes a server locally.
func (s *Service) DeleteServer(ctx context.Context, req *pb.DeleteServerRequest) (*pb.ServerActionResponse, error) {
	if !s.authorized(ctx) {
		return &pb.ServerActionResponse{Ok: false, Message: "invalid cluster token"}, nil
	}
	if err := s.reg.Delete(req.ServerId, req.RemoveFiles, 0); err != nil {
		return &pb.ServerActionResponse{Ok: false, Message: err.Error()}, nil
	}
	return &pb.ServerActionResponse{Ok: true}, nil
}

// StartServer starts a local server.
func (s *Service) StartServer(ctx context.Context, req *pb.ServerRequest) (*pb.ServerActionResponse, error) {
	if !s.authorized(ctx) {
		return &pb.ServerActionResponse{Ok: false, Message: "invalid cluster token"}, nil
	}
	sup, err := s.reg.Get(req.ServerId)
	if err != nil {
		return &pb.ServerActionResponse{Ok: false, Message: err.Error()}, nil
	}
	if err := sup.Start(0); err != nil {
		return &pb.ServerActionResponse{Ok: false, Message: err.Error()}, nil
	}
	return &pb.ServerActionResponse{Ok: true}, nil
}

// StopServer stops a local server.
func (s *Service) StopServer(ctx context.Context, req *pb.ServerRequest) (*pb.ServerActionResponse, error) {
	if !s.authorized(ctx) {
		return &pb.ServerActionResponse{Ok: false, Message: "invalid cluster token"}, nil
	}
	sup, err := s.reg.Get(req.ServerId)
	if err != nil {
		return &pb.ServerActionResponse{Ok: false, Message: err.Error()}, nil
	}
	if err := sup.Stop("cluster", 0); err != nil {
		return &pb.ServerActionResponse{Ok: false, Message: err.Error()}, nil
	}
	return &pb.ServerActionResponse{Ok: true}, nil
}

// RestartServer restarts a local server.
func (s *Service) RestartServer(ctx context.Context, req *pb.ServerRequest) (*pb.ServerActionResponse, error) {
	if !s.authorized(ctx) {
		return &pb.ServerActionResponse{Ok: false, Message: "invalid cluster token"}, nil
	}
	sup, err := s.reg.Get(req.ServerId)
	if err != nil {
		return &pb.ServerActionResponse{Ok: false, Message: err.Error()}, nil
	}
	if err := sup.Restart(0, nil); err != nil {
		return &pb.ServerActionResponse{Ok: false, Message: err.Error()}, nil
	}
	return &pb.ServerActionResponse{Ok: true}, nil
}

// KillServer force-kills a local server.
func (s *Service) KillServer(ctx context.Context, req *pb.ServerRequest) (*pb.ServerActionResponse, error) {
	if !s.authorized(ctx) {
		return &pb.ServerActionResponse{Ok: false, Message: "invalid cluster token"}, nil
	}
	sup, err := s.reg.Get(req.ServerId)
	if err != nil {
		return &pb.ServerActionResponse{Ok: false, Message: err.Error()}, nil
	}
	if err := sup.Kill(0); err != nil {
		return &pb.ServerActionResponse{Ok: false, Message: err.Error()}, nil
	}
	return &pb.ServerActionResponse{Ok: true}, nil
}

// ServerStatus returns one server's live status.
func (s *Service) ServerStatus(ctx context.Context, req *pb.ServerRequest) (*pb.ServerStatusResponse, error) {
	sup, err := s.reg.Get(req.ServerId)
	if err != nil {
		return nil, err
	}
	rt := sup.Runtime()
	players := rt.Players
	if players == nil {
		players = []string{}
	}
	return &pb.ServerStatusResponse{
		ServerId: req.ServerId, State: string(sup.State()),
		Cpu: rt.CPU, Mem: int64(rt.Mem), Online: int32(rt.Online), Max: int32(rt.Max),
		Players: players, Version: rt.Version,
	}, nil
}

// ListServers lists local servers with status.
func (s *Service) ListServers(ctx context.Context, _ *pb.ListServersRequest) (*pb.ListServersResponse, error) {
	out := &pb.ListServersResponse{Servers: []*pb.ServerStatusResponse{}}
	for _, sup := range s.reg.All() {
		rt := sup.Runtime()
		players := rt.Players
		if players == nil {
			players = []string{}
		}
		out.Servers = append(out.Servers, &pb.ServerStatusResponse{
			ServerId: sup.Definition().ServerID, State: string(sup.State()),
			Cpu: rt.CPU, Mem: int64(rt.Mem), Online: int32(rt.Online), Max: int32(rt.Max),
			Players: players, Version: rt.Version,
		})
	}
	return out, nil
}

// SendStdin forwards a command.
func (s *Service) SendStdin(ctx context.Context, req *pb.StdinRequest) (*pb.Ack, error) {
	if !s.authorized(ctx) {
		return &pb.Ack{Ok: false, Message: "invalid cluster token"}, nil
	}
	sup, err := s.reg.Get(req.ServerId)
	if err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}
	if err := sup.SendCommand(req.Command, 0); err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}
	return &pb.Ack{Ok: true}, nil
}

// ReadTerminal returns buffered terminal lines.
func (s *Service) ReadTerminal(ctx context.Context, req *pb.TerminalRequest) (*pb.TerminalChunk, error) {
	sup, err := s.reg.Get(req.ServerId)
	if err != nil {
		return nil, err
	}
	lines := sup.Terminal().Tail(int(req.Lines))
	return &pb.TerminalChunk{Lines: lines}, nil
}

// StreamEvents is the agent→panel event firehose.
func (s *Service) StreamEvents(stream pb.NodeService_StreamEventsServer) error {
	for {
		ev, err := stream.Recv()
		if err != nil {
			return err
		}
		s.bus.Publish(events.Event{
			Type: ev.Type, ServerID: ev.ServerId,
		})
	}
}

// FileRead reads a file from a server dir.
func (s *Service) FileRead(ctx context.Context, req *pb.FileRequest) (*pb.FileContent, error) {
	sup, err := s.reg.Get(req.ServerId)
	if err != nil {
		return nil, err
	}
	guard, _ := filesNewGuard(sup.Definition().Path)
	data, err := filesRead(guard, req.Path, 8<<20)
	if err != nil {
		return nil, err
	}
	return &pb.FileContent{Content: data}, nil
}

// FileWrite writes a file into a server dir.
func (s *Service) FileWrite(ctx context.Context, req *pb.FileWriteRequest) (*pb.Ack, error) {
	if !s.authorized(ctx) {
		return &pb.Ack{Ok: false, Message: "invalid cluster token"}, nil
	}
	sup, err := s.reg.Get(req.ServerId)
	if err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}
	guard, _ := filesNewGuard(sup.Definition().Path)
	if err := filesWrite(guard, req.Path, req.Content); err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}
	return &pb.Ack{Ok: true}, nil
}

// FileList lists a server dir.
func (s *Service) FileList(ctx context.Context, req *pb.FileRequest) (*pb.DirListing, error) {
	sup, err := s.reg.Get(req.ServerId)
	if err != nil {
		return nil, err
	}
	guard, _ := filesNewGuard(sup.Definition().Path)
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

func marshalCapabilities(caps []string) string {
	b, _ := json.Marshal(caps)
	return string(b)
}

// ---------------- panel-side client router ----------------

// Client connects to a remote node and proxies operations.
type Client struct {
	mu    sync.Mutex
	conns map[string]pb.NodeServiceClient
	addrs map[string]string
	token string
}

// NewClient builds the router.
func NewClient(token string) *Client {
	return &Client{conns: map[string]pb.NodeServiceClient{}, addrs: map[string]string{}, token: token}
}

func (c *Client) conn(addr string) (pb.NodeServiceClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl, ok := c.conns[addr]; ok {
		return cl, nil
	}
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	cl := pb.NewNodeServiceClient(cc)
	c.conns[addr] = cl
	c.addrs[addr] = addr
	return cl, nil
}

func (c *Client) ctx() context.Context {
	if c.token == "" {
		return context.Background()
	}
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+c.token)
}

// Start proxies a start call.
func (c *Client) Start(addr, serverID string) error {
	cl, err := c.conn(addr)
	if err != nil {
		return err
	}
	res, err := cl.StartServer(c.ctx(), &pb.ServerRequest{ServerId: serverID})
	if err != nil {
		return err
	}
	if !res.Ok {
		return fmt.Errorf("%s", res.Message)
	}
	return nil
}

// Stop proxies a stop call.
func (c *Client) Stop(addr, serverID string) error {
	cl, err := c.conn(addr)
	if err != nil {
		return err
	}
	res, err := cl.StopServer(c.ctx(), &pb.ServerRequest{ServerId: serverID})
	if err != nil {
		return err
	}
	if !res.Ok {
		return fmt.Errorf("%s", res.Message)
	}
	return nil
}

// Restart proxies a restart call.
func (c *Client) Restart(addr, serverID string) error {
	cl, err := c.conn(addr)
	if err != nil {
		return err
	}
	res, err := cl.RestartServer(c.ctx(), &pb.ServerRequest{ServerId: serverID})
	if err != nil {
		return err
	}
	if !res.Ok {
		return fmt.Errorf("%s", res.Message)
	}
	return nil
}

// Kill proxies a kill call.
func (c *Client) Kill(addr, serverID string) error {
	cl, err := c.conn(addr)
	if err != nil {
		return err
	}
	res, err := cl.KillServer(c.ctx(), &pb.ServerRequest{ServerId: serverID})
	if err != nil {
		return err
	}
	if !res.Ok {
		return fmt.Errorf("%s", res.Message)
	}
	return nil
}

// SendCommand proxies stdin.
func (c *Client) SendCommand(addr, serverID, command string) error {
	cl, err := c.conn(addr)
	if err != nil {
		return err
	}
	res, err := cl.SendStdin(c.ctx(), &pb.StdinRequest{ServerId: serverID, Command: command})
	if err != nil {
		return err
	}
	if !res.Ok {
		return fmt.Errorf("%s", res.Message)
	}
	return nil
}

// Status fetches a remote server status.
func (c *Client) Status(addr, serverID string) (*pb.ServerStatusResponse, error) {
	cl, err := c.conn(addr)
	if err != nil {
		return nil, err
	}
	return cl.ServerStatus(c.ctx(), &pb.ServerRequest{ServerId: serverID})
}

// Terminal reads remote terminal lines.
func (c *Client) Terminal(addr, serverID string, lines int) ([]string, error) {
	cl, err := c.conn(addr)
	if err != nil {
		return nil, err
	}
	res, err := cl.ReadTerminal(c.ctx(), &pb.TerminalRequest{ServerId: serverID, Lines: int32(lines)})
	if err != nil {
		return nil, err
	}
	return res.Lines, nil
}

// CreateServer provisions a server on a node.
func (c *Client) CreateServer(addr string, def gen.Server) error {
	defJSON, _ := json.Marshal(def)
	cl, err := c.conn(addr)
	if err != nil {
		return err
	}
	res, err := cl.CreateServer(c.ctx(), &pb.CreateServerRequest{ServerId: def.ServerID, Definition: string(defJSON)})
	if err != nil {
		return err
	}
	if !res.Ok {
		return fmt.Errorf("%s", res.Message)
	}
	return nil
}

// DeleteServer removes a server on a node.
func (c *Client) DeleteServer(addr, serverID string, removeFiles bool) error {
	cl, err := c.conn(addr)
	if err != nil {
		return err
	}
	res, err := cl.DeleteServer(c.ctx(), &pb.DeleteServerRequest{ServerId: serverID, RemoveFiles: removeFiles})
	if err != nil {
		return err
	}
	if !res.Ok {
		return fmt.Errorf("%s", res.Message)
	}
	return nil
}

// Hello registers this node with a peer.
func (c *Client) Hello(addr, nodeID, name, version, role string, caps []string) error {
	cl, err := c.conn(addr)
	if err != nil {
		return err
	}
	res, err := cl.Hello(c.ctx(), &pb.HelloRequest{NodeId: nodeID, Name: name, Version: version, Capabilities: caps, Role: role})
	if err != nil {
		return err
	}
	if !res.Accepted {
		return fmt.Errorf("rejected: %s", res.Message)
	}
	return nil
}

// Heartbeat sends telemetry to a peer.
func (c *Client) Heartbeat(addr, nodeID string, snap *models.ServerRuntime, version string, running int) error {
	cl, err := c.conn(addr)
	if err != nil {
		return err
	}
	_, err = cl.Heartbeat(c.ctx(), &pb.HeartbeatRequest{NodeId: nodeID, Version: version, RunningServers: int32(running)})
	return err
}

// connFor exposes the raw client for streaming (agent event pump).
func (c *Client) connFor(addr string) (pb.NodeServiceClient, error) {
	return c.conn(addr)
}
