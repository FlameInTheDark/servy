// Package cluster implements multi-node support.
//
// Model: every node (the panel and each agent) serves the same gRPC NodeService.
// Agents join the panel with a single-use ticket, receive a per-node credential
// and then push registration, telemetry and bus events upstream. The panel
// dials each agent to run server operations, so all management flows (create,
// lifecycle, console, files, config, backups, schedules) work identically for
// local and remote servers.
package cluster

import "github.com/FlameInTheDark/servy/internal/servers"

// NodeID is this process's node identity (set on startup by the panel/agent).
var NodeID = servers.LocalNodeID
