package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/gofiber/fiber/v3"

	"github.com/FlameInTheDark/servy/internal/authz"
	"github.com/FlameInTheDark/servy/internal/creation"
	"github.com/FlameInTheDark/servy/internal/events"
)

// creationRequest is the API wizard payload (JSON tags) translated into the
// creation service request.
type creationRequest struct {
	CreateType       string  `json:"create_type"`
	Name             string  `json:"name"`
	ServerType       string  `json:"type"`
	Port             int     `json:"port"`
	ArchivePath      string  `json:"archive_path"`
	Executable       string  `json:"executable"`
	ExecutionCommand string  `json:"execution_command"`
	StopCommand      string  `json:"stop_command"`
	MinMem           string  `json:"min_mem"`
	MaxMem           string  `json:"max_mem"`
	JarURL           string  `json:"jar_url"`
	JarName          string  `json:"jar_name"`
	SteamSlug        string  `json:"steam_slug"`
	AppID            string  `json:"app_id"`
	SteamOptions     map[string]string `json:"steam_options"`
	// execution environment (process | docker)
	JavaSelection    string            `json:"java_selection"`
	Shell            string            `json:"shell"`
	ExecutionBackend string            `json:"execution_backend"`
	DockerImage      string            `json:"docker_image"`
	DockerEnv        map[string]string `json:"docker_env"`
	DockerPorts      []string          `json:"docker_ports"`
	DockerBinds      []string          `json:"docker_binds"`
	DockerNetwork    string            `json:"docker_network"`
	DockerWorkdir    string            `json:"docker_workdir"`
	DockerAutoremove bool              `json:"docker_autoremove"`
	BackupDirs       []string          `json:"backup_dirs"`
	Roles            []int64 `json:"roles"`
	AutoStart        bool    `json:"auto_start"`
	CrashDetection   bool    `json:"crash_detection"`
	Node             string  `json:"node"`
	DefinitionURL    string  `json:"definition_url"` // manifest auto-setup
}

func (r creationRequest) toCreationRequest() creation.Request {
	return creation.Request{
		CreateType: r.CreateType, Name: r.Name, ServerType: r.ServerType, Port: r.Port,
		ArchivePath: r.ArchivePath, Executable: r.Executable, ExecutionCommand: r.ExecutionCommand,
		StopCommand: r.StopCommand, MinMem: r.MinMem, MaxMem: r.MaxMem, JarURL: r.JarURL,
		JarName: r.JarName, SteamSlug: r.SteamSlug, AppID: r.AppID, SteamOptions: r.SteamOptions, Roles: r.Roles,
		AutoStart: r.AutoStart, CrashDetection: r.CrashDetection,
		JavaSelection: r.JavaSelection, Shell: r.Shell, ExecutionBackend: r.ExecutionBackend,
		DockerImage: r.DockerImage, DockerEnv: r.DockerEnv, DockerPorts: r.DockerPorts,
		DockerBinds: r.DockerBinds, DockerNetwork: r.DockerNetwork,
		DockerWorkdir: r.DockerWorkdir, DockerAutoremove: r.DockerAutoremove,
		BackupDirs: r.BackupDirs,
	}
}

// createServerOnNode provisions a server on a cluster node: the node runs its
// own creation pipeline (download, extraction, steam install) and the panel
// mirrors the resulting row so the server is fully manageable from here.
func (a *App) createServerOnNode(c fiber.Ctx, p *authz.Principal, nodeID string, cr creation.Request) error {
	if a.Cluster == nil {
		return fail(c, 500, "CLUSTER_DISABLED")
	}
	if !a.nodeOnline(nodeID) {
		return fail(c, 502, "NODE_OFFLINE")
	}
	if err := a.Creation.Validate(cr); err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	// archives referenced by the wizard (zip / jar) live on the panel: stream
	// them to the node and point the node-side request at its spool copy.
	if cr.ArchivePath != "" && (cr.CreateType == creation.CreateImportZip || cr.CreateType == creation.CreateImportJar) {
		local := cr.ArchivePath
		if !filepath.IsAbs(local) {
			local = filepath.Join(a.ImportDir, local)
		}
		f, err := os.Open(local)
		if err != nil {
			return failMsg(c, 400, "INVALID_ARCHIVE", "uploaded archive is no longer available on the panel: "+err.Error())
		}
		nodePath, err := a.Cluster.UploadArchiveFrom(nodeID, f, filepath.Base(local))
		f.Close()
		if err != nil {
			return failMsg(c, 502, "NODE_ERROR", err.Error())
		}
		cr.ArchivePath = nodePath
	}
	serverID := newUUID()
	res, err := a.Cluster.Create(nodeID, serverID, cr)
	if err != nil {
		return failMsg(c, 502, "NODE_ERROR", err.Error())
	}
	def, derr := parseServerDef(res.Def)
	if derr != nil {
		return failMsg(c, 502, "NODE_ERROR", derr.Error())
	}
	var owner *int64
	if p != nil {
		uid := p.UserID
		owner = &uid
	}
	if err := a.mirrorRemoteServer(def, nodeID, owner); err != nil {
		return failMsg(c, 500, "SERVER_ERROR", err.Error())
	}
	if err := a.Backups.EnsureDefault(def, a.BackupRoot); err != nil {
		a.Log.Get("server").Warning("cluster: default backup for %s failed: %v", def.ServerID, err)
	}
	out := fiber.Map{
		"server_id": def.ServerID, "name": def.ServerName, "type": def.Type,
		"remote": true, "node_id": nodeID,
	}
	if len(res.Result) > 0 {
		var result creation.Result
		if err := json.Unmarshal(res.Result, &result); err == nil {
			out["needs_confirm"] = result.NeedsConfirm
			if len(result.DetectedExes) > 0 {
				out["detected_executables"] = result.DetectedExes
			}
			if len(result.SteamOutput) > 0 {
				out["steam_output"] = result.SteamOutput
			}
		}
	}
	a.Log.Audit("", userIDOf(c), def.ServerID, c.IP(), "Created server on node "+nodeID)
	a.Bus.Publish(events.Event{Type: events.EvSendStartReload, ServerID: def.ServerID,
		Data: map[string]interface{}{"node_id": nodeID, "remote": true}})
	return ok(c, out)
}

func bool2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// currentUserFrom pulls the principal back from locals.
func currentUserFrom(c fiber.Ctx) *authz.Principal {
	if p, ok := c.Locals("principal").(*authz.Principal); ok {
		return p
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
