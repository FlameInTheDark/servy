package cluster

import (
        "encoding/json"
        "fmt"
        "net"
        "sync"
        "time"

        "google.golang.org/grpc"

        pb "github.com/FlameInTheDark/servy/internal/cluster/pb"
        "github.com/FlameInTheDark/servy/internal/database"
        "github.com/FlameInTheDark/servy/internal/dockerx"
        "github.com/FlameInTheDark/servy/internal/events"
        "github.com/FlameInTheDark/servy/internal/logging"
        "github.com/FlameInTheDark/servy/internal/ports"
        "github.com/FlameInTheDark/servy/internal/servers"
        "github.com/FlameInTheDark/servy/internal/version"
)

// AgentDeps bundles what an agent needs (headless: no HTTP UI).
type AgentDeps struct {
        DB        *database.Queries
        Log       *logging.Manager
        Bus       *events.Bus
        Registry  *servers.Registry
        HostStats *servers.HostStats
        PanelAddr string
        Token     string
        NodeName  string
        GRPCPort  int
}

// Agent runs the headless node: local gRPC server + upstream link to the panel.
type Agent struct {
        deps   AgentDeps
        svc    *Service
        client *Client
        grpc   *grpc.Server
        stop   chan struct{}
}

// NewAgent builds the agent.
func NewAgent(deps AgentDeps) *Agent {
        sd := servers.Deps{DB: deps.DB, Bus: deps.Bus, Log: deps.Log, Cfg: nil,
                Docker: dockerMgrForAgent(), Ports: ports.NewAllocator()}
        _ = sd
        return &Agent{deps: deps, stop: make(chan struct{})}
}

// Run starts the gRPC server and the upstream keepalive loop (blocking).
func (a *Agent) Run() error {
        nodeID := EnsureNodeID(a.deps.DB)
        NodeID = nodeID

        // gRPC server (panel connects to us for proxied ops)
        a.svc = NewService(servers.Deps{DB: a.deps.DB, Bus: a.deps.Bus, Log: a.deps.Log,
                Docker: dockerMgrForAgent(), Ports: ports.NewAllocator()}, a.deps.Registry, a.deps.Token, a.deps.Log, a.deps.Bus)
        lis, err := net.Listen("tcp", fmt.Sprintf(":%d", a.deps.GRPCPort))
        if err != nil {
                return err
        }
        a.grpc = grpc.NewServer()
        pb.RegisterNodeServiceServer(a.grpc, a.svc)
        go func() {
                if err := a.grpc.Serve(lis); err != nil {
                        a.deps.Log.Get("server").Error("agent grpc server: %v", err)
                }
        }()
        a.deps.Log.Get("server").Info("agent gRPC listening on :%d (node %s)", a.deps.GRPCPort, nodeID)

        // upstream: register + heartbeats + event stream
        a.client = NewClient(a.deps.Token)
        go a.upstreamLoop(nodeID)
        <-a.stop
        a.grpc.Stop()
        return nil
}

// upstreamLoop maintains hello + heartbeats + event stream to the panel.
func (a *Agent) upstreamLoop(nodeID string) {
        addr := a.deps.PanelAddr
        caps := []string{"servers", "files", "terminal", "backups-local"}
        for {
                err := a.client.Hello(addr, nodeID, a.deps.NodeName, version.Version, "agent", caps)
                if err != nil {
                        a.deps.Log.Get("server").Warning("agent: panel hello failed (%v); retrying in 15s", err)
                        time.Sleep(15 * time.Second)
                        continue
                }
                a.deps.Log.Get("server").Info("agent: registered with panel %s", addr)
                break
        }
        // mark node row locally too (so the agent's own UI-less state knows itself)
        now := database.Now()
        a.deps.DB.UpsertNode(nodeID, a.deps.NodeName, addr, "agent", "online", &now, &version.Version, nil, nil, now)

        // event pump: forward local bus events to the panel via StreamEvents
        go func() {
                ch, cancel := a.deps.Bus.Subscribe("*")
                defer cancel()
                for {
                        select {
                        case <-a.stop:
                                return
                        case ev, ok := <-ch:
                                if !ok {
                                        return
                                }
                                if cl, err := a.client.connFor(addr); err == nil {
                                        stream, err := cl.StreamEvents(a.client.ctx())
                                        if err == nil {
                                                payload, _ := json.Marshal(ev.Data)
                                                stream.Send(&pb.NodeEvent{
                                                        NodeId: nodeID, ServerId: ev.ServerID, Type: ev.Type,
                                                        Payload: string(payload), Timestamp: time.Now().Unix(),
                                                })
                                                stream.CloseSend()
                                        }
                                }
                        }
                }
        }()

        tick := time.NewTicker(10 * time.Second)
        defer tick.Stop()
        for {
                select {
                case <-a.stop:
                        return
                case <-tick.C:
                        running := 0
                        for _, sup := range a.deps.Registry.All() {
                                if sup.State() == "running" {
                                        running++
                                }
                        }
                        if err := a.client.Heartbeat(addr, nodeID, nil, version.Version, running); err != nil {
                                a.deps.Log.Get("server").Warning("agent: heartbeat failed: %v", err)
                        } else {
                                now := database.Now()
                                a.deps.DB.UpdateNodeStatus("online", &now, &version.Version, nodeID)
                        }
                }
        }
}

// EnsureNodeID loads or creates this node's identity row.
func EnsureNodeID(db *database.Queries) string {
        if n, err := db.GetNodeByName("self"); err == nil {
                return n.NodeID
        }
        id := "node-" + database.Now()[:10] + "-" + randomSuffix()
        db.UpsertNode(id, "self", "", "agent", "online", nil, nil, nil, nil, database.Now())
        return id
}

func randomSuffix() string {
        b := make([]byte, 4)
        for i := range b {
                b[i] = "0123456789abcdef"[time.Now().UnixNano()>>uint((i+1)*7)&0xf]
        }
        return string(b)
}

// dockerMgrForAgent resolves the docker execution backend for agent mode
// (nil-safe: agents without a daemon run servers as host processes).
var agentDockerOnce sync.Once
var agentDocker *dockerx.Manager

func dockerMgrForAgent() *dockerx.Manager {
        agentDockerOnce.Do(func() {
                m, err := dockerx.New()
                if err == nil {
                        agentDocker = m
                }
        })
        return agentDocker
}
