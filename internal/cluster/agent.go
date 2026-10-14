package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "github.com/FlameInTheDark/servy/internal/cluster/pb"
	"github.com/FlameInTheDark/servy/internal/database"
	"github.com/FlameInTheDark/servy/internal/events"
	"github.com/FlameInTheDark/servy/internal/logging"
	"github.com/FlameInTheDark/servy/internal/version"
)

// AgentCredentials are what an agent needs to talk to its panel. They are
// written next to the config file with 0600 permissions after a successful join.
type AgentCredentials struct {
	NodeID    string `json:"node_id"`
	Token     string `json:"token"`
	PanelAddr string `json:"panel_addr"`
	Name      string `json:"name"`
	JoinedAt  string `json:"joined_at"`
}

// CredentialsPath returns the credentials file inside a data dir.
func CredentialsPath(dataDir string) string {
	return filepath.Join(dataDir, "config", "agent.json")
}

// LoadCredentials reads stored agent credentials (empty when not joined yet).
func LoadCredentials(dataDir string) (*AgentCredentials, error) {
	raw, err := os.ReadFile(CredentialsPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var creds AgentCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("parse %s: %w", CredentialsPath(dataDir), err)
	}
	if creds.NodeID == "" || creds.Token == "" {
		return nil, fmt.Errorf("%s is incomplete; re-join the node", CredentialsPath(dataDir))
	}
	return &creds, nil
}

// SaveCredentials persists agent credentials with restrictive permissions.
func SaveCredentials(dataDir string, creds *AgentCredentials) error {
	path := CredentialsPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// JoinOptions configures a join attempt.
type JoinOptions struct {
	PanelAddr string // host:port of the panel node API
	Ticket    string
	Name      string
	GRPCPort  int
	Advertise string // optional explicit dial address
	Version   string
}

// Join exchanges a ticket for node credentials. It does not need any local
// state, so it can run before a data dir exists (CLI bootstrap, containers).
func Join(opts JoinOptions) (*AgentCredentials, error) {
	hostname, _ := os.Hostname()
	addr := opts.PanelAddr
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connect to panel %s: %w", addr, err)
	}
	defer cc.Close()
	cl := pb.NewNodeServiceClient(cc)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req := &pb.JoinRequest{
		Ticket: opts.Ticket, Name: opts.Name, Version: opts.Version, Hostname: hostname,
		GrpcPort: int32(opts.GRPCPort), Advertise: opts.Advertise,
		Capabilities: []string{"servers", "files", "terminal", "backups", "creation"},
	}
	res, err := cl.Join(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("join failed: %w", err)
	}
	if !res.Accepted {
		return nil, fmt.Errorf("panel rejected the join: %s", res.Message)
	}
	name := opts.Name
	if name == "" {
		name = hostname
	}
	return &AgentCredentials{
		NodeID: res.NodeId, Token: res.Token, PanelAddr: addr, Name: name,
		JoinedAt: database.Now(),
	}, nil
}

// AgentDeps bundles what the headless agent needs.
type AgentDeps struct {
	DB   *database.Queries
	Log  *logging.Manager
	Bus  *events.Bus
	Deps Deps // registry, creation, backups, files, host stats …

	DataDir   string
	PanelAddr string
	Token     string
	NodeID    string
	NodeName  string
	GRPCPort  int
}

// Agent runs the headless node: local gRPC node API plus the upstream link.
type Agent struct {
	deps   AgentDeps
	svc    *Service
	grpc   *grpc.Server
	stop   chan struct{}
	once   sync.Once
	creds  *AgentCredentials
	cancel func()
}

// NewAgent builds the agent runtime.
func NewAgent(deps AgentDeps) *Agent {
	return &Agent{deps: deps, stop: make(chan struct{})}
}

// Run starts the node API and the upstream loops (blocking).
func (a *Agent) Run() error {
	log := a.deps.Log.Get("server")
	a.deps.Deps.DataDir = a.deps.DataDir
	a.deps.Deps.ImportsDir = filepath.Join(a.deps.DataDir, "import")
	a.deps.Deps.Version = version.Version

	// local node API (the panel calls into this)
	a.svc = NewAgentService(a.deps.Deps, a.deps.Token, a.deps.NodeName, a.deps.PanelAddr)
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", a.deps.GRPCPort))
	if err != nil {
		return fmt.Errorf("listen on node API port %d: %w", a.deps.GRPCPort, err)
	}
	a.grpc = grpc.NewServer()
	pb.RegisterNodeServiceServer(a.grpc, a.svc)
	go func() {
		if err := a.grpc.Serve(lis); err != nil {
			log.Error("node API stopped: %v", err)
		}
	}()
	log.Info("node API listening on :%d (node %s)", a.deps.GRPCPort, a.deps.NodeID)

	go a.upstreamLoop()
	<-a.stop
	a.grpc.GracefulStop()
	return nil
}

