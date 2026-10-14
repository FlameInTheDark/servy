package httpapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/FlameInTheDark/servy/internal/models"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
	"github.com/FlameInTheDark/servy/internal/servers"
)

// Remote-server helpers. Servers may live on any joined node; the panel keeps
// the authoritative row (name, settings, owner, node assignment) while the node
// owns the files and the process. Handlers resolve the owning node first and
// route operations over the cluster client.

// ErrNodeUnreachable is returned when the owning node does not answer.
var ErrNodeUnreachable = errors.New("NODE_UNREACHABLE")

// nodeIDForServer returns the owning node id, or "" when the server runs on
// this panel.
func (a *App) nodeIDForServer(serverID string) string {
	def, err := a.DB.GetServer(serverID)
	if err != nil || def.NodeID == nil {
		return ""
	}
	id := *def.NodeID
	if id == "" || id == servers.LocalNodeID {
		return ""
	}
	return id
}

// remoteRuntime maps the node's last reported status onto the runtime shape the
// API/UI expects, so remote servers look exactly like local ones.
func (a *App) remoteRuntime(serverID string) *models.ServerRuntime {
	if a.Nodes == nil {
		return nil
	}
	st, _, ok := a.Nodes.ServerStatus(serverID)
	if !ok {
		return nil
	}
	rt := models.ServerRuntime{
		ServerID:    serverID,
		State:       models.ServerState(st.State),
		Running:     st.State == "running" || st.State == "starting",
		CPU:         st.Cpu,
		Mem:         uint64(st.Mem),
		Online:      int(st.Online),
		Max:         int(st.Max),
		Players:     st.Players,
		Version:     st.Version,
		Desc:        st.Desc,
		WorldSize:   uint64(st.WorldSize),
		EULAPending: st.EulaPending,
		PingMS:      st.PingMs,
	}
	if rt.Players == nil {
		rt.Players = []string{}
	}
	return &rt
}

// nodeOnline reports whether a node is currently answering.
func (a *App) nodeOnline(nodeID string) bool {
	if a.Nodes == nil {
		return false
	}
	st, ok := a.Nodes.Get(nodeID)
	return ok && st.Online()
}

// parseServerDef decodes a definition payload produced by a node.
func parseServerDef(raw json.RawMessage) (gen.Server, error) {
	var def gen.Server
	if len(raw) == 0 {
		return def, errors.New("empty server definition from node")
	}
	if err := json.Unmarshal(raw, &def); err != nil {
		return def, err
	}
	return def, nil
}

// mirrorRemoteServer stores a node-side definition in the panel database so the
// server shows up in the panel UI with its node assignment.
func (a *App) mirrorRemoteServer(def gen.Server, nodeID string, owner *int64) error {
	nodeIDCopy := nodeID
	def.NodeID = &nodeIDCopy
	if owner != nil {
		def.CreatedBy = owner
	}
	_, err := a.DB.Queries.CreateServer(context.Background(), gen.CreateServerParams{
		ServerID:            def.ServerID,
		Created:             def.Created,
		LastUpdate:          def.LastUpdate,
		ServerName:          def.ServerName,
		Path:                def.Path,
		Executable:          def.Executable,
		LogPath:             def.LogPath,
		ExecutionCommand:    def.ExecutionCommand,
		AutoStart:           def.AutoStart,
		AutoStartDelay:      def.AutoStartDelay,
		CrashDetection:      def.CrashDetection,
		StopCommand:         def.StopCommand,
		ExecutableUpdateUrl: def.ExecutableUpdateUrl,
		ServerIp:            def.ServerIp,
		ServerPort:          def.ServerPort,
		LogsDeleteAfter:     def.LogsDeleteAfter,
		Type:                def.Type,
		ShowStatus:          def.ShowStatus,
		CreatedBy:           def.CreatedBy,
		ShutdownTimeout:     def.ShutdownTimeout,
		IgnoredExits:        def.IgnoredExits,
		AppID:               def.AppID,
		CountPlayers:        def.CountPlayers,
		UpdateWatcher:       def.UpdateWatcher,
		StopAction:          def.StopAction,
		IoMode:              def.IoMode,
		NodeID:              def.NodeID,
		Definitions:         def.Definitions,
		JavaSelection:       def.JavaSelection,
		ExecutionBackend:    def.ExecutionBackend,
		DockerImage:         def.DockerImage,
		DockerEnv:           def.DockerEnv,
		DockerPorts:         def.DockerPorts,
		DockerBinds:         def.DockerBinds,
		DockerNetwork:       def.DockerNetwork,
		DockerWorkdir:       def.DockerWorkdir,
		DockerAutoremove:    def.DockerAutoremove,
		Shell:               def.Shell,
	})
	return err
}

// applyRemoteDefWithOwner mirrors/updates a definition and sets its owner.
func (a *App) applyRemoteDefWithOwner(raw json.RawMessage, nodeID string, owner *int64) error {
	def, err := parseServerDef(raw)
	if err != nil {
		return err
	}
	if _, err := a.DB.GetServer(def.ServerID); err != nil {
		return a.mirrorRemoteServer(def, nodeID, owner)
	}
	return a.applyRemoteDef(raw, nodeID)
}

// applyRemoteDef refreshes the mirrored panel row from a node definition,
// keeping the panel's own ownership/node columns intact.
func (a *App) applyRemoteDef(raw json.RawMessage, nodeID string) error {
	def, err := parseServerDef(raw)
	if err != nil {
		return err
	}
	if _, err := a.DB.GetServer(def.ServerID); err != nil {
		return a.mirrorRemoteServer(def, nodeID, nil)
	}
	return a.DB.Queries.UpdateServerConfig(context.Background(), gen.UpdateServerConfigParams{
		LastUpdate:          def.LastUpdate,
		ServerName:          def.ServerName,
		Path:                def.Path,
		Executable:          def.Executable,
		LogPath:             def.LogPath,
		ExecutionCommand:    def.ExecutionCommand,
		AutoStart:           def.AutoStart,
		AutoStartDelay:      def.AutoStartDelay,
		CrashDetection:      def.CrashDetection,
		StopCommand:         def.StopCommand,
		ExecutableUpdateUrl: def.ExecutableUpdateUrl,
		ServerIp:            def.ServerIp,
		ServerPort:          def.ServerPort,
		LogsDeleteAfter:     def.LogsDeleteAfter,
		Type:                def.Type,
		ShowStatus:          def.ShowStatus,
		ShutdownTimeout:     def.ShutdownTimeout,
		IgnoredExits:        def.IgnoredExits,
		AppID:               def.AppID,
		CountPlayers:        def.CountPlayers,
		UpdateWatcher:       def.UpdateWatcher,
		StopAction:          def.StopAction,
		IoMode:              def.IoMode,
		Definitions:         def.Definitions,
		JavaSelection:       def.JavaSelection,
		ExecutionBackend:    def.ExecutionBackend,
		DockerImage:         def.DockerImage,
		DockerEnv:           def.DockerEnv,
		DockerPorts:         def.DockerPorts,
		DockerBinds:         def.DockerBinds,
		DockerNetwork:       def.DockerNetwork,
		DockerWorkdir:       def.DockerWorkdir,
		DockerAutoremove:    def.DockerAutoremove,
		Shell:               def.Shell,
		ServerID:            def.ServerID,
	})
}
