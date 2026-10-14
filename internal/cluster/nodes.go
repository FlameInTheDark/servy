package cluster

import (
	"sort"
	"sync"
	"time"

	pb "github.com/FlameInTheDark/servy/internal/cluster/pb"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

// OfflineAfter is how long a node may stay silent before the panel treats it as
// offline. Agents heartbeat every 10s, so this tolerates two lost heartbeats.
const OfflineAfter = 45 * time.Second

// NodeState is the panel-side live view of one node: identity from the database
// plus in-memory telemetry and the last known server statuses.
type NodeState struct {
	NodeID       string
	Name         string
	Address      string
	Role         string
	Version      string
	Status       string // online | offline | unreachable
	LastSeen     time.Time
	Created      time.Time
	Capabilities []string

	CPU      float64
	MemPct   float64
	MemUsed  int64
	MemTotal int64
	Running  int
	Total    int

	LastError string
	Servers   map[string]*pb.ServerStatusResponse
	serverIDs []string // stable ordering for views
}

// Online reports whether the node answered recently.
func (n *NodeState) Online() bool {
	return n.Status == "online" && time.Since(n.LastSeen) < OfflineAfter
}

// NodeRegistry keeps the panel's view of every node and the credentials agents
// authenticate with. Everything here is in-memory: the durable parts (name,
// address, token, last_seen) live in the database and are reloaded on boot.
type NodeRegistry struct {
	mu       sync.RWMutex
	nodes    map[string]*NodeState
	tokenIdx map[string]string // sha256(node token) -> node id
	tokens   map[string]string // node id -> node token (panel dials with it)
	now      func() time.Time

	// OnStatus is notified on online/offline transitions so the panel can
	// persist them (called outside the lock, never blocking the registry).
	OnStatus func(nodeID, status, version string, lastSeen time.Time)
}

// NewNodeRegistry builds an empty registry.
func NewNodeRegistry() *NodeRegistry {
	return &NodeRegistry{
		nodes:    map[string]*NodeState{},
		tokenIdx: map[string]string{},
		tokens:   map[string]string{},
		now:      time.Now,
	}
}

// Load seeds the registry from the database rows (tokens included).
func (r *NodeRegistry) Load(rows []gen.Node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range rows {
		if row.Role != "agent" {
			continue
		}
		st := &NodeState{
			NodeID: row.NodeID, Name: row.Name, Address: row.Address, Role: row.Role,
			Status: "offline", Servers: map[string]*pb.ServerStatusResponse{},
		}
		if row.Version != nil {
			st.Version = *row.Version
		}
		if row.LastSeen != nil {
			if ts, err := time.Parse(time.RFC3339, *row.LastSeen); err == nil {
				st.LastSeen = ts
			}
		}
		if ts, err := time.Parse(time.RFC3339, row.Created); err == nil {
			st.Created = ts
		}
		if row.ClusterTokenHash != nil && *row.ClusterTokenHash != "" {
			r.tokenIdx[hashToken(*row.ClusterTokenHash)] = row.NodeID
			r.tokens[row.NodeID] = *row.ClusterTokenHash
		}
		r.nodes[row.NodeID] = st
	}
}

// Register adds or replaces a node (join, credential rotation).
func (r *NodeRegistry) Register(nodeID, name, address, version, token string, caps []string) *NodeState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if prev, ok := r.tokens[nodeID]; ok && token != "" && prev != token {
		delete(r.tokenIdx, hashToken(prev))
	}
	st, ok := r.nodes[nodeID]
	if !ok {
		st = &NodeState{NodeID: nodeID, Servers: map[string]*pb.ServerStatusResponse{}}
		r.nodes[nodeID] = st
	}
	st.Name, st.Address, st.Role = name, address, "agent"
	st.Version, st.Capabilities = version, caps
	st.Status, st.LastSeen = "online", r.now()
	st.LastError = ""
	if token != "" {
		r.tokenIdx[hashToken(token)] = nodeID
		r.tokens[nodeID] = token
	}
	return st
}

// Token returns the credential the panel uses to dial a node (empty when the
// node predates the join flow).
func (r *NodeRegistry) Token(nodeID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tokens[nodeID]
}

// Forget removes a node (and its token) from the registry.
func (r *NodeRegistry) Forget(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, id := range r.tokenIdx {
		if id == nodeID {
			delete(r.tokenIdx, key)
		}
	}
	delete(r.tokens, nodeID)
	delete(r.nodes, nodeID)
}

// Authenticate resolves a bearer token to a node id (hashed lookup: the
// plaintext credential is only kept so the panel can dial the node).
func (r *NodeRegistry) Authenticate(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	nodeID, ok := r.tokenIdx[hashToken(token)]
	if !ok {
		return "", false
	}
	return nodeID, true
}

// Get returns a node state copy.
func (r *NodeRegistry) Get(nodeID string) (NodeState, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.nodes[nodeID]
	if !ok {
		return NodeState{}, false
	}
	return *st, true
}

// Address returns the dialable address of a remote node.
func (r *NodeRegistry) Address(nodeID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.nodes[nodeID]
	if !ok || st.Address == "" {
		return "", false
	}
	return st.Address, true
}

// Touch records liveness from any inbound RPC.
func (r *NodeRegistry) Touch(nodeID, version string) {
	prev, st, notify := r.markAlive(nodeID, version)
	_ = prev
	if notify {
		r.notify(st, "online")
	}
}