// Stop shuts the agent down.
func (a *Agent) Stop() {
	a.once.Do(func() { close(a.stop) })
}

// upstreamLoop keeps the panel registration, heartbeats and event stream alive.
func (a *Agent) upstreamLoop() {
	log := a.deps.Log.Get("server")
	backoff := time.Second

	// one long-lived event stream, reconnected on failure
	go a.eventLoop()

	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	report := func() error {
		cc, err := a.dial()
		if err != nil {
			return err
		}
		defer cc.Close()
		cl := pb.NewNodeServiceClient(cc)
		ctx, cancel := a.callCtx(15 * time.Second)
		defer cancel()

		// hello refreshes name/address/version after a restart or a move
		helloRes, err := cl.Hello(ctx, &pb.HelloRequest{
			NodeId: a.deps.NodeID, Name: a.deps.NodeName, Version: version.Version,
			Role: "agent", Capabilities: []string{"servers", "files", "terminal", "backups", "creation"},
		})
		if err != nil {
			return err
		}
		if !helloRes.Accepted {
			return fmt.Errorf("%s", helloRes.Message)
		}

		hb := a.heartbeat()
		res, err := cl.Heartbeat(ctx, hb)
		if err != nil {
			return err
		}
		if !res.Ok {
			return fmt.Errorf("%s", res.Message)
		}
		return nil
	}

	for {
		if err := report(); err != nil {
			log.Warning("agent: panel unreachable (%v); retrying", err)
			select {
			case <-a.stop:
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		if backoff != time.Second {
			log.Info("agent: reconnected to panel %s", a.deps.PanelAddr)
			backoff = time.Second
		}
		select {
		case <-a.stop:
			return
		case <-tick.C:
		}
	}
}

// eventLoop forwards local bus events to the panel over one stream.
func (a *Agent) eventLoop() {
	log := a.deps.Log.Get("server")
	ch, cancel := a.deps.Bus.Subscribe("*")
	defer cancel()
	backoff := time.Second
	for {
		select {
		case <-a.stop:
			return
		default:
		}
		cc, err := a.dial()
		if err != nil {
			select {
			case <-a.stop:
				return
			case <-time.After(backoff):
				continue
			}
		}
		cl := pb.NewNodeServiceClient(cc)
		ctx, cancelCall := context.WithCancel(context.Background())
		stream, err := cl.StreamEvents(ctx)
		if err != nil {
			cancelCall()
			cc.Close()
			select {
			case <-a.stop:
				return
			case <-time.After(backoff):
			}
			continue
		}
		log.Info("agent: event stream to panel established")
		backoff = time.Second
	send:
		for {
			select {
			case <-a.stop:
				cancelCall()
				cc.Close()
				return
			case ev, ok := <-ch:
				if !ok {
					break send
				}
				payload, _ := json.Marshal(ev.Data)
				err := stream.Send(&pb.NodeEvent{
					NodeId: a.deps.NodeID, ServerId: ev.ServerID, Type: ev.Type,
					Payload: string(payload), Timestamp: time.Now().Unix(),
				})
				if err != nil {
					break send
				}
			}
		}
		cancelCall()
		cc.Close()
		select {
		case <-a.stop:
			return
		case <-time.After(time.Second):
		}
	}
}

func (a *Agent) dial() (*grpc.ClientConn, error) {
	return grpc.NewClient(a.deps.PanelAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func (a *Agent) callCtx(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx := metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+a.deps.Token, "node-id", a.deps.NodeID)
	return context.WithTimeout(ctx, timeout)
}

// heartbeat collects this node's telemetry.
func (a *Agent) heartbeat() *pb.HeartbeatRequest {
	hb := &pb.HeartbeatRequest{NodeId: a.deps.NodeID, Version: version.Version}
	if a.deps.Deps.HostStats != nil {
		snap := a.deps.Deps.HostStats.Snapshot()
		hb.CpuUsage = snap.CPUUsage
		hb.MemPercent = snap.MemPercent
		hb.MemUsed = int64(snap.MemUsage)
		hb.MemTotal = int64(snap.MemTotal)
	}
	if a.deps.Deps.Registry != nil {
		all := a.deps.Deps.Registry.All()
		hb.TotalServers = int32(len(all))
		running := 0
		for _, sup := range all {
			if sup.State() == "running" {
				running++
			}
		}
		hb.RunningServers = int32(running)
	}
	return hb
}