// markAlive refreshes liveness and reports whether the node just came online.
func (r *NodeRegistry) markAlive(nodeID, version string) (prev *NodeState, st *NodeState, notify bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.nodes[nodeID]
	if !ok {
		return nil, nil, false
	}
	prevStatus := st.Status
	st.LastSeen = r.now()
	if st.Status != "online" {
		st.Status = "online"
		st.LastError = ""
	}
	if version != "" {
		st.Version = version
	}
	return nil, st.snapshot(), prevStatus != "online"
}

// notify reports a status transition outside the registry lock.
func (r *NodeRegistry) notify(st *NodeState, status string) {
	if r.OnStatus == nil || st == nil {
		return
	}
	r.OnStatus(st.NodeID, status, st.Version, st.LastSeen)
}

// snapshot copies a state without the lock (callers hold it).
func (n *NodeState) snapshot() *NodeState {
	if n == nil {
		return nil
	}
	cp := *n
	return &cp
}

// ApplyHeartbeat stores telemetry reported by an agent.
func (r *NodeRegistry) ApplyHeartbeat(nodeID string, hb *pb.HeartbeatRequest) {
	_, st, notify := r.applyHeartbeat(nodeID, hb)
	if notify {
		r.notify(st, "online")
	}
}

func (r *NodeRegistry) applyHeartbeat(nodeID string, hb *pb.HeartbeatRequest) (*NodeState, *NodeState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.nodes[nodeID]
	if !ok {
		return nil, nil, false
	}
	prevStatus := st.Status
	st.LastSeen = r.now()
	st.Status = "online"
	st.LastError = ""
	st.CPU = hb.CpuUsage
	st.MemPct = hb.MemPercent
	st.MemUsed, st.MemTotal = hb.MemUsed, hb.MemTotal
	st.Running = int(hb.RunningServers)
	if hb.TotalServers > 0 {
		st.Total = int(hb.TotalServers)
	}
	if hb.Version != "" {
		st.Version = hb.Version
	}
	return nil, st.snapshot(), prevStatus != "online"
}

// ApplyServers replaces the cached server statuses of a node.
func (r *NodeRegistry) ApplyServers(nodeID string, list []*pb.ServerStatusResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.nodes[nodeID]
	if !ok {
		return
	}
	st.LastSeen = r.now()
	st.Status = "online"
	st.LastError = ""
	st.Servers = make(map[string]*pb.ServerStatusResponse, len(list))
	ids := make([]string, 0, len(list))
	for _, s := range list {
		st.Servers[s.ServerId] = s
		ids = append(ids, s.ServerId)
	}
	sort.Strings(ids)
	st.serverIDs = ids
	st.Total = len(list)
	running := 0
	for _, s := range list {
		if s.State == "running" || s.State == "starting" {
			running++
		}
	}
	st.Running = running
}

// UpdateServerStatus merges a single server status (event driven updates).
func (r *NodeRegistry) UpdateServerStatus(nodeID, serverID, state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.nodes[nodeID]
	if !ok {
		return
	}
	if st.Servers == nil {
		st.Servers = map[string]*pb.ServerStatusResponse{}
	}
	if cur, ok := st.Servers[serverID]; ok {
		cur.State = state
		return
	}
	st.Servers[serverID] = &pb.ServerStatusResponse{ServerId: serverID, State: state, Players: []string{}}
}

// MarkUnreachable flags a node whose RPC failed at the transport level.
func (r *NodeRegistry) MarkUnreachable(nodeID, reason string) {
	wasOnline, st := func() (bool, *NodeState) {
		r.mu.Lock()
		defer r.mu.Unlock()
		st, ok := r.nodes[nodeID]
		if !ok {
			return false, nil
		}
		wasOnline := st.Status == "online"
		st.Status = "offline"
		st.LastError = reason
		return wasOnline, st.snapshot()
	}()
	if wasOnline {
		r.notify(st, "unreachable")
	}
}

// Sweep marks nodes offline when they stopped reporting and returns the ids
// that crossed the threshold in this call.
func (r *NodeRegistry) Sweep() []string {
	var offline []string
	var notify []NodeState
	r.mu.Lock()
	for id, st := range r.nodes {
		if st.Status == "online" && r.now().Sub(st.LastSeen) > OfflineAfter {
			st.Status = "offline"
			st.LastError = "no heartbeat"
			offline = append(offline, id)
			notify = append(notify, *st.snapshot())
		}
	}
	r.mu.Unlock()
	for i := range notify {
		r.notify(&notify[i], "offline")
	}
	return offline
}

// ServerStatus returns the cached status of a server and its node id.
func (r *NodeRegistry) ServerStatus(serverID string) (*pb.ServerStatusResponse, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for id, st := range r.nodes {
		if s, ok := st.Servers[serverID]; ok {
			return s, id, true
		}
	}
	return nil, "", false
}

// Counts returns running/total servers per node.
func (r *NodeRegistry) Counts(nodeID string) (running, total int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.nodes[nodeID]
	if !ok {
		return 0, 0
	}
	return st.Running, st.Total
}

// Snapshot returns copies of every node state, sorted by name.
func (r *NodeRegistry) Snapshot() []NodeState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]NodeState, 0, len(r.nodes))
	for _, st := range r.nodes {
		cp := *st
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// IDs returns all known node ids.
func (r *NodeRegistry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.nodes))
	for id := range r.nodes {
		out = append(out, id)
	}
	return out
}
